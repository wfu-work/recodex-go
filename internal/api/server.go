package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"recodex-go/internal/auth"
	"recodex-go/internal/codex"
	"recodex-go/internal/config"
	"recodex-go/internal/gitops"
	"recodex-go/internal/relayclient"
	"recodex-go/internal/relaysettings"
	"recodex-go/internal/session"
	"recodex-go/internal/web"
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
	relayMu      sync.Mutex
	relayRoot    context.Context
	relayCancel  context.CancelFunc
}

type Envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func NewServer(cfg config.Config) (*Server, error) {
	if relayCfg, ok, err := relaysettings.Load(cfg.State.Dir); err != nil {
		return nil, err
	} else if ok {
		cfg.Relay = normalizeRelayConfig(relayCfg)
	}
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
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /healthz", s.health)
	apiMux.HandleFunc("GET /version", s.version)
	apiMux.HandleFunc("GET /relay", s.relayGet)
	apiMux.HandleFunc("PUT /relay", s.relayPut)
	apiMux.HandleFunc("GET /pairing", s.pairing)
	apiMux.HandleFunc("GET /context", s.context)
	apiMux.HandleFunc("GET /workspaces", s.workspaceList)
	apiMux.HandleFunc("GET /devices", s.deviceList)
	apiMux.HandleFunc("DELETE /devices/{id}", s.deviceRevoke)
	apiMux.HandleFunc("GET /sessions", s.sessionList)
	apiMux.HandleFunc("POST /sessions/start", s.sessionStart)
	apiMux.HandleFunc("GET /sessions/{id}/events", s.sessionEvents)
	apiMux.HandleFunc("POST /sessions/{id}/interrupt", s.sessionInterrupt)
	apiMux.HandleFunc("GET /git/status", s.gitStatus)
	apiMux.HandleFunc("GET /git/diff", s.gitDiff)
	apiMux.HandleFunc("POST /git/commit", s.gitCommit)
	apiMux.HandleFunc("POST /git/push", s.gitPush)
	apiMux.HandleFunc("POST /git/undo", s.gitUndo)
	apiMux.HandleFunc("/ws", s.ws)

	webHandler := web.Handler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.StripPrefix("/api", apiMux).ServeHTTP(w, r)
			return
		}
		webHandler.ServeHTTP(w, r)
	})
	return withCORS(handler)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"name": "rcc-bridge", "version": Version})
}

func (s *Server) relayGet(w http.ResponseWriter, _ *http.Request) {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	writeJSON(w, http.StatusOK, relayConfigPayload(s.cfg.Relay))
}

func (s *Server) relayPut(w http.ResponseWriter, r *http.Request) {
	var payload relayUpdatePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "bad_payload", "message": err.Error()})
		return
	}

	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	next := payload.toConfig(s.cfg.Relay)
	if err := validateRelayConfig(next); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "relay_config_invalid", "message": err.Error()})
		return
	}
	if err := relaysettings.Save(s.cfg.State.Dir, next); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "relay_config_save_failed", "message": err.Error()})
		return
	}
	s.cfg.Relay = next
	if err := s.restartRelayLocked(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "relay_restart_failed", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, relayConfigPayload(s.cfg.Relay))
}

type relayUpdatePayload struct {
	Enabled          bool   `json:"enabled"`
	URL              string `json:"url"`
	PublicURL        string `json:"publicUrl"`
	RoomID           string `json:"roomId"`
	RoomToken        string `json:"roomToken"`
	AccountGuid      string `json:"accountGuid"`
	ClientID         string `json:"clientId"`
	ClientSecret     string `json:"clientSecret"`
	ClientType       string `json:"clientType"`
	TargetClientID   string `json:"targetClientId"`
	ReconnectSeconds int    `json:"reconnectSeconds"`
}

func (p relayUpdatePayload) toConfig(current config.RelayConfig) config.RelayConfig {
	clientSecret := strings.TrimSpace(p.ClientSecret)
	if clientSecret == "" {
		clientSecret = current.ClientSecret
	}
	return normalizeRelayConfig(config.RelayConfig{
		Enabled:          p.Enabled,
		URL:              strings.TrimSpace(p.URL),
		PublicURL:        strings.TrimSpace(p.PublicURL),
		RoomID:           strings.Trim(strings.TrimSpace(p.RoomID), "/"),
		RoomToken:        strings.TrimSpace(p.RoomToken),
		AccountGuid:      strings.TrimSpace(p.AccountGuid),
		ClientID:         strings.TrimSpace(p.ClientID),
		ClientSecret:     clientSecret,
		ClientType:       strings.TrimSpace(p.ClientType),
		TargetClientID:   strings.TrimSpace(p.TargetClientID),
		ReconnectSeconds: p.ReconnectSeconds,
	})
}

