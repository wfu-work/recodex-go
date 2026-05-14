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

func TestParseLineSuppressesStructuralCodexEvents(t *testing.T) {
	_, ok := parseLine("s_1", "stdout", `{"type":"thread.started","thread_id":"abc"}`)
	if ok {
		t.Fatal("structural event should not be emitted")
	}
}

func TestParseLineSuppressesJsonWithoutReadableText(t *testing.T) {
	_, ok := parseLine("s_1", "stdout", `{"type":"turn.completed","usage":{"input_tokens":1}}`)
	if ok {
		t.Fatal("json without readable text should not be emitted")
	}
}
