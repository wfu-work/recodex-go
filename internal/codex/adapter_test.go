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
