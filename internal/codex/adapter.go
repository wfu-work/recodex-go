package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"recodex-go/internal/config"
)

var ignoredEventTypes = map[string]bool{
	"thread.started":              true,
	"turn.started":                true,
	"turn.completed":              true,
	"turn.failed":                 true,
	"item.started":                true,
	"item.completed":              true,
	"token_count":                 true,
	"response.created":            true,
	"response.in_progress":        true,
	"response.completed":          true,
	"response.output_item.added":  true,
	"response.output_item.done":   true,
	"response.content_part.added": true,
	"response.content_part.done":  true,
}

type StartRequest struct {
	SessionID       string
	Workspace       string
	Prompt          string
	Model           string
	ReasoningEffort string
}

type Event struct {
	SessionID string    `json:"sessionId"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text,omitempty"`
	Raw       string    `json:"raw,omitempty"`
	Time      time.Time `json:"time"`
}

type Adapter interface {
	Run(ctx context.Context, req StartRequest) (<-chan Event, error)
}

type CLIAdapter struct {
	cfg config.CodexConfig
}

func NewCLIAdapter(cfg config.CodexConfig) CLIAdapter {
	return CLIAdapter{cfg: cfg}
}

func (a CLIAdapter) Run(ctx context.Context, req StartRequest) (<-chan Event, error) {
	if req.Workspace == "" {
		return nil, errors.New("workspace is required")
	}
	if req.Prompt == "" {
		return nil, errors.New("prompt is required")
	}

	args := []string{"exec", "--json", "--color", "never", "--skip-git-repo-check", "--cd", req.Workspace}
	model := req.Model
	if model == "" {
		model = a.cfg.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	reasoningEffort := req.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = a.cfg.ReasoningEffort
	}
	if reasoningEffort != "" {
		args = append(args, "--config", "model_reasoning_effort=\""+reasoningEffort+"\"")
	}
	args = append(args, req.Prompt)

	cmd := exec.CommandContext(ctx, a.cfg.Binary, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}

	events := make(chan Event, 64)
	go func() {
		defer close(events)
		var scanners sync.WaitGroup
		scanners.Add(2)
		go scanLines(&scanners, events, req.SessionID, "codex_stdout", stdout)
		go scanLines(&scanners, events, req.SessionID, "codex_stderr", stderr)

		err := cmd.Wait()
		scanners.Wait()
		if ctx.Err() != nil {
			events <- Event{SessionID: req.SessionID, Kind: "interrupted", Text: ctx.Err().Error(), Time: time.Now()}
			return
		}
		if err != nil {
			events <- Event{SessionID: req.SessionID, Kind: "error", Text: err.Error(), Time: time.Now()}
			return
		}
		events <- Event{SessionID: req.SessionID, Kind: "done", Time: time.Now()}
	}()

	return events, nil
}

func scanLines(wg *sync.WaitGroup, events chan<- Event, sessionID, kind string, reader io.Reader) {
	defer wg.Done()
	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		event, ok := parseLine(sessionID, kind, scanner.Text())
		if ok {
			events <- event
		}
	}
	if err := scanner.Err(); err != nil {
		events <- Event{SessionID: sessionID, Kind: "error", Text: err.Error(), Time: time.Now()}
	}
}

func parseLine(sessionID, fallbackKind, line string) (Event, bool) {
	event := Event{SessionID: sessionID, Kind: fallbackKind, Text: line, Raw: line, Time: time.Now()}
	var data map[string]any
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return event, true
	}
	if value, ok := data["type"].(string); ok && value != "" {
		event.Kind = value
	}
	if text := firstText(data); text != "" {
		event.Text = text
	}
	event.Kind = normalizeKind(event.Kind, data)
	if event.Text == "" || ignoredEventTypes[event.Kind] {
		return Event{}, false
	}
	return event, true
}

func normalizeKind(kind string, data map[string]any) string {
	if item, ok := data["item"].(map[string]any); ok {
		if itemType, ok := item["type"].(string); ok && itemType != "" {
			kind = itemType
		}
	}
	switch {
	case kind == "agent_message",
		kind == "agent_message_delta",
		kind == "assistant_message",
		kind == "assistant_message_delta",
		kind == "message_delta",
		kind == "output_text",
		strings.HasPrefix(kind, "response.output_text"),
		strings.HasPrefix(kind, "response.reasoning_summary_text"):
		return "assistant"
	case kind == "user_message":
		return "user"
	case strings.Contains(kind, "tool_call"):
		return "tool_call"
	case strings.Contains(kind, "error"):
		return "error"
	default:
		return kind
	}
}

func firstText(data map[string]any) string {
	for _, key := range []string{"text", "message", "delta", "output_text", "summary", "content"} {
		if value, ok := data[key].(string); ok && value != "" {
			return value
		}
	}
	for _, key := range []string{"item", "message", "delta", "output", "response"} {
		if child, ok := data[key].(map[string]any); ok {
			if text := firstText(child); text != "" {
				return text
			}
		}
	}
	for _, key := range []string{"content", "items", "output"} {
		if content, ok := data[key].([]any); ok {
			if text := firstTextFromList(content); text != "" {
				return text
			}
		}
	}
	return ""
}

func firstTextFromList(items []any) string {
	var parts []string
	for _, entry := range items {
		switch value := entry.(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				parts = append(parts, value)
			}
		case map[string]any:
			if text := firstText(value); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
