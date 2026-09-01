package main

import (
	"encoding/json"
	"testing"
)

func TestLimitOutputKeepsPrefix(t *testing.T) {
	in := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}],"max_tokens":32000,"stream":true}`)
	out, err := limitOutput(in, 1)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["max_tokens"].(float64) != 1 {
		t.Fatalf("max_tokens=%v", parsed["max_tokens"])
	}
	if parsed["stream"].(bool) {
		t.Fatal("stream should be false")
	}
	if parsed["model"] != "claude-sonnet-5" {
		t.Fatalf("model=%v", parsed["model"])
	}
}

func TestIsClaudeUpstream(t *testing.T) {
	if !isClaudeUpstream("claude", "claude-opus-5") {
		t.Fatal("expected claude upstream")
	}
	if isClaudeUpstream("codex", "gpt-5.6-sol") {
		t.Fatal("codex should skip")
	}
}
