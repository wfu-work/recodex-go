package config

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestLoadResolvesStateDirRelativeToConfigFile(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "bridge.yaml")
	raw := []byte("state:\n  dir: .recodex\n")
	if err := os.WriteFile(configPath, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	defer func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	want := filepath.Join(root, ".recodex")
	if cfg.State.Dir != want {
		t.Fatalf("state dir should be relative to config file, got %q want %q", cfg.State.Dir, want)
	}
}

func TestReadCodexProjects(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	missing := filepath.Join(root, "missing")
	if err := os.Mkdir(app, 0o755); err != nil {
		t.Fatalf("mkdir app: %v", err)
	}

	configPath := filepath.Join(root, "config.toml")
	raw := `[projects."` + app + `"]
trust_level = "trusted"

[projects."` + missing + `"]
trust_level = "trusted"
`
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	items, err := readCodexProjects(configPath)
	if err != nil {
		t.Fatalf("readCodexProjects returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one existing project, got %d", len(items))
	}
	if items[0].Name != "app" || items[0].Path != app {
		t.Fatalf("unexpected project: %+v", items[0])
	}
}

func TestMergeWorkspacesKeepsManualFirstAndDeduplicates(t *testing.T) {
	manual := []WorkspaceConfig{{Name: "manual-app", Path: "/tmp/app"}}
	discovered := []WorkspaceConfig{
		{Name: "auto-app", Path: "/tmp/app"},
		{Name: "other", Path: "/tmp/other"},
	}

	items := mergeWorkspaces(manual, discovered)
	if len(items) != 2 {
		t.Fatalf("expected two workspaces, got %d", len(items))
	}
	if items[0].Name != "manual-app" {
		t.Fatalf("manual workspace should win duplicate path, got %+v", items[0])
	}
	if items[1].Name != "other" {
		t.Fatalf("unexpected second workspace: %+v", items[1])
	}
}