func relayConfigPayload(cfg config.RelayConfig) map[string]any {
	cfg = normalizeRelayConfig(cfg)
	return map[string]any{
		"enabled":                cfg.Enabled,
		"url":                    cfg.URL,
		"publicUrl":              cfg.PublicURL,
		"roomId":                 cfg.RoomID,
		"roomTokenConfigured":    cfg.RoomToken != "",
		"accountGuid":            cfg.AccountGuid,
		"clientId":               cfg.ClientID,
		"clientSecretConfigured": cfg.ClientSecret != "",
		"clientType":             cfg.ClientType,
		"targetClientId":         cfg.TargetClientID,
		"reconnectSeconds":       cfg.ReconnectSeconds,
	}
}

func validateRelayConfig(cfg config.RelayConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if err := validateRelayURL("relay.url", cfg.URL); err != nil {
		return err
	}
	if cfg.PublicURL != "" {
		if err := validateRelayURL("relay.public_url", cfg.PublicURL); err != nil {
			return err
		}
	}
	if cfg.RoomID == "" {
		return errors.New("relay.room_id is required")
	}
	if cfg.ClientID == "" {
		return errors.New("relay.client_id is required")
	}
	if cfg.ClientSecret == "" {
		return errors.New("relay.client_secret is required")
	}
	if cfg.ClientType != "bridge" {
		return errors.New("relay.client_type must be bridge")
	}
	if cfg.ReconnectSeconds < 1 {
		return errors.New("relay.reconnect_seconds must be greater than 0")
	}
	return nil
}

func validateRelayURL(name, raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return errors.New(name + " must use ws or wss scheme")
	}
	if parsed.Host == "" {
		return errors.New(name + " host is required")
	}
	return nil
}

func normalizeRelayConfig(cfg config.RelayConfig) config.RelayConfig {
	cfg.URL = strings.TrimSpace(cfg.URL)
	cfg.PublicURL = strings.TrimSpace(cfg.PublicURL)
	cfg.RoomID = strings.Trim(strings.TrimSpace(cfg.RoomID), "/")
	cfg.RoomToken = strings.TrimSpace(cfg.RoomToken)
	cfg.AccountGuid = strings.TrimSpace(cfg.AccountGuid)
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.ClientSecret = strings.TrimSpace(cfg.ClientSecret)
	cfg.ClientType = strings.TrimSpace(cfg.ClientType)
	if cfg.ClientType == "" {
		cfg.ClientType = "bridge"
	}
	cfg.TargetClientID = strings.TrimSpace(cfg.TargetClientID)
	if cfg.ReconnectSeconds <= 0 {
		cfg.ReconnectSeconds = 5
	}
	return cfg
}

func (s *Server) context(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.contextPayload(""))
}

func (s *Server) workspaceList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": s.workspaces.List()})
}

func (s *Server) deviceList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"devices": s.devices.Devices()})
}

func (s *Server) deviceRevoke(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	if err := s.devices.Revoke(deviceID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "device_revoke_failed", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deviceId": deviceID})
}

func (s *Server) sessionList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sessions": s.sessions.List()})
}

func (s *Server) sessionStart(w http.ResponseWriter, r *http.Request) {
	var payload sessionStartPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "bad_payload", "message": err.Error()})
		return
	}
	ws, err := s.workspaces.Resolve(payload.Workspace)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "workspace_denied", "message": err.Error()})
		return
	}
	record, events, err := s.sessions.Start(r.Context(), codex.StartRequest{
		Workspace:       ws.Path,
		Prompt:          payload.Prompt,
		Model:           payload.Model,
		ReasoningEffort: payload.ReasoningEffort,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "session_start_failed", "message": err.Error()})
		return
	}
	go func() {
		for range events {
		}
	}()
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) sessionEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.sessions.Events(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "session_events_failed", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessionId": r.PathValue("id"), "events": events})
}

func (s *Server) sessionInterrupt(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if err := s.sessions.Interrupt(sessionID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "interrupt_failed", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessionId": sessionID})
}

func (s *Server) gitStatus(w http.ResponseWriter, r *http.Request) {
	s.gitSnapshot(w, r, false)
}

func (s *Server) gitDiff(w http.ResponseWriter, r *http.Request) {
	s.gitSnapshot(w, r, true)
}

func (s *Server) gitSnapshot(w http.ResponseWriter, r *http.Request, includeDiff bool) {
	ws, err := s.workspaces.Resolve(r.URL.Query().Get("workspace"))
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "workspace_denied", "message": err.Error()})
		return
	}
	result, err := gitops.Snapshot(r.Context(), ws.Path, includeDiff)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "git_failed", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) gitCommit(w http.ResponseWriter, r *http.Request) {
	var payload gitWritePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "bad_payload", "message": err.Error()})
		return
	}
	if s.cfg.Security.RequireConfirmForGitWrite && !payload.Confirm {
		writeJSON(w, http.StatusOK, map[string]any{"action": "git.commit", "message": "Commit requires confirmation."})
		return
	}
	ws, err := s.workspaces.Resolve(payload.Workspace)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "workspace_denied", "message": err.Error()})
		return
	}
	output, err := gitops.Commit(r.Context(), ws.Path, payload.Message)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "git_commit_failed", "message": err.Error() + "\n" + output})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"output": output})
}

