package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"recodex-go/internal/auth"
	"recodex-go/internal/codex"
)

const codexHistoryIDPrefix = "codex_"

type Status string

const (
	StatusRunning     Status = "running"
	StatusDone        Status = "done"
	StatusInterrupted Status = "interrupted"
	StatusError       Status = "error"
)

type Record struct {
	ID        string    `json:"id"`
	Workspace string    `json:"workspace"`
	Prompt    string    `json:"prompt"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Error     string    `json:"error,omitempty"`
}

type UsageSummary struct {
	TodayTokens    int       `json:"todayTokens"`
	MonthTokens    int       `json:"monthTokens"`
	TodayCost      float64   `json:"todayCost"`
	MonthCost      float64   `json:"monthCost"`
	LastUpdated    time.Time `json:"lastUpdated,omitempty"`
	CanReadUsage   bool      `json:"canReadUsage"`
	RateConfigured bool      `json:"rateConfigured"`
}

type Manager struct {
	mu           sync.Mutex
	path         string
	eventsDir    string
	codexHistory map[string]codexHistorySession
	adapter      codex.Adapter
	records      map[string]Record
	running      map[string]context.CancelFunc
}

type codexHistorySession struct {
	ID        string
	Path      string
	Workspace string
	Prompt    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewManager(stateDir string, adapter codex.Adapter) (*Manager, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	eventsDir := filepath.Join(stateDir, "session_events")
	if err := os.MkdirAll(eventsDir, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{
		path:         filepath.Join(stateDir, "sessions.json"),
		eventsDir:    eventsDir,
		codexHistory: map[string]codexHistorySession{},
		adapter:      adapter,
		records:      map[string]Record{},
		running:      map[string]context.CancelFunc{},
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	_ = m.loadCodexHistory()
	return m, nil
}

func (m *Manager) List() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, 0, len(m.records))
	for _, record := range m.records {
		out = append(out, record)
	}
	for _, item := range m.codexHistory {
		out = append(out, Record{
			ID:        codexHistoryIDPrefix + item.ID,
			Workspace: item.Workspace,
			Prompt:    item.Prompt,
			Status:    StatusDone,
			CreatedAt: item.CreatedAt,
			UpdatedAt: item.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func (m *Manager) Events(id string) ([]codex.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasPrefix(id, codexHistoryIDPrefix) {
		historyID := strings.TrimPrefix(id, codexHistoryIDPrefix)
		item, ok := m.codexHistory[historyID]
		if !ok {
			return nil, errors.New("session not found")
		}
		return readCodexHistoryEvents(item.Path, historyID)
	}
	if _, ok := m.records[id]; !ok {
		return nil, errors.New("session not found")
	}
	raw, err := os.ReadFile(m.eventsPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var events []codex.Event
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, err
	}
	return events, nil
}

func (m *Manager) UsageSummary(ratePer1KTokens float64) UsageSummary {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	todayYear, todayMonth, todayDay := now.Date()
	summary := UsageSummary{
		CanReadUsage:   true,
		RateConfigured: ratePer1KTokens > 0,
	}
	entries, err := os.ReadDir(m.eventsDir)
	if err != nil {
		summary.CanReadUsage = false
		return summary
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(m.eventsDir, entry.Name()))
		if err != nil {
			continue
		}
		var events []codex.Event
		if err := json.Unmarshal(raw, &events); err != nil {
			continue
		}
		for _, event := range events {
			if event.Usage == nil || event.Usage.TotalTokens <= 0 {
				continue
			}
			if event.Time.After(summary.LastUpdated) {
				summary.LastUpdated = event.Time
			}
			year, month, day := event.Time.Date()
			if year == todayYear && month == todayMonth && day == todayDay {
				summary.TodayTokens += event.Usage.TotalTokens
			}
			if year == todayYear && month == todayMonth {
				summary.MonthTokens += event.Usage.TotalTokens
			}
		}
	}
	if ratePer1KTokens > 0 {
		summary.TodayCost = float64(summary.TodayTokens) / 1000 * ratePer1KTokens
		summary.MonthCost = float64(summary.MonthTokens) / 1000 * ratePer1KTokens
	}
	return summary
}

func (m *Manager) Start(parent context.Context, req codex.StartRequest) (Record, <-chan codex.Event, error) {
	now := time.Now()
	record := Record{
		ID:        "s_" + auth.RandomToken(9),
		Workspace: req.Workspace,
		Prompt:    req.Prompt,
		Status:    StatusRunning,
		CreatedAt: now,
		UpdatedAt: now,
	}
	ctx, cancel := context.WithCancel(parent)

	req.SessionID = record.ID
	events, err := m.adapter.Run(ctx, req)
	if err != nil {
		cancel()
		return Record{}, nil, err
	}

	m.mu.Lock()
	m.records[record.ID] = record
	m.running[record.ID] = cancel
	_ = m.saveLocked()
	m.mu.Unlock()

	wrapped := make(chan codex.Event, 64)
	go func() {
		defer close(wrapped)
		for event := range events {
			m.appendEvent(record.ID, event)
			m.applyEvent(record.ID, event)
			wrapped <- event
		}
	}()

	return record, wrapped, nil
}

func (m *Manager) Interrupt(id string) error {
	m.mu.Lock()
	cancel, ok := m.running[id]
	m.mu.Unlock()
	if !ok {
		return errors.New("session is not running")
	}
	cancel()
	m.update(id, StatusInterrupted, "")
	return nil
}

func (m *Manager) applyEvent(id string, event codex.Event) {
	switch event.Kind {
	case "done":
		m.update(id, StatusDone, "")
	case "interrupted":
		m.update(id, StatusInterrupted, "")
	case "error":
		m.update(id, StatusError, event.Text)
	}
}

func (m *Manager) update(id string, status Status, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok {
		return
	}
	record.Status = status
	record.UpdatedAt = time.Now()
	record.Error = message
	m.records[id] = record
	if status != StatusRunning {
		delete(m.running, id)
	}
	_ = m.saveLocked()
}

func (m *Manager) load() error {
	raw, err := os.ReadFile(m.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return json.Unmarshal(raw, &m.records)
}

func (m *Manager) saveLocked() error {
	raw, err := json.MarshalIndent(m.records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, raw, 0o600)
}

func (m *Manager) appendEvent(id string, event codex.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var events []codex.Event
	path := m.eventsPath(id)
	raw, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(raw, &events)
	}
	events = append(events, event)
	next, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, next, 0o600)
}

func (m *Manager) eventsPath(id string) string {
	return filepath.Join(m.eventsDir, id+".json")
}

func (m *Manager) loadCodexHistory() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	root := filepath.Join(home, ".codex", "sessions")
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		item, err := readCodexHistorySummary(path)
		if err != nil || item.ID == "" || item.Workspace == "" {
			return nil
		}
		m.codexHistory[item.ID] = item
		return nil
	})
}

type codexHistoryLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexHistoryMetaPayload struct {
	ID        string `json:"id"`
	CWD       string `json:"cwd"`
	Timestamp string `json:"timestamp"`
}

type codexHistoryEventPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func readCodexHistorySummary(path string) (codexHistorySession, error) {
	file, err := os.Open(path)
	if err != nil {
		return codexHistorySession{}, err
	}
	defer file.Close()

	item := codexHistorySession{Path: path}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var line codexHistoryLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if ts := parseCodexTime(line.Timestamp); !ts.IsZero() {
			if item.CreatedAt.IsZero() {
				item.CreatedAt = ts
			}
			item.UpdatedAt = ts
		}
		switch line.Type {
		case "session_meta":
			var payload codexHistoryMetaPayload
			if err := json.Unmarshal(line.Payload, &payload); err == nil {
				item.ID = payload.ID
				item.Workspace = filepath.Clean(payload.CWD)
				if ts := parseCodexTime(payload.Timestamp); !ts.IsZero() && item.CreatedAt.IsZero() {
					item.CreatedAt = ts
				}
			}
		case "event_msg":
			if item.Prompt != "" {
				continue
			}
			var payload codexHistoryEventPayload
			if err := json.Unmarshal(line.Payload, &payload); err == nil && payload.Type == "user_message" {
				item.Prompt = cleanUserPrompt(payload.Message)
			}
		}
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	return item, scanner.Err()
}

func readCodexHistoryEvents(path, sessionID string) ([]codex.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var events []codex.Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var line codexHistoryLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil || line.Type != "event_msg" {
			continue
		}
		var payload codexHistoryEventPayload
		if err := json.Unmarshal(line.Payload, &payload); err != nil || payload.Message == "" {
			continue
		}
		kind := ""
		switch payload.Type {
		case "user_message":
			kind = "user"
		case "agent_message":
			kind = "assistant"
		default:
			continue
		}
		events = append(events, codex.Event{
			SessionID: codexHistoryIDPrefix + sessionID,
			Kind:      kind,
			Text:      cleanUserPrompt(payload.Message),
			Time:      parseCodexTime(line.Timestamp),
		})
	}
	return events, scanner.Err()
}

func cleanUserPrompt(value string) string {
	const marker = "## My request for Codex:"
	if idx := strings.Index(value, marker); idx >= 0 {
		return strings.TrimSpace(value[idx+len(marker):])
	}
	return strings.TrimSpace(value)
}

func parseCodexTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return ts
}
