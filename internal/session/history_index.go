package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"recodex-go/internal/config"
	"recodex-go/internal/statefile"
)

type historyIndexEntry struct {
	Size            int64     `json:"size"`
	ModifiedUnixNS  int64     `json:"modifiedUnixNs"`
	ID              string    `json:"id"`
	Workspace       string    `json:"workspace"`
	Prompt          string    `json:"prompt,omitempty"`
	Status          Status    `json:"status,omitempty"`
	CreatedAt       time.Time `json:"createdAt,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty"`
	SummaryComplete bool      `json:"summaryComplete"`
}

func workspaceSet(workspaces []config.WorkspaceConfig) map[string]struct{} {
	result := make(map[string]struct{}, len(workspaces))
	for _, workspace := range workspaces {
		if path := canonicalWorkspacePath(workspace.Path); path != "" {
			result[path] = struct{}{}
		}
	}
	return result
}

func canonicalWorkspacePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if evaluated, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(evaluated)
	}
	return abs
}

func (m *Manager) workspaceAllowed(path string) bool {
	_, ok := m.allowed[canonicalWorkspacePath(path)]
	return ok
}

func (m *Manager) loadHistoryIndex() error {
	raw, err := os.ReadFile(m.historyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &m.historyIndex); err != nil {
		log.Printf("ignore invalid Codex history index: %v", err)
		m.historyIndex = map[string]historyIndexEntry{}
	}
	if m.historyIndex == nil {
		m.historyIndex = map[string]historyIndexEntry{}
	}
	return nil
}

func (m *Manager) refreshCodexHistoryLocked() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	root := filepath.Join(home, ".codex", "sessions")
	nextHistory := map[string]codexHistorySession{}
	nextIndex := map[string]historyIndexEntry{}
	dirty := false

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}

		cached, cachedOK := m.historyIndex[path]
		unchanged := cachedOK && cached.Size == info.Size() && cached.ModifiedUnixNS == info.ModTime().UnixNano()
		allowed := cachedOK && m.workspaceAllowed(cached.Workspace)
		var item codexHistorySession

		switch {
		case unchanged && allowed && cached.SummaryComplete:
			item = cached.session(path)
		case unchanged && !allowed:
			item = cached.session(path)
			if cached.SummaryComplete || cached.Prompt != "" {
				dirty = true
			}
			cached.Prompt = ""
			cached.SummaryComplete = false
		case unchanged:
			item, err = readCodexHistorySummary(path)
			dirty = true
		default:
			item, err = readCodexHistoryIdentity(path)
			if err == nil && m.workspaceAllowed(item.Workspace) {
				item, err = readCodexHistorySummary(path)
			}
			dirty = true
		}
		if err != nil || item.ID == "" || item.Workspace == "" {
			return nil
		}
		if item.Status == StatusRunning {
			age := time.Since(item.UpdatedAt)
			if age < 0 || age > codexHistoryLiveTTL {
				item.Status = StatusDone
				dirty = true
			}
		}

		allowed = m.workspaceAllowed(item.Workspace)
		if allowed {
			if !cached.SummaryComplete || !unchanged {
				item.Prompt = summarizePrompt(item.Prompt)
			}
			nextHistory[item.ID] = item
		}
		nextIndex[path] = indexEntry(info, item, allowed)
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		m.codexHistory = nextHistory
		m.historyIndex = nextIndex
		return nil
	}
	if err != nil {
		return err
	}
	if len(nextIndex) != len(m.historyIndex) {
		dirty = true
	}
	m.codexHistory = nextHistory
	m.historyIndex = nextIndex
	if !dirty {
		return nil
	}
	raw, err := json.MarshalIndent(nextIndex, "", "  ")
	if err != nil {
		return err
	}
	return statefile.WriteFile(m.historyPath, raw, 0o600)
}

func indexEntry(info fs.FileInfo, item codexHistorySession, complete bool) historyIndexEntry {
	entry := historyIndexEntry{
		Size:            info.Size(),
		ModifiedUnixNS:  info.ModTime().UnixNano(),
		ID:              item.ID,
		Workspace:       item.Workspace,
		Status:          item.Status,
		CreatedAt:       item.CreatedAt,
		UpdatedAt:       item.UpdatedAt,
		SummaryComplete: complete,
	}
	if complete {
		entry.Prompt = summarizePrompt(item.Prompt)
	}
	return entry
}

func (entry historyIndexEntry) session(path string) codexHistorySession {
	return codexHistorySession{
		ID:        entry.ID,
		Path:      path,
		Workspace: entry.Workspace,
		Prompt:    entry.Prompt,
		Status:    entry.Status,
		CreatedAt: entry.CreatedAt,
		UpdatedAt: entry.UpdatedAt,
	}
}

func readCodexHistoryIdentity(path string) (codexHistorySession, error) {
	file, err := os.Open(path)
	if err != nil {
		return codexHistorySession{}, err
	}
	defer file.Close()

	item := codexHistorySession{Path: path}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStoredEventBytes)
	for scanner.Scan() {
		var line codexHistoryLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil || line.Type != "session_meta" {
			continue
		}
		var payload codexHistoryMetaPayload
		if err := json.Unmarshal(line.Payload, &payload); err != nil {
			continue
		}
		item.ID = payload.ID
		item.Workspace = filepath.Clean(payload.CWD)
		item.CreatedAt = parseCodexTime(payload.Timestamp)
		if item.CreatedAt.IsZero() {
			item.CreatedAt = parseCodexTime(line.Timestamp)
		}
		item.UpdatedAt = item.CreatedAt
		item.Status = StatusDone
		return item, nil
	}
	if err := scanner.Err(); err != nil {
		return codexHistorySession{}, err
	}
	return codexHistorySession{}, errors.New("Codex session metadata not found")
}

func summarizePrompt(value string) string {
	value = strings.TrimSpace(value)
	const maxRunes = 512
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:maxRunes])) + "..."
}
