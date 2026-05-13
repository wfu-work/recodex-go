package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"recodex-go/internal/auth"
	"recodex-go/internal/codex"
	"recodex-go/internal/config"
	"recodex-go/internal/gitops"
	"recodex-go/internal/session"
	"recodex-go/internal/workspace"
)

const Version = "0.1.0"

type Server struct {
	cfg          config.Config
	devices      *auth.Store
	sessions     *session.Manager
	workspaces   workspace.Registry
	pairingToken string
	pairingUntil time.Time
	upgrader     websocket.Upgrader
}

type Envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func NewServer(cfg config.Config) (*Server, error) {
	devices, err := auth.NewStore(cfg.State.Dir)
	if err != nil {
		return nil, err
	}
	manager, err := session.NewManager(cfg.State.Dir, codex.NewCLIAdapter(cfg.Codex))
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:          cfg,
		devices:      devices,
		sessions:     manager,
		workspaces:   workspace.NewRegistry(cfg.Workspaces),
		pairingToken: auth.RandomToken(18),
		pairingUntil: time.Now().Add(time.Duration(cfg.Security.PairingTTLSeconds) * time.Second),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}, nil
}

func (s *Server) PairingToken() string {
	if !s.cfg.Security.PairingEnabled || time.Now().After(s.pairingUntil) {
		return ""
	}
	return s.pairingToken
}

func (s *Server) refreshPairingToken() string {
	if !s.cfg.Security.PairingEnabled {
		return ""
	}
	if token := s.PairingToken(); token != "" {
		return token
	}
	s.pairingToken = auth.RandomToken(18)
	s.pairingUntil = time.Now().Add(time.Duration(s.cfg.Security.PairingTTLSeconds) * time.Second)
	return s.pairingToken
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /version", s.version)
	mux.HandleFunc("GET /pairing", s.pairing)
	mux.HandleFunc("GET /context", s.context)
	mux.HandleFunc("/ws", s.ws)
	return withCORS(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"name": "rcc-bridge", "version": Version})
}

func (s *Server) context(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.contextPayload(""))
}

func (s *Server) contextPayload(workspacePath string) map[string]any {
	branch := ""
	if workspacePath != "" {
		if snapshot, err := gitops.Snapshot(context.Background(), workspacePath, false); err == nil {
			branch = snapshot.Branch
		}
	}
	return map[string]any{
		"transport":              "Local",
		"model":                  s.cfg.Codex.Model,
		"models":                 s.cfg.Codex.Models,
		"reasoningEffort":        s.cfg.Codex.ReasoningEffort,
		"reasoningEfforts":       []string{"low", "medium", "high", "xhigh"},
		"approvalPolicy":         "on-request",
		"requireConfirmGitWrite": s.cfg.Security.RequireConfirmForGitWrite,
		"branch":                 branch,
		"version":                Version,
	}
}

func (s *Server) pairing(w http.ResponseWriter, r *http.Request) {
	token := s.refreshPairingToken()
	host := r.Host
	if host == "" {
		host = s.cfg.Server.Address()
	}
	baseURL := "http://" + host
	pairingURI := url.URL{
		Scheme: "recodex",
		Host:   "pair",
	}
	query := pairingURI.Query()
	query.Set("baseUrl", baseURL)
	query.Set("token", token)
	pairingURI.RawQuery = query.Encode()

	lanHost := localIP()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        Version,
		"host":           host,
		"lanHost":        lanHost,
		"baseUrl":        baseURL,
		"wsUrl":          "ws://" + host + "/ws",
		"token":          token,
		"pairingUri":     pairingURI.String(),
		"pairingEnabled": s.cfg.Security.PairingEnabled && token != "",
		"expiresAt":      s.pairingUntil,
	})
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &wsClient{server: s, conn: conn}
	client.run(r.Context())
}

type wsClient struct {
	server *Server
	conn   *websocket.Conn
	mu     sync.Mutex
	authed bool
}

func (c *wsClient) run(ctx context.Context) {
	defer c.conn.Close()
	_ = c.write("bridge.hello", "", map[string]any{
		"version": Version,
		"pairing": c.server.PairingToken() != "",
	})

	for {
		var env Envelope
		if err := c.conn.ReadJSON(&env); err != nil {
			return
		}
		if env.Type != "auth.hello" && !c.authed {
			_ = c.writeError(env.ID, "auth_required", "send auth.hello first")
			continue
		}
		c.handle(ctx, env)
	}
}

