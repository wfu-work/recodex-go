package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"recodex-go/internal/codex"
)

func (m *Manager) loadCodexHistory() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadCodexHistoryLocked()
}

func (m *Manager) loadCodexHistoryLocked() error {
	return m.refreshCodexHistoryLocked()
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
	scanner.Buffer(make([]byte, 0, 64*1024), maxStoredEventBytes)
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
	age := time.Since(item.UpdatedAt)
	if !completed && age >= 0 && age <= codexHistoryLiveTTL {
		item.Status = StatusRunning
	}
	return item, scanner.Err()
}

func readCodexHistoryEvents(item codexHistorySession, sessionID string, limit int) ([]codex.Event, error) {
	file, err := os.Open(item.Path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var events []codex.Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStoredEventBytes)
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
					events = appendTailEvent(events, event, limit)
				}
			}
			continue
		}
		if payload.Type == "task_complete" {
			events = appendTailEvent(events, codex.Event{
				SessionID: codexHistoryIDPrefix + sessionID,
				Kind:      "done",
				Time:      parseCodexTime(line.Timestamp),
			}, limit)
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
		events = appendTailEvent(events, codex.Event{
			SessionID:   codexHistoryIDPrefix + sessionID,
			Kind:        kind,
			Text:        cleanUserPrompt(payload.Message),
			Time:        parseCodexTime(line.Timestamp),
			Attachments: codexHistoryImageAttachments(payload.Images),
		}, limit)
	}
	if item.Status == StatusRunning {
		events = trimTerminalEventsAfterLastUser(events)
		events = appendLiveCodexHistoryEvent(events, sessionID, item.UpdatedAt)
	}
	return tailEvents(events, limit), scanner.Err()
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
