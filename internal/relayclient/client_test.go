package relayclient

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"recodex-go/internal/config"
)

func TestSignMatchesRelayPayload(t *testing.T) {
	got := Sign("secret", "cli_001", "bridge", "room_001", "1710000000", "nonce_001")
	want := "2910e70e44dc1b3b71cfde3ab80192d74efda457a91edddc53bb3874ab30c58b"
	if got != want {
		t.Fatalf("unexpected signature: %s", got)
	}
}

func TestSignedURLBuildsRelayQuery(t *testing.T) {
	cfg := config.RelayConfig{
		URL:          "ws://127.0.0.1:4200/relay",
		RoomID:       "recodex-local",
		RoomToken:    "room-token",
		ClientID:     "cli_001",
		ClientSecret: "secret",
		ClientType:   "bridge",
	}
	got, err := SignedURL(cfg, time.Unix(1710000000, 0))
	if err != nil {
		t.Fatalf("SignedURL returned error: %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse signed URL: %v", err)
	}
	if parsed.Scheme != "ws" || parsed.Host != "127.0.0.1:4200" || parsed.Path != "/relay/recodex-local" {
		t.Fatalf("unexpected URL target: %s", got)
	}
	values := parsed.Query()
	if values.Get("clientId") != "cli_001" {
		t.Fatalf("unexpected clientId: %s", values.Get("clientId"))
	}
	if values.Get("clientType") != "bridge" {
		t.Fatalf("unexpected clientType: %s", values.Get("clientType"))
	}
	if values.Get("timestamp") != "1710000000" {
		t.Fatalf("unexpected timestamp: %s", values.Get("timestamp"))
	}
	if values.Get("nonce") == "" {
		t.Fatal("nonce should be set")
	}
	if values.Get("signature") == "" || strings.Contains(values.Get("signature"), " ") {
		t.Fatalf("unexpected signature: %q", values.Get("signature"))
	}
	if values.Get("roomToken") != "room-token" {
		t.Fatalf("unexpected roomToken: %s", values.Get("roomToken"))
	}
	if values.Get("platform") == "" {
		t.Fatal("platform should be set")
	}
	if values.Get("version") != "recodex-go/0.1.0" {
		t.Fatalf("unexpected version: %s", values.Get("version"))
	}
}

func TestPublicConfigUsesPublicURLAndHidesSecrets(t *testing.T) {
	cfg := config.RelayConfig{
		Enabled:      true,
		URL:          "ws://127.0.0.1:8788/relay",
		PublicURL:    "wss://relay.example.com/relay",
		RoomID:       "room_001",
		RoomToken:    "room-token",
		ClientID:     "cli_001",
		ClientSecret: "secret",
		ClientType:   "bridge",
	}
	got := PublicConfig(cfg)
	if got["url"] != "wss://relay.example.com/relay" {
		t.Fatalf("unexpected public URL: %v", got["url"])
	}
	if _, ok := got["clientSecret"]; ok {
		t.Fatal("public config should not expose clientSecret")
	}
	if _, ok := got["roomToken"]; ok {
		t.Fatal("public config should not expose roomToken")
	}
}
