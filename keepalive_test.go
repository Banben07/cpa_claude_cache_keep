package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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

func TestInspectBody(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","max_tokens":32000,"stream":true,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"}],"system":[{"type":"text","text":"x","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`)
	info := inspectBody(body)
	if !info.HasMaxTokens {
		t.Fatal("expected max_tokens")
	}
	if !info.Stream {
		t.Fatal("expected stream")
	}
	if info.MessageCount != 2 {
		t.Fatalf("messages=%d", info.MessageCount)
	}
	if info.CacheTTL != "1h" {
		t.Fatalf("ttl=%q", info.CacheTTL)
	}
	if info.CacheControlCount < 1 {
		t.Fatal("expected cache_control")
	}
}

func TestInspectBodyCountTokensShape(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	info := inspectBody(body)
	if info.HasMaxTokens {
		t.Fatal("count_tokens-like body should not have max_tokens")
	}
}

func TestNextPingAt(t *testing.T) {
	start := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
	now := start.Add(10 * time.Minute)
	got := nextPingAt(now, start, 50*time.Minute)
	want := start.Add(50 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestRenderStatusHTMLEmptyAndArmed(t *testing.T) {
	empty := renderStatusHTML()
	if !strings.Contains(empty, "等待快照") {
		t.Fatalf("empty page missing status: %s", empty)
	}
	mu.Lock()
	last = snapshot{
		Model:    "claude-opus-5",
		ToFormat: "claude",
		Body:     []byte(`{"model":"claude-opus-5","max_tokens":16000,"messages":[{"role":"user","content":"hi"}],"system":[{"cache_control":{"ttl":"1h"}}]}`),
		SavedAt:  time.Now(),
	}
	loopStartedAt = time.Now()
	cfg = defaultConfig()
	mu.Unlock()
	defer func() {
		mu.Lock()
		last = snapshot{}
		loopStartedAt = time.Time{}
		lastPingAt = time.Time{}
		lastErr = ""
		mu.Unlock()
	}()
	html := renderStatusHTML()
	if !strings.Contains(html, "已记录，等待保活") {
		t.Fatalf("armed page missing status: %s", html)
	}
	if !strings.Contains(html, "claude-opus-5") {
		t.Fatal("missing model")
	}
	if !strings.Contains(html, "1h") {
		t.Fatal("missing ttl")
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