func (s *Server) gitPush(w http.ResponseWriter, r *http.Request) {
	s.gitWrite(w, r, "git.push", "Push requires confirmation.", gitops.Push)
}

func (s *Server) gitUndo(w http.ResponseWriter, r *http.Request) {
	s.gitWrite(w, r, "git.undo", "Undo changes requires confirmation.", gitops.Undo)
}

func (s *Server) gitWrite(w http.ResponseWriter, r *http.Request, action, confirmMessage string, run func(context.Context, string) (string, error)) {
	var payload gitWritePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "bad_payload", "message": err.Error()})
		return
	}
	if s.cfg.Security.RequireConfirmForGitWrite && !payload.Confirm {
		writeJSON(w, http.StatusOK, map[string]any{"action": action, "message": confirmMessage})
		return
	}
	ws, err := s.workspaces.Resolve(payload.Workspace)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "workspace_denied", "message": err.Error()})
		return
	}
	output, err := run(r.Context(), ws.Path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": action + "_failed", "message": err.Error() + "\n" + output})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"output": output})
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
		"bridgeVersion":          Version,
		"codexBinary":            s.cfg.Codex.Binary,
		"codexVersion":           s.codexVersion(),
		"apiKeyConfigured":       apiKeyConfigured(),
		"usage":                  s.sessions.UsageSummary(usageRatePer1KTokens()),
		"relay":                  s.publicRelayConfig(),
	}
}

func (s *Server) publicRelayConfig() map[string]any {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	return relayclient.PublicConfig(s.cfg.Relay)
}

func (s *Server) codexVersion() string {
	binary := s.cfg.Codex.Binary
	if binary == "" {
		binary = "codex"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func apiKeyConfigured() bool {
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

func usageRatePer1KTokens() float64 {
	raw := strings.TrimSpace(os.Getenv("RECODEX_TOKEN_USD_PER_1K"))
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || value < 0 {
		return 0
	}
	return value
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
		"wsUrl":          "ws://" + host + "/api/ws",
		"token":          token,
		"pairingUri":     pairingURI.String(),
		"pairingEnabled": s.cfg.Security.PairingEnabled && token != "",
		"expiresAt":      s.pairingUntil,
		"relay":          s.publicRelayConfig(),
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

func (s *Server) RunRelayClient(ctx context.Context) error {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	s.relayRoot = ctx
	return s.restartRelayLocked()
}

func (s *Server) restartRelayLocked() error {
	if s.relayCancel != nil {
		s.relayCancel()
		s.relayCancel = nil
	}
	if !s.cfg.Relay.Enabled {
		return nil
	}
	root := s.relayRoot
	if root == nil {
		root = context.Background()
	}
	relayCtx, cancel := context.WithCancel(root)
	client, err := relayclient.New(s.cfg.Relay, func(connCtx context.Context, conn *websocket.Conn) {
		wsClient := &wsClient{server: s, conn: conn}
		wsClient.run(connCtx)
	})
	if err != nil {
		cancel()
		return err
	}
	s.relayCancel = cancel
	go client.Run(relayCtx)
	return nil
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
		if strings.HasPrefix(env.Type, "relay.") {
			continue
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
	case "git.undo":
		c.handleGitUndo(ctx, env)
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

func (c *wsClient) handleGitUndo(ctx context.Context, env Envelope) {
	var payload gitWritePayload
	if err := decode(env.Payload, &payload); err != nil {
		_ = c.writeError(env.ID, "bad_payload", err.Error())
		return
	}
	if c.server.cfg.Security.RequireConfirmForGitWrite && !payload.Confirm {
		_ = c.write("confirm.required", env.ID, map[string]any{"action": "git.undo", "message": "Undo changes requires confirmation."})
		return
	}
	ws, err := c.server.workspaces.Resolve(payload.Workspace)
	if err != nil {
		_ = c.writeError(env.ID, "workspace_denied", err.Error())
		return
	}
	output, err := gitops.Undo(ctx, ws.Path)
	if err != nil {
		_ = c.writeError(env.ID, "git_undo_failed", err.Error()+"\n"+output)
		return
	}
	_ = c.write("git.undo.result", env.ID, map[string]any{"output": output})
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
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
