package main

import (
	"encoding/json"
	"strings"
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

func TestPluginRegistrationHasRequiredMetadata(t *testing.T) {
	reg := pluginRegistration()
	if strings.TrimSpace(reg.Metadata.Name) == "" {
		t.Fatal("name required")
	}
	if strings.TrimSpace(reg.Metadata.Version) == "" {
		t.Fatal("version required")
	}
	if strings.TrimSpace(reg.Metadata.Author) == "" {
		t.Fatal("author required")
	}
	if strings.TrimSpace(reg.Metadata.GitHubRepository) == "" {
		t.Fatal("github repository required by CPA validPlugin")
	}
	if !reg.Capabilities.RequestInterceptor || !reg.Capabilities.ManagementAPI {
		t.Fatal("expected request interceptor and management api")
	}
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded registration
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Metadata.GitHubRepository == "" || !decoded.Capabilities.RequestInterceptor {
		t.Fatalf("round-trip lost required fields: %+v", decoded)
	}
}
