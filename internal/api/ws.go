package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"recodex-go/internal/codex"
	"recodex-go/internal/gitops"
	"recodex-go/internal/version"
)

type wsClient struct {
	server    *Server
	conn      *websocket.Conn
	mu        sync.Mutex
	authed    bool
	deviceID  string
	deviceKey string
}

func (c *wsClient) run(ctx context.Context) {
	defer c.conn.Close()
	c.conn.SetReadLimit(maxWSMessageBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(wsAuthTimeout))
	c.conn.SetPongHandler(func(string) error {
		if !c.authed {
			return nil
		}
		return c.conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
	})
	done := make(chan struct{})
	defer close(done)
	go c.keepAlive(ctx, done)
	_ = c.write("bridge.hello", "", map[string]any{
		"version": version.Value,
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
		if env.Type != "auth.hello" && !c.server.devices.IsAuthorized(c.deviceID, c.deviceKey) {
			_ = c.writeError(env.ID, "device_revoked", "device authorization has been revoked")
			return
		}
		c.handle(ctx, env)
		if env.Type == "auth.hello" && !c.authed {
			return
		}
	}
}

func (c *wsClient) keepAlive(ctx context.Context, done <-chan struct{}) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = c.conn.Close()
			return
		case <-done:
			return
		case <-ticker.C:
			if err := c.writeControl(websocket.PingMessage, nil); err != nil {
				_ = c.conn.Close()
				return
			}
		}
	}
}

func (c *wsClient) handle(ctx context.Context, env Envelope) {
	switch env.Type {
	case "auth.hello":
		c.handleAuth(env)
	case "workspace.list":
		_ = c.write("workspace.list.result", env.ID, map[string]any{"workspaces": c.server.workspaces.List()})
	case "session.list":
		c.handleSessionList(env)
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
	c.authed = false
	c.deviceID = ""
	c.deviceKey = ""
	if c.server.devices.Verify(payload.DeviceID, payload.DeviceKey) {
		c.authed = true
		c.deviceID = payload.DeviceID
		c.deviceKey = payload.DeviceKey
		_ = c.conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
		_ = c.write("auth.ok", env.ID, map[string]any{"deviceId": payload.DeviceID, "paired": false})
		return
	}
	if c.server.cfg.Security.PairingEnabled {
		device, err := c.server.pairDevice(payload.Token, payload.DeviceID, payload.DeviceName)
		if err != nil {
			if payload.Token != "" {
				_ = c.writeError(env.ID, "pair_failed", err.Error())
				return
			}
		} else {
			c.authed = true
			c.deviceID = device.ID
			c.deviceKey = device.Key
			_ = c.conn.SetReadDeadline(time.Now().Add(wsPongTimeout))
			_ = c.write("auth.ok", env.ID, map[string]any{
				"deviceId":  device.ID,
				"deviceKey": device.Key,
				"paired":    true,
			})
			return
		}
	}
	_ = c.writeError(env.ID, "auth_failed", "device is not authorized")
}

type sessionListPayload struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

func (c *wsClient) handleSessionList(env Envelope) {
	var payload sessionListPayload
	if len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			_ = c.writeError(env.ID, "bad_payload", err.Error())
			return
		}
	}
	records, nextOffset := c.server.sessions.ListPage(payload.Limit, payload.Offset)
	_ = c.write("session.list.result", env.ID, map[string]any{"sessions": records, "nextOffset": nextOffset})
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
	if len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			_ = c.writeError(env.ID, "bad_payload", err.Error())
			return
		}
	}
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
			if event.Terminal && event.Kind == "done" {
				msgType = "session.done"
			}
			if event.Terminal && event.Kind == "error" {
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
	Limit     int    `json:"limit"`
}

func (c *wsClient) handleSessionEvents(env Envelope) {
	var payload sessionEventsPayload
	if err := decode(env.Payload, &payload); err != nil {
		_ = c.writeError(env.ID, "bad_payload", err.Error())
		return
	}
	events, err := c.server.sessions.EventsPage(payload.SessionID, payload.Limit)
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
	if err := c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout)); err != nil {
		return err
	}
	err = c.conn.WriteJSON(Envelope{Type: msgType, ID: id, Payload: raw})
	if err != nil {
		_ = c.conn.Close()
	}
	return err
}

func (c *wsClient) writeControl(messageType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteControl(messageType, data, time.Now().Add(wsWriteTimeout))
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
