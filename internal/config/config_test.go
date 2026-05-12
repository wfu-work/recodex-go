package config

import "testing"

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(t.TempDir() + "/missing.yaml")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Server.Address() != "127.0.0.1:8765" {
		t.Fatalf("unexpected address: %s", cfg.Server.Address())
	}
	if cfg.Codex.Binary != "codex" {
		t.Fatalf("unexpected codex binary: %s", cfg.Codex.Binary)
	}
	if !cfg.Security.PairingEnabled {
		t.Fatal("pairing should be enabled by default")
	}
}
