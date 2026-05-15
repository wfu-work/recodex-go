package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Status    Status
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
	_ = m.loadCodexHistoryLocked()
	out := make([]Record, 0, len(m.records))
	for _, record := range m.records {
		out = append(out, record)
	}
	for _, item := range m.codexHistory {
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
	return out
}

func (m *Manager) Events(id string) ([]codex.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasPrefix(id, codexHistoryIDPrefix) {
		_ = m.loadCodexHistoryLocked()
		historyID := strings.TrimPrefix(id, codexHistoryIDPrefix)
		item, ok := m.codexHistory[historyID]
		if !ok {
			return nil, errors.New("session not found")
		}
		return readCodexHistoryEvents(item, historyID)
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
	if len(events) > 0 && isLiveStatusEvent(event) && isLiveStatusEvent(events[len(events)-1]) {
		events[len(events)-1] = event
	} else {
		events = append(events, event)
	}
	next, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, next, 0o600)
}

func isLiveStatusEvent(event codex.Event) bool {
	return event.Kind == "running" || event.Kind == "tool_call"
}

func (m *Manager) eventsPath(id string) string {
	return filepath.Join(m.eventsDir, id+".json")
}

func (m *Manager) loadCodexHistory() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadCodexHistoryLocked()
}

func (m *Manager) loadCodexHistoryLocked() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	root := filepath.Join(home, ".codex", "sessions")
	next := map[string]codexHistorySession{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		item, err := readCodexHistorySummary(path)
		if err != nil || item.ID == "" || item.Workspace == "" {
			return nil
		}
		next[item.ID] = item
		return nil
	})
	if err == nil {
		m.codexHistory = next
	}
	return err
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
	Type    string                           `json:"type"`
	Message string                           `json:"message"`
	Images  []string                         `json:"images"`
	Changes map[string]codexHistoryPatchFile `json:"changes"`
}

type codexHistoryPatchFile struct {
	Type        string  `json:"type"`
	UnifiedDiff string  `json:"unified_diff"`
	MovePath    *string `json:"move_path"`
}

func readCodexHistorySummary(path string) (codexHistorySession, error) {
	file, err := os.Open(path)
	if err != nil {
		return codexHistorySession{}, err
	}
	defer file.Close()

	item := codexHistorySession{Path: path}
	completed := false
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
			var payload codexHistoryEventPayload
			if err := json.Unmarshal(line.Payload, &payload); err == nil {
				if payload.Type == "user_message" {
					completed = false
					item.Prompt = cleanUserPrompt(payload.Message)
				}
				if payload.Type == "task_complete" {
					completed = true
				}
			}
		}
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	item.Status = StatusDone
	if !completed && time.Since(item.UpdatedAt) <= codexHistoryLiveTTL {
		item.Status = StatusRunning
	}
	return item, scanner.Err()
}

func readCodexHistoryEvents(item codexHistorySession, sessionID string) ([]codex.Event, error) {
	file, err := os.Open(item.Path)
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
		if err := json.Unmarshal(line.Payload, &payload); err != nil {
			continue
		}
		if payload.Type == "patch_apply_end" {
			if text := codexHistoryPatchNumstat(payload); text != "" {
				event := codex.Event{
					SessionID: codexHistoryIDPrefix + sessionID,
					Kind:      "git_change",
					Text:      text,
					Time:      parseCodexTime(line.Timestamp),
				}
				if len(events) > 0 && events[len(events)-1].Kind == event.Kind {
					events[len(events)-1].Text += "\n" + event.Text
				} else {
					events = append(events, event)
				}
			}
			continue
		}
		if payload.Type == "task_complete" {
			events = append(events, codex.Event{
				SessionID: codexHistoryIDPrefix + sessionID,
				Kind:      "done",
				Time:      parseCodexTime(line.Timestamp),
			})
			continue
		}
		if payload.Message == "" && len(payload.Images) == 0 {
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
			SessionID:   codexHistoryIDPrefix + sessionID,
			Kind:        kind,
			Text:        cleanUserPrompt(payload.Message),
			Time:        parseCodexTime(line.Timestamp),
			Attachments: codexHistoryImageAttachments(payload.Images),
		})
	}
	if item.Status == StatusRunning {
		events = trimTerminalEventsAfterLastUser(events)
		events = appendLiveCodexHistoryEvent(events, sessionID, item.UpdatedAt)
	}
	return events, scanner.Err()
}

func trimTerminalEventsAfterLastUser(events []codex.Event) []codex.Event {
	lastUserIndex := -1
	for index, event := range events {
		if event.Kind == "user" {
			lastUserIndex = index
		}
	}
	if lastUserIndex < 0 {
		return events
	}
	trimmed := events[:0]
	for index, event := range events {
		if index > lastUserIndex && isTerminalHistoryEvent(event) {
			continue
		}
		trimmed = append(trimmed, event)
	}
	return trimmed
}

func isTerminalHistoryEvent(event codex.Event) bool {
	switch event.Kind {
	case "done", "interrupted", "error":
		return true
	default:
		return false
	}
}

func codexHistoryImageAttachments(images []string) []codex.Attachment {
	if len(images) == 0 {
		return nil
	}
	attachments := make([]codex.Attachment, 0, len(images))
	for _, image := range images {
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}
		attachments = append(attachments, codex.Attachment{
			Type:    "image",
			Mime:    dataURLMime(image),
			DataURL: image,
		})
	}
	return attachments
}

func dataURLMime(value string) string {
	if !strings.HasPrefix(value, "data:") {
		return ""
	}
	meta, _, ok := strings.Cut(value, ",")
	if !ok {
		return ""
	}
	mime := strings.TrimPrefix(meta, "data:")
	mime, _, _ = strings.Cut(mime, ";")
	return mime
}

func codexHistoryPatchNumstat(payload codexHistoryEventPayload) string {
	if len(payload.Changes) == 0 {
		return ""
	}
	lines := make([]string, 0, len(payload.Changes))
	for path, change := range payload.Changes {
		displayPath := path
		if change.MovePath != nil && strings.TrimSpace(*change.MovePath) != "" {
			displayPath = strings.TrimSpace(*change.MovePath)
		}
		added, removed := countUnifiedDiffDelta(change.UnifiedDiff)
		if change.Type == "delete" && added == 0 && removed == 0 {
			removed = 1
		}
		if strings.TrimSpace(displayPath) == "" || added == 0 && removed == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d\t%d\t%s", added, removed, displayPath))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func countUnifiedDiffDelta(diff string) (int, int) {
	added := 0
	removed := 0
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func appendLiveCodexHistoryEvent(events []codex.Event, sessionID string, at time.Time) []codex.Event {
	if len(events) > 0 && isLiveStatusEvent(events[len(events)-1]) {
		events[len(events)-1] = codex.Event{
			SessionID: codexHistoryIDPrefix + sessionID,
			Kind:      "running",
			Text:      "正在同步电脑端 Codex 执行...",
			Time:      at,
		}
		return events
	}
	return append(events, codex.Event{
		SessionID: codexHistoryIDPrefix + sessionID,
		Kind:      "running",
		Text:      "正在同步电脑端 Codex 执行...",
		Time:      at,
	})
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
