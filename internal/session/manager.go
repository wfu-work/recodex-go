package session

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"recodex-go/internal/auth"
	"recodex-go/internal/codex"
	"recodex-go/internal/config"
	"recodex-go/internal/statefile"
)

const (
	codexHistoryIDPrefix = "codex_"
	codexHistoryLiveTTL  = 30 * time.Minute
)

type Status string

const (
	StatusRunning     Status = "running"
	StatusDone        Status = "done"
	StatusInterrupted Status = "interrupted"
	StatusError       Status = "error"
)

type Record struct {
	ID            string    `json:"id"`
	CodexThreadID string    `json:"codexThreadId,omitempty"`
	Workspace     string    `json:"workspace"`
	Prompt        string    `json:"prompt"`
	Status        Status    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Error         string    `json:"error,omitempty"`
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
	root         context.Context
	path         string
	eventsDir    string
	historyPath  string
	codexHistory map[string]codexHistorySession
	historyIndex map[string]historyIndexEntry
	allowed      map[string]struct{}
	adapter      codex.Adapter
	records      map[string]Record
	running      map[string]context.CancelFunc
}

type codexHistorySession struct {
	ID        string
	Path      string
	Workspace string
	Prompt    string
	Status    Status
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewManager(root context.Context, stateDir string, adapter codex.Adapter, workspaces []config.WorkspaceConfig) (*Manager, error) {
	if root == nil {
		root = context.Background()
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	eventsDir := filepath.Join(stateDir, "session_events")
	if err := os.MkdirAll(eventsDir, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{
		root:         root,
		path:         filepath.Join(stateDir, "sessions.json"),
		eventsDir:    eventsDir,
		historyPath:  filepath.Join(stateDir, "codex_history_index.json"),
		codexHistory: map[string]codexHistorySession{},
		historyIndex: map[string]historyIndexEntry{},
		allowed:      workspaceSet(workspaces),
		adapter:      adapter,
		records:      map[string]Record{},
		running:      map[string]context.CancelFunc{},
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	if err := m.reconcileRunningRecords(); err != nil {
		return nil, err
	}
	if err := m.loadHistoryIndex(); err != nil {
		return nil, err
	}
	if err := m.loadCodexHistory(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) List() []Record {
	page, _ := m.ListPage(100, 0)
	return page
}

func (m *Manager) ListPage(limit, offset int) ([]Record, int) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadCodexHistoryLocked(); err != nil {
		log.Printf("refresh Codex history: %v", err)
	}
	out := make([]Record, 0, len(m.records))
	managedThreads := make(map[string]struct{}, len(m.records))
	for _, record := range m.records {
		if record.CodexThreadID != "" {
			managedThreads[record.CodexThreadID] = struct{}{}
		}
		record.Prompt = summarizePrompt(record.Prompt)
		out = append(out, record)
	}
	for _, item := range m.codexHistory {
		if _, managed := managedThreads[item.ID]; managed {
			continue
		}
		out = append(out, Record{
			ID:        codexHistoryIDPrefix + item.ID,
			Workspace: item.Workspace,
			Prompt:    item.Prompt,
			Status:    item.Status,
			CreatedAt: item.CreatedAt,
			UpdatedAt: item.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if offset >= len(out) {
		return nil, 0
	}
	end := offset + limit
	if end >= len(out) {
		return append([]Record(nil), out[offset:]...), 0
	}
	return append([]Record(nil), out[offset:end]...), end
}

func (m *Manager) Events(id string) ([]codex.Event, error) {
	return m.EventsPage(id, 1000)
}

func (m *Manager) EventsPage(id string, limit int) ([]codex.Event, error) {
	if limit <= 0 {
		limit = 1000
	}
	if limit > 5000 {
		limit = 5000
	}
	m.mu.Lock()
	if strings.HasPrefix(id, codexHistoryIDPrefix) {
		if err := m.loadCodexHistoryLocked(); err != nil {
			m.mu.Unlock()
			return nil, err
		}
		historyID := strings.TrimPrefix(id, codexHistoryIDPrefix)
		item, ok := m.codexHistory[historyID]
		if !ok {
			m.mu.Unlock()
			return nil, errors.New("session not found")
		}
		m.mu.Unlock()
		return readCodexHistoryEvents(item, historyID, limit)
	}
	if _, ok := m.records[id]; !ok {
		m.mu.Unlock()
		return nil, errors.New("session not found")
	}
	m.mu.Unlock()
	return m.readEventsPage(id, limit)
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		events, err := readEventFile(filepath.Join(m.eventsDir, entry.Name()), 0)
		if err != nil {
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
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		return Record{}, nil, err
	}
	if err := m.root.Err(); err != nil {
		return Record{}, nil, err
	}
	ctx, cancel := context.WithCancel(m.root)

	req.SessionID = record.ID
	events, err := m.adapter.Run(ctx, req)
	if err != nil {
		cancel()
		return Record{}, nil, err
	}

	m.mu.Lock()
	m.records[record.ID] = record
	m.running[record.ID] = cancel
	if err := m.saveLocked(); err != nil {
		delete(m.records, record.ID)
		delete(m.running, record.ID)
		m.mu.Unlock()
		cancel()
		return Record{}, nil, err
	}
	m.mu.Unlock()

	wrapped := make(chan codex.Event, 256)
	go func() {
		defer close(wrapped)
		for event := range events {
			if err := m.appendEvent(record.ID, event); err != nil {
				log.Printf("persist session %s event: %v", record.ID, err)
			}
			m.applyEvent(record.ID, event)
			offerEvent(wrapped, event)
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
	if event.ThreadID != "" {
		m.attachCodexThreadID(id, event.ThreadID)
	}
	if !event.Terminal {
		return
	}
	switch event.Kind {
	case "done":
		m.update(id, StatusDone, "")
	case "interrupted":
		m.update(id, StatusInterrupted, event.Text)
	case "error":
		m.update(id, StatusError, event.Text)
	}
}

func (m *Manager) attachCodexThreadID(id, threadID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[id]
	if !ok || record.CodexThreadID == threadID {
		return
	}
	record.CodexThreadID = threadID
	m.records[id] = record
	if err := m.saveLocked(); err != nil {
		log.Printf("persist session %s Codex thread ID: %v", id, err)
	}
}

func (m *Manager) update(id string, status Status, message string) {
	m.mu.Lock()
	record, ok := m.records[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	if record.Status != StatusRunning && record.Status != status {
		m.mu.Unlock()
		return
	}
	record.Status = status
	record.UpdatedAt = time.Now()
	record.Error = message
	m.records[id] = record
	var cancel context.CancelFunc
	if status != StatusRunning {
		cancel = m.running[id]
		delete(m.running, id)
	}
	if err := m.saveLocked(); err != nil {
		log.Printf("persist session %s status: %v", id, err)
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) load() error {
	raw, err := os.ReadFile(m.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := json.Unmarshal(raw, &m.records); err != nil {
		return err
	}
	if m.records == nil {
		m.records = map[string]Record{}
	}
	return nil
}

func (m *Manager) saveLocked() error {
	raw, err := json.MarshalIndent(m.records, "", "  ")
	if err != nil {
		return err
	}
	return statefile.WriteFile(m.path, raw, 0o600)
}

func (m *Manager) appendEvent(id string, event codex.Event) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	file, err := os.OpenFile(m.eventsPath(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if written != len(raw) {
		return errors.New("short session event write")
	}
	return closeErr
}

func isLiveStatusEvent(event codex.Event) bool {
	return event.Kind == "running" || event.Kind == "tool_call"
}

func (m *Manager) eventsPath(id string) string {
	return filepath.Join(m.eventsDir, id+".jsonl")
}

func (m *Manager) legacyEventsPath(id string) string {
	return filepath.Join(m.eventsDir, id+".json")
}

func offerEvent(events chan codex.Event, event codex.Event) {
	select {
	case events <- event:
		return
	default:
	}
	// Persistence is authoritative. When a client falls behind, discard one
	// stale live event instead of blocking Codex stdout indefinitely.
	select {
	case <-events:
	default:
	}
	select {
	case events <- event:
	default:
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.running))
	now := time.Now()
	for id, cancel := range m.running {
		cancels = append(cancels, cancel)
		record := m.records[id]
		if record.Status == StatusRunning {
			record.Status = StatusInterrupted
			record.Error = "bridge is shutting down"
			record.UpdatedAt = now
			m.records[id] = record
		}
	}
	clear(m.running)
	if len(cancels) > 0 {
		if err := m.saveLocked(); err != nil {
			log.Printf("persist sessions during shutdown: %v", err)
		}
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (m *Manager) reconcileRunningRecords() error {
	changed := false
	for id, record := range m.records {
		if record.Status != StatusRunning {
			continue
		}
		record.Status = StatusInterrupted
		record.Error = "bridge restarted while session was running"
		record.UpdatedAt = time.Now()
		m.records[id] = record
		changed = true
	}
	if changed {
		return m.saveLocked()
	}
	return nil
}
