package codex

import "testing"

func TestParseLineExtractsCodexJsonText(t *testing.T) {
	event, ok := parseLine("s_1", "stdout", `{"type":"agent_message","message":"hello"}`)
	if !ok {
		t.Fatal("agent message should be emitted")
	}
	if event.Kind != "assistant" {
		t.Fatalf("unexpected kind: %s", event.Kind)
	}
	if event.Text != "hello" {
		t.Fatalf("unexpected text: %s", event.Text)
	}
	if event.Raw == "" {
		t.Fatal("raw line should be preserved")
	}
}

func TestParseLineFallsBackToPlainText(t *testing.T) {
	event, ok := parseLine("s_1", "stdout", "plain output")
	if !ok {
		t.Fatal("plain output should be emitted")
	}
	if event.Kind != "stdout" {
		t.Fatalf("unexpected kind: %s", event.Kind)
	}
	if event.Text != "plain output" {
		t.Fatalf("unexpected text: %s", event.Text)
	}
}

func TestParseLineExtractsNestedItemContentText(t *testing.T) {
	event, ok := parseLine("s_1", "stdout", `{"type":"event_msg","item":{"type":"agent_message","content":[{"type":"output_text","text":"hello from nested content"}]}}`)
	if !ok {
		t.Fatal("nested agent message should be emitted")
	}
	if event.Kind != "assistant" {
		t.Fatalf("unexpected kind: %s", event.Kind)
	}
	if event.Text != "hello from nested content" {
		t.Fatalf("unexpected text: %s", event.Text)
	}
}

func TestParseLineExtractsContentArrayText(t *testing.T) {
	event, ok := parseLine("s_1", "stdout", `{"type":"response.output_text.delta","delta":{"content":[{"text":"first"},{"text":"second"}]}}`)
	if !ok {
		t.Fatal("output text delta should be emitted")
	}
	if event.Kind != "assistant" {
		t.Fatalf("unexpected kind: %s", event.Kind)
	}
	if event.Text != "first\nsecond" {
		t.Fatalf("unexpected text: %s", event.Text)
	}
}

func TestParseLineNormalizesAgentMessageDelta(t *testing.T) {
	event, ok := parseLine("s_1", "stdout", `{"type":"agent_message_delta","delta":"hello"}`)
	if !ok {
		t.Fatal("agent message delta should be emitted")
	}
	if event.Kind != "assistant" {
		t.Fatalf("unexpected kind: %s", event.Kind)
	}
	if event.Text != "hello" {
		t.Fatalf("unexpected text: %s", event.Text)
	}
}

func TestParseLineEmitsStructuralRunningEvents(t *testing.T) {
	event, ok := parseLine("s_1", "stdout", `{"type":"thread.started","thread_id":"abc"}`)
	if ok {
		if event.Kind != "running" {
			t.Fatalf("unexpected kind: %s", event.Kind)
		}
		return
	}
	t.Fatal("structural running event should be emitted")
}

func TestParseLineSuppressesJsonWithoutReadableText(t *testing.T) {
	_, ok := parseLine("s_1", "stdout", `{"type":"turn.completed","usage":{}}`)
	if ok {
		t.Fatal("json without readable text should not be emitted")
	}
}

func TestParseLineEmitsRunningStatus(t *testing.T) {
	event, ok := parseLine("s_1", "stdout", `{"type":"response.in_progress"}`)
	if !ok {
		t.Fatal("running event should be emitted")
	}
	if event.Kind != "running" {
		t.Fatalf("unexpected kind: %s", event.Kind)
	}
	if event.Text == "" {
		t.Fatal("running event should have readable text")
	}
}

func TestParseLineExtractsToolCommand(t *testing.T) {
	event, ok := parseLine("s_1", "stdout", `{"type":"exec_command","arguments":["flutter","analyze"]}`)
	if !ok {
		t.Fatal("tool command should be emitted")
	}
	if event.Kind != "tool_call" {
		t.Fatalf("unexpected kind: %s", event.Kind)
	}
	if event.Text != "command: flutter analyze" {
		t.Fatalf("unexpected text: %s", event.Text)
	}
}

func TestParseLineExtractsUsage(t *testing.T) {
	event, ok := parseLine("s_1", "stdout", `{"type":"turn.completed","usage":{"input_tokens":12,"output_tokens":8,"total_tokens":20}}`)
	if !ok {
		t.Fatal("usage event should be emitted")
	}
	if event.Kind != "token_usage" {
		t.Fatalf("unexpected kind: %s", event.Kind)
	}
	if event.Usage == nil {
		t.Fatal("usage should be set")
	}
	if event.Usage.InputTokens != 12 || event.Usage.OutputTokens != 8 || event.Usage.TotalTokens != 20 {
		t.Fatalf("unexpected usage: %+v", event.Usage)
	}
}
