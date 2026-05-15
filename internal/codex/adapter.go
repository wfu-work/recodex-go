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

var runningEventLabels = map[string]string{
	"thread.started":              "正在准备会话...",
	"turn.started":                "正在思考...",
	"item.started":                "正在处理任务...",
	"response.created":            "正在连接模型...",
	"response.in_progress":        "正在思考...",
	"response.output_item.added":  "正在生成回复...",
	"response.content_part.added": "正在生成回复...",
}

type StartRequest struct {
	SessionID       string
	Workspace       string
	Prompt          string
	Model           string
	ReasoningEffort string
}

type Event struct {
	SessionID   string       `json:"sessionId"`
	Kind        string       `json:"kind"`
	Text        string       `json:"text,omitempty"`
	Raw         string       `json:"raw,omitempty"`
	Time        time.Time    `json:"time"`
	Usage       *Usage       `json:"usage,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type Attachment struct {
	Type    string `json:"type"`
	Mime    string `json:"mime,omitempty"`
	DataURL string `json:"dataUrl,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
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
	event.Text = ""
	if value, ok := data["type"].(string); ok && value != "" {
		event.Kind = value
	}
	if text := firstText(data); text != "" {
		event.Text = text
	}
	if usage := firstUsage(data); usage != nil {
		event.Kind = "token_usage"
		event.Text = "Token 用量"
		event.Usage = usage
		return event, true
	}
	event.Kind = normalizeKind(event.Kind, data)
	if isToolKind(event.Kind) {
		event.Kind = "tool_call"
		if command := firstCommand(data); command != "" {
			event.Text = "command: " + command
		}
	}
	if label, ok := runningEventLabels[event.Kind]; ok && event.Text == "" {
		event.Kind = "running"
		event.Text = label
		return event, true
	}
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
	case isToolKind(kind):
		return "tool_call"
	case strings.Contains(kind, "error"):
		return "error"
	default:
		return kind
	}
}

func isToolKind(kind string) bool {
	normalized := strings.ToLower(kind)
	return strings.Contains(normalized, "tool_call") ||
		strings.Contains(normalized, "function_call") ||
		strings.Contains(normalized, "exec_command") ||
		strings.Contains(normalized, "command_execution") ||
		strings.Contains(normalized, "shell_command") ||
		strings.Contains(normalized, "local_shell") ||
		strings.Contains(normalized, "mcp_tool")
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

func firstCommand(data map[string]any) string {
	for _, key := range []string{"cmd", "command", "shell_command", "exec_command", "program", "name"} {
		if value, ok := data[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	for _, key := range []string{"arguments", "args", "input", "params"} {
		switch value := data[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		case []any:
			parts := make([]string, 0, len(value))
			for _, entry := range value {
				if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	for _, key := range []string{"item", "call", "tool_call", "function_call", "action", "delta"} {
		if child, ok := data[key].(map[string]any); ok {
			if command := firstCommand(child); command != "" {
				return command
			}
		}
	}
	return ""
}

func firstUsage(data map[string]any) *Usage {
	for _, key := range []string{"usage", "token_count", "tokenCount"} {
		if child, ok := data[key].(map[string]any); ok {
			if usage := usageFromMap(child); usage.TotalTokens > 0 {
				return &usage
			}
		}
	}
	if usage := usageFromMap(data); usage.TotalTokens > 0 {
		return &usage
	}
	for _, key := range []string{"item", "message", "delta", "output", "response"} {
		if child, ok := data[key].(map[string]any); ok {
			if usage := firstUsage(child); usage != nil {
				return usage
			}
		}
	}
	return nil
}

func usageFromMap(data map[string]any) Usage {
	input := intValue(data, "input_tokens", "inputTokens", "prompt_tokens", "promptTokens")
	output := intValue(data, "output_tokens", "outputTokens", "completion_tokens", "completionTokens")
	total := intValue(data, "total_tokens", "totalTokens")
	if total == 0 {
		total = input + output
	}
	return Usage{InputTokens: input, OutputTokens: output, TotalTokens: total}
}

func intValue(data map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := data[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case json.Number:
			if number, err := value.Int64(); err == nil {
				return int(number)
			}
		}
	}
	return 0
}
