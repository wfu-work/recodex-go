package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
	return net.JoinHostPort(strings.Trim(c.Host, "[]"), strconv.Itoa(c.Port))
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
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("config must contain exactly one YAML document")
		}
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
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535, got %d", cfg.Server.Port)
	}
	if cfg.Codex.Binary == "" {
		cfg.Codex.Binary = "codex"
	}
	if cfg.Codex.Mode == "" {
		cfg.Codex.Mode = "cli"
	}
	if cfg.Codex.Mode != "cli" {
		return fmt.Errorf("codex.mode must be cli, got %q", cfg.Codex.Mode)
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
	cfg.Codex.Models = normalizeModels(cfg.Codex.Model, cfg.Codex.Models)
	if cfg.Codex.ReasoningEffort == "" {
		cfg.Codex.ReasoningEffort = "medium"
	}
	if !validReasoningEffort(cfg.Codex.ReasoningEffort) {
		return fmt.Errorf("unsupported codex.reasoning_effort %q", cfg.Codex.ReasoningEffort)
	}
	if cfg.Relay.ClientType == "" {
		cfg.Relay.ClientType = "bridge"
	}
	if cfg.Relay.ReconnectSeconds == 0 {
		cfg.Relay.ReconnectSeconds = 5
	}
	if cfg.Relay.ReconnectSeconds < 1 || cfg.Relay.ReconnectSeconds > 3600 {
		return fmt.Errorf("relay.reconnect_seconds must be between 1 and 3600, got %d", cfg.Relay.ReconnectSeconds)
	}
	if cfg.Security.PairingTTLSeconds == 0 {
		cfg.Security.PairingTTLSeconds = 300
	}
	if cfg.Security.PairingTTLSeconds < 30 || cfg.Security.PairingTTLSeconds > 86400 {
		return fmt.Errorf("security.pairing_ttl_seconds must be between 30 and 86400, got %d", cfg.Security.PairingTTLSeconds)
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
		workspacePath := cfg.Workspaces[i].Path
		if !filepath.IsAbs(workspacePath) && baseDir != "" {
			workspacePath = filepath.Join(baseDir, workspacePath)
		}
		abs, err := filepath.Abs(workspacePath)
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

func validReasoningEffort(value string) bool {
	switch value {
	case "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func normalizeModels(defaultModel string, models []string) []string {
	result := make([]string, 0, len(models)+1)
	seen := map[string]struct{}{}
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		if _, ok := seen[model]; ok {
			return
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	add(defaultModel)
	for _, model := range models {
		add(model)
	}
	return result
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
	var currentPath string
	trusted := false
	flush := func() {
		if currentPath == "" || !trusted {
			return
		}
		if info, err := os.Stat(currentPath); err == nil && info.IsDir() {
			items = append(items, WorkspaceConfig{
				Name: filepath.Base(currentPath),
				Path: filepath.Clean(currentPath),
			})
		}
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if projectPath, ok := parseProjectHeader(line); ok {
			flush()
			currentPath = projectPath
			trusted = false
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			currentPath = ""
			trusted = false
			continue
		}
		if currentPath != "" {
			key, value, found := strings.Cut(line, "=")
			if found && strings.TrimSpace(key) == "trust_level" {
				unquoted, err := strconv.Unquote(strings.TrimSpace(value))
				trusted = err == nil && unquoted == "trusted"
			}
		}
	}
	flush()
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
	if unquoted, err := strconv.Unquote(`"` + path + `"`); err == nil {
		path = unquoted
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
	return uniqueWorkspaceNames(result)
}

func uniqueWorkspaceNames(items []WorkspaceConfig) []WorkspaceConfig {
	counts := make(map[string]int, len(items))
	for _, item := range items {
		counts[item.Name]++
	}
	used := make(map[string]struct{}, len(items))
	for i := range items {
		name := items[i].Name
		if counts[name] > 1 {
			parent := filepath.Base(filepath.Dir(items[i].Path))
			name = fmt.Sprintf("%s (%s)", name, parent)
		}
		candidate := name
		for suffix := 2; ; suffix++ {
			if _, exists := used[candidate]; !exists {
				break
			}
			candidate = fmt.Sprintf("%s #%d", name, suffix)
		}
		items[i].Name = candidate
		used[candidate] = struct{}{}
	}
	return items
}
