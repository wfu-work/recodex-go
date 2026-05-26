package api

import (
	"testing"

	"recodex-go/internal/config"
)

func TestRelayUpdateKeepsExistingSecretWhenBlank(t *testing.T) {
	current := config.RelayConfig{
		Enabled:          true,
		URL:              "wss://relay.example.com/relay",
		RoomID:           "room-old",
		ClientID:         "cli_old",
		ClientSecret:     "secret-old",
		ClientType:       "bridge",
		ReconnectSeconds: 5,
	}
	payload := relayUpdatePayload{
		Enabled:          true,
		URL:              "wss://relay.example.com/relay",
		RoomID:           "room-new",
		ClientID:         "cli_new",
		ClientSecret:     "",
		ClientType:       "bridge",
		ReconnectSeconds: 10,
	}

	next := payload.toConfig(current)
	if next.ClientSecret != "secret-old" {
		t.Fatalf("blank clientSecret should keep existing secret, got %q", next.ClientSecret)
	}
	if next.RoomID != "room-new" || next.ClientID != "cli_new" || next.ReconnectSeconds != 10 {
		t.Fatalf("unexpected relay config: %+v", next)
	}
}

func TestValidateRelayConfigAllowsDisabledEmptyConfig(t *testing.T) {
	if err := validateRelayConfig(config.RelayConfig{}); err != nil {
		t.Fatalf("disabled empty relay config should be valid: %v", err)
	}
}
