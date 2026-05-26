package relaysettings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"recodex-go/internal/config"
)

const filename = "relay.json"

type storedConfig struct {
	Enabled          bool   `json:"enabled"`
	URL              string `json:"url"`
	PublicURL        string `json:"publicUrl"`
	RoomID           string `json:"roomId"`
	RoomToken        string `json:"roomToken,omitempty"`
	AccountGuid      string `json:"accountGuid"`
	ClientID         string `json:"clientId"`
	ClientSecret     string `json:"clientSecret,omitempty"`
	ClientType       string `json:"clientType"`
	TargetClientID   string `json:"targetClientId"`
	ReconnectSeconds int    `json:"reconnectSeconds"`
}

func Load(stateDir string) (config.RelayConfig, bool, error) {
	raw, err := os.ReadFile(path(stateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.RelayConfig{}, false, nil
		}
		return config.RelayConfig{}, false, err
	}
	var stored storedConfig
	if err := json.Unmarshal(raw, &stored); err != nil {
		return config.RelayConfig{}, false, err
	}
	return fromStored(stored), true, nil
}

func Save(stateDir string, cfg config.RelayConfig) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(toStored(cfg), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path(stateDir), raw, 0o600)
}

func path(stateDir string) string {
	return filepath.Join(stateDir, filename)
}

func toStored(cfg config.RelayConfig) storedConfig {
	return storedConfig{
		Enabled:          cfg.Enabled,
		URL:              cfg.URL,
		PublicURL:        cfg.PublicURL,
		RoomID:           cfg.RoomID,
		RoomToken:        cfg.RoomToken,
		AccountGuid:      cfg.AccountGuid,
		ClientID:         cfg.ClientID,
		ClientSecret:     cfg.ClientSecret,
		ClientType:       cfg.ClientType,
		TargetClientID:   cfg.TargetClientID,
		ReconnectSeconds: cfg.ReconnectSeconds,
	}
}

func fromStored(stored storedConfig) config.RelayConfig {
	return config.RelayConfig{
		Enabled:          stored.Enabled,
		URL:              stored.URL,
		PublicURL:        stored.PublicURL,
		RoomID:           stored.RoomID,
		RoomToken:        stored.RoomToken,
		AccountGuid:      stored.AccountGuid,
		ClientID:         stored.ClientID,
		ClientSecret:     stored.ClientSecret,
		ClientType:       stored.ClientType,
		TargetClientID:   stored.TargetClientID,
		ReconnectSeconds: stored.ReconnectSeconds,
	}
}
