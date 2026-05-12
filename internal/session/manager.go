package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"recodex-go/internal/auth"
	"recodex-go/internal/codex"
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

type Manager struct {
	mu      sync.Mutex
	path    string
	adapter codex.Adapter
	records map[string]Record
	running map[string]context.CancelFunc
}

func NewManager(stateDir string, adapter codex.Adapter) (*Manager, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{
		path:    filepath.Join(stateDir, "sessions.json"),
		adapter: adapter,
		records: map[string]Record{},
		running: map[string]context.CancelFunc{},
	}
	return m, m.load()
}

func (m *Manager) List() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, 0, len(m.records))
	for _, record := range m.records {
		out = append(out, record)
	}
	return out
}

func (m *Manager) Start(parent context.Context, workspacePath, prompt string) (Record, <-chan codex.Event, error) {
	now := time.Now()
	record := Record{
		ID:        "s_" + auth.RandomToken(9),
		Workspace: workspacePath,
		Prompt:    prompt,
		Status:    StatusRunning,
		CreatedAt: now,
		UpdatedAt: now,
	}
	ctx, cancel := context.WithCancel(parent)

	events, err := m.adapter.Run(ctx, codex.StartRequest{
		SessionID: record.ID,
		Workspace: workspacePath,
		Prompt:    prompt,
	})
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
