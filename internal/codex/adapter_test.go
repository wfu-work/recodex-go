package codex

import "testing"

func TestParseLineExtractsCodexJsonText(t *testing.T) {
	event := parseLine("s_1", "stdout", `{"type":"agent_message","message":"hello"}`)
	if event.Kind != "agent_message" {
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
	event := parseLine("s_1", "stdout", "plain output")
	if event.Kind != "stdout" {
		t.Fatalf("unexpected kind: %s", event.Kind)
	}
	if event.Text != "plain output" {
		t.Fatalf("unexpected text: %s", event.Text)
	}
}

func TestParseLineExtractsNestedItemContentText(t *testing.T) {
	event := parseLine("s_1", "stdout", `{"type":"event_msg","item":{"type":"agent_message","content":[{"type":"output_text","text":"hello from nested content"}]}}`)
	if event.Kind != "event_msg" {
		t.Fatalf("unexpected kind: %s", event.Kind)
	}
	if event.Text != "hello from nested content" {
		t.Fatalf("unexpected text: %s", event.Text)
	}
}

func TestParseLineExtractsContentArrayText(t *testing.T) {
	event := parseLine("s_1", "stdout", `{"type":"response.output_text.delta","delta":{"content":[{"text":"first"},{"text":"second"}]}}`)
	if event.Text != "first\nsecond" {
		t.Fatalf("unexpected text: %s", event.Text)
	}
}
