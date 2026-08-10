package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	"recodex-go/internal/version"
	"recodex-go/internal/web"
	"recodex-go/internal/workspace"
)

const (
	maxJSONBodyBytes  = 1 << 20
	maxWSMessageBytes = 1 << 20
	wsAuthTimeout     = 10 * time.Second
	wsWriteTimeout    = 10 * time.Second
	wsPongTimeout     = 60 * time.Second
	wsPingInterval    = 30 * time.Second
)

type Server struct {
	cfg               config.Config
	devices           *auth.Store
	sessions          *session.Manager
	workspaces        workspace.Registry
	root              context.Context
	rootCancel        context.CancelFunc
	pairingMu         sync.Mutex
	pairingToken      string
	pairingUntil      time.Time
	codexVersionValue string
	upgrader          websocket.Upgrader
	relayMu           sync.Mutex
	relayRoot         context.Context
	relayCancel       context.CancelFunc
}

type Envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func NewServer(cfg config.Config) (*Server, error) {
	root, rootCancel := context.WithCancel(context.Background())
	if relayCfg, ok, err := relaysettings.Load(cfg.State.Dir); err != nil {
		rootCancel()
		return nil, err
	} else if ok {
		cfg.Relay = normalizeRelayConfig(relayCfg)
		if err := validateRelayConfig(cfg.Relay); err != nil {
			rootCancel()
			return nil, fmt.Errorf("validate persisted relay config: %w", err)
		}
	}
	devices, err := auth.NewStore(cfg.State.Dir)
	if err != nil {
		rootCancel()
		return nil, err
	}
	manager, err := session.NewManager(root, cfg.State.Dir, codex.NewCLIAdapter(cfg.Codex), cfg.Workspaces)
	if err != nil {
		rootCancel()
		return nil, err
	}
	server := &Server{
		cfg:          cfg,
		devices:      devices,
		sessions:     manager,
		workspaces:   workspace.NewRegistry(cfg.Workspaces),
		root:         root,
		rootCancel:   rootCancel,
		pairingToken: auth.RandomToken(18),
		pairingUntil: time.Now().Add(time.Duration(cfg.Security.PairingTTLSeconds) * time.Second),
		upgrader: websocket.Upgrader{
			CheckOrigin: websocketOriginAllowed,
		},
	}
	server.codexVersionValue = server.readCodexVersion()
	return server, nil
}

func (s *Server) PairingToken() string {
	s.pairingMu.Lock()
	defer s.pairingMu.Unlock()
	return s.pairingTokenLocked()
}

func (s *Server) pairingTokenLocked() string {
	if !s.cfg.Security.PairingEnabled || time.Now().After(s.pairingUntil) {
		return ""
	}
	return s.pairingToken
}

func (s *Server) refreshPairingToken() string {
	s.pairingMu.Lock()
	defer s.pairingMu.Unlock()
	if !s.cfg.Security.PairingEnabled {
		return ""
	}
	if token := s.pairingTokenLocked(); token != "" {
		return token
	}
	s.pairingToken = auth.RandomToken(18)
	s.pairingUntil = time.Now().Add(time.Duration(s.cfg.Security.PairingTTLSeconds) * time.Second)
	return s.pairingToken
}

func (s *Server) pairDevice(token, id, name string) (auth.Device, error) {
	s.pairingMu.Lock()
	defer s.pairingMu.Unlock()
	currentToken := s.pairingTokenLocked()
	if token == "" || currentToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(currentToken)) != 1 {
		return auth.Device{}, errors.New("device is not authorized")
	}
	device, err := s.devices.Pair(id, name)
	if err != nil {
		return auth.Device{}, err
	}
	// Pairing tokens are single-use. A fresh token is available locally for a
	// subsequent device without extending the old token's lifetime.
	s.pairingToken = auth.RandomToken(18)
	s.pairingUntil = time.Now().Add(time.Duration(s.cfg.Security.PairingTTLSeconds) * time.Second)
	return device, nil
}

