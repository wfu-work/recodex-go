package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"recodex-go/internal/config"
	"recodex-go/internal/relaysettings"
)

func (s *Server) relayGet(w http.ResponseWriter, _ *http.Request) {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	writeJSON(w, http.StatusOK, relayConfigPayload(s.cfg.Relay))
}

func (s *Server) relayPut(w http.ResponseWriter, r *http.Request) {
	var payload relayUpdatePayload
	if err := decodeRequest(w, r, &payload); err != nil {
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
	previous := s.cfg.Relay
	s.cfg.Relay = next
	if err := s.restartRelayLocked(); err != nil {
		s.cfg.Relay = previous
		rollbackErr := relaysettings.Save(s.cfg.State.Dir, previous)
		restartErr := s.restartRelayLocked()
		if rollbackErr != nil || restartErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"code":    "relay_rollback_failed",
				"message": errors.Join(err, rollbackErr, restartErr).Error(),
			})
			return
		}
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
	ClientID         string `json:"clientId"`
	ClientSecret     string `json:"clientSecret"`
	ClientType       string `json:"clientType"`
	TargetClientID   string `json:"targetClientId"`
	ReconnectSeconds int    `json:"reconnectSeconds"`
}

func (p relayUpdatePayload) toConfig(current config.RelayConfig) config.RelayConfig {
	roomToken := strings.TrimSpace(p.RoomToken)
	if roomToken == "" {
		roomToken = current.RoomToken
	}
	clientSecret := strings.TrimSpace(p.ClientSecret)
	if clientSecret == "" {
		clientSecret = current.ClientSecret
	}
	return normalizeRelayConfig(config.RelayConfig{
		Enabled:          p.Enabled,
		URL:              strings.TrimSpace(p.URL),
		PublicURL:        strings.TrimSpace(p.PublicURL),
		RoomID:           strings.Trim(strings.TrimSpace(p.RoomID), "/"),
		RoomToken:        roomToken,
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
	if cfg.ReconnectSeconds < 1 || cfg.ReconnectSeconds > 3600 {
		return errors.New("relay.reconnect_seconds must be between 1 and 3600")
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