func (c *wsClient) handle(ctx context.Context, env Envelope) {
	switch env.Type {
	case "auth.hello":
		c.handleAuth(env)
	case "workspace.list":
		_ = c.write("workspace.list.result", env.ID, map[string]any{"workspaces": c.server.workspaces.List()})
	case "session.list":
		_ = c.write("session.list.result", env.ID, map[string]any{"sessions": c.server.sessions.List()})
	case "context.get":
		c.handleContext(env)
	case "session.events":
		c.handleSessionEvents(env)
	case "device.list":
		_ = c.write("device.list.result", env.ID, map[string]any{"devices": c.server.devices.Devices()})
	case "device.revoke":
		c.handleDeviceRevoke(env)
	case "session.start", "session.prompt":
		c.handleSessionStart(ctx, env)
	case "session.interrupt":
		c.handleInterrupt(env)
	case "git.status":
		c.handleGitStatus(ctx, env, false)
	case "git.diff":
		c.handleGitStatus(ctx, env, true)
	case "git.commit":
		c.handleGitCommit(ctx, env)
	case "git.push":
		c.handleGitPush(ctx, env)
	default:
		_ = c.writeError(env.ID, "unknown_type", "unsupported message type: "+env.Type)
	}
}

type authPayload struct {
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	DeviceKey  string `json:"deviceKey"`
	Token      string `json:"token"`
}

func (c *wsClient) handleAuth(env Envelope) {
	var payload authPayload
	if err := decode(env.Payload, &payload); err != nil {
		_ = c.writeError(env.ID, "bad_payload", err.Error())
		return
	}
	if c.server.devices.Verify(payload.DeviceID, payload.DeviceKey) {
		c.authed = true
		_ = c.write("auth.ok", env.ID, map[string]any{"deviceId": payload.DeviceID, "paired": false})
		return
	}
	if c.server.cfg.Security.PairingEnabled && c.server.PairingToken() != "" && payload.Token == c.server.PairingToken() {
		device, err := c.server.devices.Pair(payload.DeviceID, payload.DeviceName)
		if err != nil {
			_ = c.writeError(env.ID, "pair_failed", err.Error())
			return
		}
		c.authed = true
		_ = c.write("auth.ok", env.ID, map[string]any{
			"deviceId":  device.ID,
			"deviceKey": device.Key,
			"paired":    true,
		})
		return
	}
	_ = c.writeError(env.ID, "auth_failed", "device is not authorized")
}

type deviceRevokePayload struct {
	DeviceID string `json:"deviceId"`
}

func (c *wsClient) handleDeviceRevoke(env Envelope) {
	var payload deviceRevokePayload
	if err := decode(env.Payload, &payload); err != nil {
		_ = c.writeError(env.ID, "bad_payload", err.Error())
		return
	}
	if err := c.server.devices.Revoke(payload.DeviceID); err != nil {
		_ = c.writeError(env.ID, "device_revoke_failed", err.Error())
		return
	}
	_ = c.write("device.revoke.result", env.ID, map[string]any{"deviceId": payload.DeviceID})
}

type workspacePayload struct {
	Workspace string `json:"workspace"`
}

type sessionStartPayload struct {
	Workspace       string `json:"workspace"`
	Prompt          string `json:"prompt"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort"`
}

func (c *wsClient) handleContext(env Envelope) {
	var payload workspacePayload
	_ = json.Unmarshal(env.Payload, &payload)
	branch := ""
	if payload.Workspace != "" {
		if ws, err := c.server.workspaces.Resolve(payload.Workspace); err == nil {
			branch = ws.Path
		}
	}
	_ = c.write("context.result", env.ID, c.server.contextPayload(branch))
}

func (c *wsClient) handleSessionStart(ctx context.Context, env Envelope) {
	var payload sessionStartPayload
	if err := decode(env.Payload, &payload); err != nil {
		_ = c.writeError(env.ID, "bad_payload", err.Error())
		return
	}
	ws, err := c.server.workspaces.Resolve(payload.Workspace)
	if err != nil {
		_ = c.writeError(env.ID, "workspace_denied", err.Error())
		return
	}
	record, events, err := c.server.sessions.Start(ctx, codex.StartRequest{
		Workspace:       ws.Path,
		Prompt:          payload.Prompt,
		Model:           payload.Model,
		ReasoningEffort: payload.ReasoningEffort,
	})
	if err != nil {
		_ = c.writeError(env.ID, "session_start_failed", err.Error())
		return
	}
	_ = c.write("session.created", env.ID, record)
	go func() {
		for event := range events {
			msgType := "session.event"
			if event.Kind == "done" {
				msgType = "session.done"
			}
			if event.Kind == "error" {
				msgType = "session.error"
			}
			_ = c.write(msgType, "", event)
		}
	}()
}