func (s *Server) Routes() http.Handler {
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /healthz", s.health)
	apiMux.HandleFunc("GET /version", s.version)
	local := func(pattern string, handler http.HandlerFunc) {
		apiMux.Handle(pattern, localOnly(handler))
	}
	local("GET /relay", s.relayGet)
	local("PUT /relay", s.relayPut)
	local("GET /pairing", s.pairing)
	local("GET /context", s.context)
	local("GET /workspaces", s.workspaceList)
	local("GET /devices", s.deviceList)
	local("DELETE /devices/{id}", s.deviceRevoke)
	local("GET /sessions", s.sessionList)
	local("POST /sessions/start", s.sessionStart)
	local("GET /sessions/{id}/events", s.sessionEvents)
	local("POST /sessions/{id}/interrupt", s.sessionInterrupt)
	local("GET /git/status", s.gitStatus)
	local("GET /git/diff", s.gitDiff)
	local("POST /git/commit", s.gitCommit)
	local("POST /git/push", s.gitPush)
	local("POST /git/undo", s.gitUndo)
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version.Value})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"name": "rcc-bridge", "version": version.Value})
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

func (s *Server) sessionList(w http.ResponseWriter, r *http.Request) {
	limit, offset := listPagination(r.URL.Query().Get("limit"), r.URL.Query().Get("offset"))
	records, nextOffset := s.sessions.ListPage(limit, offset)
	writeJSON(w, http.StatusOK, map[string]any{"sessions": records, "nextOffset": nextOffset})
}

func (s *Server) sessionStart(w http.ResponseWriter, r *http.Request) {
	var payload sessionStartPayload
	if err := decodeRequest(w, r, &payload); err != nil {
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
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.sessions.EventsPage(r.PathValue("id"), limit)
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
	if err := decodeRequest(w, r, &payload); err != nil {
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
	if err := decodeRequest(w, r, &payload); err != nil {
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
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if value, err := gitops.CurrentBranch(ctx, workspacePath); err == nil {
			branch = value
		}
	}
	return map[string]any{
		"transport":              "Local",
		"model":                  s.cfg.Codex.Model,
		"models":                 s.cfg.Codex.Models,
		"reasoningEffort":        s.cfg.Codex.ReasoningEffort,
		"reasoningEfforts":       []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"},
		"approvalPolicy":         "on-request",
		"requireConfirmGitWrite": s.cfg.Security.RequireConfirmForGitWrite,
		"branch":                 branch,
		"version":                version.Value,
		"bridgeVersion":          version.Value,
		"codexBinary":            s.cfg.Codex.Binary,
		"codexVersion":           s.codexVersionValue,
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

func (s *Server) readCodexVersion() string {
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
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return value
}

func (s *Server) pairing(w http.ResponseWriter, r *http.Request) {
	token := s.refreshPairingToken()
	host := pairingHost(r, s.cfg.Server)
	scheme := "http"
	wsScheme := "ws"
	if r.TLS != nil {
		scheme = "https"
		wsScheme = "wss"
	}
	origin := scheme + "://" + host
	baseURL := origin + "/api"
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
		"version":        version.Value,
		"host":           host,
		"lanHost":        lanHost,
		"baseUrl":        baseURL,
		"wsUrl":          wsScheme + "://" + host + "/api/ws",
		"token":          token,
		"pairingUri":     pairingURI.String(),
		"pairingEnabled": s.cfg.Security.PairingEnabled && token != "",
		"expiresAt":      s.pairingExpiry(),
		"relay":          s.publicRelayConfig(),
	})
}

func (s *Server) pairingExpiry() time.Time {
	s.pairingMu.Lock()
	defer s.pairingMu.Unlock()
	return s.pairingUntil
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &wsClient{server: s, conn: conn}
	client.run(s.root)
}

func (s *Server) RunRelayClient(ctx context.Context) error {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	s.relayRoot = ctx
	return s.restartRelayLocked()
}

func (s *Server) Close() {
	s.relayMu.Lock()
	if s.relayCancel != nil {
		s.relayCancel()
		s.relayCancel = nil
	}
	s.relayMu.Unlock()
	if s.rootCancel != nil {
		s.rootCancel()
	}
	if s.sessions != nil {
		s.sessions.Close()
	}
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
