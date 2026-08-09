package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server     ServerConfig      `yaml:"server" json:"server"`
	Codex      CodexConfig       `yaml:"codex" json:"codex"`
	Relay      RelayConfig       `yaml:"relay" json:"relay"`
	Workspaces []WorkspaceConfig `yaml:"workspaces" json:"workspaces"`
	Security   SecurityConfig    `yaml:"security" json:"security"`
	State      StateConfig       `yaml:"state" json:"state"`
}

type ServerConfig struct {
	Host string `yaml:"host" json:"host"`
	Port int    `yaml:"port" json:"port"`
}

func (c ServerConfig) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

type CodexConfig struct {
	Mode            string   `yaml:"mode" json:"mode"`
	Binary          string   `yaml:"binary" json:"binary"`
	Model           string   `yaml:"model" json:"model"`
	Models          []string `yaml:"models" json:"models"`
	ReasoningEffort string   `yaml:"reasoning_effort" json:"reasoningEffort"`
}

type WorkspaceConfig struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
}

type RelayConfig struct {
	Enabled          bool   `yaml:"enabled" json:"enabled"`
	URL              string `yaml:"url" json:"url"`
	PublicURL        string `yaml:"public_url" json:"publicUrl"`
	RoomID           string `yaml:"room_id" json:"roomId"`
	RoomToken        string `yaml:"room_token" json:"-"`
	ClientID         string `yaml:"client_id" json:"clientId"`
	ClientSecret     string `yaml:"client_secret" json:"-"`
	ClientType       string `yaml:"client_type" json:"clientType"`
	TargetClientID   string `yaml:"target_client_id" json:"targetClientId"`
	ReconnectSeconds int    `yaml:"reconnect_seconds" json:"reconnectSeconds"`
}

type SecurityConfig struct {
	PairingEnabled            bool `yaml:"pairing_enabled" json:"pairingEnabled"`
	PairingTTLSeconds         int  `yaml:"pairing_ttl_seconds" json:"pairingTtlSeconds"`
	RequireConfirmForGitWrite bool `yaml:"require_confirm_for_git_write" json:"requireConfirmForGitWrite"`
}

type StateConfig struct {
	Dir string `yaml:"dir" json:"dir"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{Host: "127.0.0.1", Port: 8765},
		Codex:  CodexConfig{Mode: "cli", Binary: "codex"},
		Security: SecurityConfig{
			PairingEnabled:            true,
			PairingTTLSeconds:         300,
			RequireConfirmForGitWrite: true,
		},
		State: StateConfig{Dir: ".recodex"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := normalize(&cfg, ""); err != nil {
				return Config{}, err
			}
			return cfg, nil
		}
		return Config{}, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	if err := normalize(&cfg, filepath.Dir(path)); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func normalize(cfg *Config, baseDir string) error {
	if cfg.Server.Host == "" {
		cfg.Server.Host = "127.0.0.1"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8765
	}
	if cfg.Codex.Binary == "" {
		cfg.Codex.Binary = "codex"
	}
	if cfg.Codex.Mode == "" {
		cfg.Codex.Mode = "cli"
	}
	if cfg.Codex.Model == "" {
		cfg.Codex.Model = "gpt-5.5"
	}
	if len(cfg.Codex.Models) == 0 {
		cfg.Codex.Models = []string{
			"gpt-5.5",
			"gpt-5.4",
			"gpt-5.4-mini",
			"gpt-5.3-codex",
			"gpt-5.2",
		}
	}
	if cfg.Codex.ReasoningEffort == "" {
		cfg.Codex.ReasoningEffort = "medium"
	}
	if cfg.Relay.ClientType == "" {
		cfg.Relay.ClientType = "bridge"
	}
	if cfg.Relay.ReconnectSeconds == 0 {
		cfg.Relay.ReconnectSeconds = 5
	}
	if cfg.Security.PairingTTLSeconds == 0 {
		cfg.Security.PairingTTLSeconds = 300
	}
	if cfg.State.Dir == "" {
		cfg.State.Dir = ".recodex"
	}

	stateDir := cfg.State.Dir
	if !filepath.IsAbs(stateDir) && baseDir != "" {
		stateDir = filepath.Join(baseDir, stateDir)
	}
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	cfg.State.Dir = stateDir

	for i := range cfg.Workspaces {
		abs, err := filepath.Abs(cfg.Workspaces[i].Path)
		if err != nil {
			return err
		}
		cfg.Workspaces[i].Path = filepath.Clean(abs)
		if cfg.Workspaces[i].Name == "" {
			cfg.Workspaces[i].Name = filepath.Base(abs)
		}
	}

	discovered, err := discoverCodexWorkspaces()
	if err != nil {
		return err
	}
	cfg.Workspaces = mergeWorkspaces(cfg.Workspaces, discovered)
	return nil
}

func discoverCodexWorkspaces() ([]WorkspaceConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	return readCodexProjects(filepath.Join(home, ".codex", "config.toml"))
}

func readCodexProjects(path string) ([]WorkspaceConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var items []WorkspaceConfig
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		projectPath, ok := parseProjectHeader(line)
		if !ok {
			continue
		}
		if info, err := os.Stat(projectPath); err != nil || !info.IsDir() {
			continue
		}
		items = append(items, WorkspaceConfig{
			Name: filepath.Base(projectPath),
			Path: filepath.Clean(projectPath),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func parseProjectHeader(line string) (string, bool) {
	const prefix = `[projects."`
	const suffix = `"]`
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
		return "", false
	}
	path := strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)
	if path == "" {
		return "", false
	}
	return filepath.Clean(path), true
}

func mergeWorkspaces(manual, discovered []WorkspaceConfig) []WorkspaceConfig {
	result := make([]WorkspaceConfig, 0, len(manual)+len(discovered))
	seen := map[string]struct{}{}

	add := func(item WorkspaceConfig) {
		path := filepath.Clean(item.Path)
		if path == "" || path == "." {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		if item.Name == "" {
			item.Name = filepath.Base(path)
		}
		item.Path = path
		seen[path] = struct{}{}
		result = append(result, item)
	}

	for _, item := range manual {
		add(item)
	}
	for _, item := range discovered {
		add(item)
	}
	return result
}