type interruptPayload struct {
	SessionID string `json:"sessionId"`
}

type sessionEventsPayload struct {
	SessionID string `json:"sessionId"`
}

func (c *wsClient) handleSessionEvents(env Envelope) {
	var payload sessionEventsPayload
	if err := decode(env.Payload, &payload); err != nil {
		_ = c.writeError(env.ID, "bad_payload", err.Error())
		return
	}
	events, err := c.server.sessions.Events(payload.SessionID)
	if err != nil {
		_ = c.writeError(env.ID, "session_events_failed", err.Error())
		return
	}
	_ = c.write("session.events.result", env.ID, map[string]any{
		"sessionId": payload.SessionID,
		"events":    events,
	})
}

func (c *wsClient) handleInterrupt(env Envelope) {
	var payload interruptPayload
	if err := decode(env.Payload, &payload); err != nil {
		_ = c.writeError(env.ID, "bad_payload", err.Error())
		return
	}
	if err := c.server.sessions.Interrupt(payload.SessionID); err != nil {
		_ = c.writeError(env.ID, "interrupt_failed", err.Error())
		return
	}
	_ = c.write("session.interrupted", env.ID, map[string]any{"sessionId": payload.SessionID})
}

func (c *wsClient) handleGitStatus(ctx context.Context, env Envelope, includeDiff bool) {
	var payload workspacePayload
	if err := decode(env.Payload, &payload); err != nil {
		_ = c.writeError(env.ID, "bad_payload", err.Error())
		return
	}
	ws, err := c.server.workspaces.Resolve(payload.Workspace)
	if err != nil {
		_ = c.writeError(env.ID, "workspace_denied", err.Error())
		return
	}
	result, err := gitops.Snapshot(ctx, ws.Path, includeDiff)
	if err != nil {
		_ = c.writeError(env.ID, "git_failed", err.Error())
		return
	}
	msgType := "git.status.result"
	if includeDiff {
		msgType = "git.diff.result"
	}
	_ = c.write(msgType, env.ID, result)
}

type gitWritePayload struct {
	Workspace string `json:"workspace"`
	Message   string `json:"message"`
	Confirm   bool   `json:"confirm"`
}

func (c *wsClient) handleGitCommit(ctx context.Context, env Envelope) {
	var payload gitWritePayload
	if err := decode(env.Payload, &payload); err != nil {
		_ = c.writeError(env.ID, "bad_payload", err.Error())
		return
	}
	if c.server.cfg.Security.RequireConfirmForGitWrite && !payload.Confirm {
		_ = c.write("confirm.required", env.ID, map[string]any{"action": "git.commit", "message": "Commit requires confirmation."})
		return
	}
	ws, err := c.server.workspaces.Resolve(payload.Workspace)
	if err != nil {
		_ = c.writeError(env.ID, "workspace_denied", err.Error())
		return
	}
	output, err := gitops.Commit(ctx, ws.Path, payload.Message)
	if err != nil {
		_ = c.writeError(env.ID, "git_commit_failed", err.Error()+"\n"+output)
		return
	}
	_ = c.write("git.commit.result", env.ID, map[string]any{"output": output})
}

func (c *wsClient) handleGitPush(ctx context.Context, env Envelope) {
	var payload gitWritePayload
	if err := decode(env.Payload, &payload); err != nil {
		_ = c.writeError(env.ID, "bad_payload", err.Error())
		return
	}
	if c.server.cfg.Security.RequireConfirmForGitWrite && !payload.Confirm {
		_ = c.write("confirm.required", env.ID, map[string]any{"action": "git.push", "message": "Push requires confirmation."})
		return
	}
	ws, err := c.server.workspaces.Resolve(payload.Workspace)
	if err != nil {
		_ = c.writeError(env.ID, "workspace_denied", err.Error())
		return
	}
	output, err := gitops.Push(ctx, ws.Path)
	if err != nil {
		_ = c.writeError(env.ID, "git_push_failed", err.Error()+"\n"+output)
		return
	}
	_ = c.write("git.push.result", env.ID, map[string]any{"output": output})
}

func (c *wsClient) write(msgType, id string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(Envelope{Type: msgType, ID: id, Payload: raw})
}

func (c *wsClient) writeError(id, code, message string) error {
	return c.write("session.error", id, map[string]any{"code": code, "message": message})
}

func decode(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return errors.New("payload is required")
	}
	return json.Unmarshal(raw, target)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ip := ipNet.IP.To4(); ip != nil {
			return ip.String()
		}
	}
	return ""
}
