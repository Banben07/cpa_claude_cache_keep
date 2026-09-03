package main

import (
	"encoding/json"
	"net/http"
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

func TestSessionNextPingAtAnchoredToLastRequest(t *testing.T) {
	lastSeen := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	interval := 50 * time.Minute

	now := lastSeen.Add(10 * time.Minute)
	got := sessionNextPingAt(lastSeen, time.Time{}, now, interval)
	want := lastSeen.Add(interval)
	if !got.Equal(want) {
		t.Fatalf("before first slot: got %s want %s", got, want)
	}

	now = lastSeen.Add(50 * time.Minute)
	got = sessionNextPingAt(lastSeen, time.Time{}, now, interval)
	if !got.Equal(lastSeen.Add(interval)) {
		t.Fatalf("due first slot should stay at lastSeen+interval, got %s", got)
	}

	pinged := lastSeen.Add(51 * time.Minute)
	now = lastSeen.Add(52 * time.Minute)
	got = sessionNextPingAt(lastSeen, pinged, now, interval)
	if !got.Equal(lastSeen.Add(100 * time.Minute)) {
		t.Fatalf("after ping, next slot should stay aligned to lastSeen, got %s", got)
	}

	now = lastSeen.Add(85 * time.Minute)
	laterPing := lastSeen.Add(80 * time.Minute)
	got = sessionNextPingAt(lastSeen, laterPing, now, interval)
	if !got.Equal(lastSeen.Add(100 * time.Minute)) {
		t.Fatalf("late ping must not shift the grid off lastSeen, got %s", got)
	}
}

func TestSessionDue(t *testing.T) {
	lastSeen := time.Now().Add(-51 * time.Minute)
	if !sessionDue(lastSeen, time.Time{}, time.Now(), 50*time.Minute) {
		t.Fatal("expected due after interval with no ping")
	}
	if sessionDue(time.Now(), time.Time{}, time.Now(), 50*time.Minute) {
		t.Fatal("fresh request should not be due")
	}
	if sessionDue(lastSeen, lastSeen.Add(50*time.Minute), time.Now(), 50*time.Minute) {
		t.Fatal("already pinged this slot should not be due")
	}
}

func TestRenderStatusHTMLEmptyAndArmed(t *testing.T) {
	resetSessionsForTest()
	empty := renderStatusHTML()
	if !strings.Contains(empty, "等待对话") {
		t.Fatalf("empty page missing status: %s", empty)
	}

	loopStartedAt = time.Now()
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("写一篇论文大纲", "opus-sys"))
	upsertSession("claude-sonnet-5", "claude", "claude", nil, claudeBody("改这段文案", "sonnet-sys"))
	t.Cleanup(resetSessionsForTest)

	html := renderStatusHTML()
	if !strings.Contains(html, "已记录，等待保活") {
		t.Fatalf("armed page missing status: %s", html)
	}
	if !strings.Contains(html, "写一篇论文大纲") {
		t.Fatal("missing first session label")
	}
	if !strings.Contains(html, "改这段文案") {
		t.Fatal("missing second session label")
	}
	if !strings.Contains(html, "claude-opus-5") || !strings.Contains(html, "claude-sonnet-5") {
		t.Fatal("missing models")
	}
	if !strings.Contains(html, "1h") {
		t.Fatal("missing ttl")
	}
	if !strings.Contains(html, "下次保活") {
		t.Fatal("missing per-session next ping")
	}
	if !strings.Contains(html, "立刻保活") || !strings.Contains(html, "?ping=") {
		t.Fatal("missing per-session manual ping")
	}
	if strings.Contains(html, `http-equiv="refresh"`) {
		t.Fatal("full page refresh causes flicker")
	}
	if !strings.Contains(html, `id="view"`) {
		t.Fatal("missing view root for silent updates")
	}

	items := listSessions()
	if len(items) != 2 {
		t.Fatalf("sessions=%d", len(items))
	}
	setSessionEnabled(items[0].ID, false)
	setSessionEnabled(items[1].ID, false)
	paused := renderStatusHTML()
	if !strings.Contains(paused, "已全部暂停") {
		t.Fatalf("paused page missing status: %s", paused)
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
	if !reg.Capabilities.RequestInterceptor || !reg.Capabilities.ManagementAPI || !reg.Capabilities.UsagePlugin {
		t.Fatal("expected request interceptor, management api, and usage plugin")
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

func TestNormalizeMessagesURL(t *testing.T) {
	if got := normalizeMessagesURL(""); got != defaultMessagesURL {
		t.Fatalf("empty=%q", got)
	}
	if got := normalizeMessagesURL(" /v1/messages "); got != "http://127.0.0.1:8317/v1/messages" {
		t.Fatalf("path=%q", got)
	}
	if got := normalizeMessagesURL("http://127.0.0.1:9000/v1/messages"); got != "http://127.0.0.1:9000/v1/messages" {
		t.Fatalf("full=%q", got)
	}
}

func TestParseConfigMessagesURL(t *testing.T) {
	got := parseConfig(nil)
	if got.MessagesURL != defaultMessagesURL {
		t.Fatalf("default=%q", got.MessagesURL)
	}
	got = parseConfig([]byte("messages_url: http://127.0.0.1:9000/v1/messages\n"))
	if got.MessagesURL != "http://127.0.0.1:9000/v1/messages" {
		t.Fatalf("parsed=%q", got.MessagesURL)
	}
}

func TestCopyPingHeaders(t *testing.T) {
	in := http.Header{
		"Authorization":            []string{"Bearer test"},
		"Content-Length":           []string{"99"},
		"Connection":               []string{"keep-alive"},
		"Anthropic-Beta":           []string{"prompt-caching-2024-07-31"},
		"Accept-Encoding":          []string{"gzip"},
		"X-Claude-Code-Session-Id": []string{"sess-1"},
	}
	out := copyPingHeaders(in)
	if out.Get("Authorization") != "Bearer test" {
		t.Fatal("keep auth")
	}
	if out.Get("Anthropic-Beta") == "" || out.Get("X-Claude-Code-Session-Id") != "sess-1" {
		t.Fatal("keep claude headers")
	}
	if out.Get(keepaliveHeaderKey) != keepaliveHeaderVal {
		t.Fatal("keepalive header")
	}
	if out.Get("Content-Length") != "" || out.Get("Connection") != "" || out.Get("Accept-Encoding") != "" {
		t.Fatalf("hop-by-hop leaked: %v", out)
	}
	if out.Get("Content-Type") != "application/json" {
		t.Fatal("content-type")
	}
}

func TestPingSessionPostsToMessages(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	var gotURL string
	var gotBody []byte
	var gotKeep, gotAuth, gotBeta string
	orig := pingDo
	pingDo = func(url string, headers http.Header, body []byte) (int, []byte, error) {
		gotURL = url
		gotBody = append([]byte(nil), body...)
		gotKeep = headers.Get(keepaliveHeaderKey)
		gotAuth = headers.Get("Authorization")
		gotBeta = headers.Get("Anthropic-Beta")
		return http.StatusOK, []byte(`{"type":"message"}`), nil
	}
	t.Cleanup(func() { pingDo = orig })

	err := pingSession(session{
		ID:    "s1",
		Model: "claude-opus-5",
		Body:  []byte(`{"model":"claude-opus-5","max_tokens":32000,"stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		Headers: http.Header{
			"Authorization":  []string{"Bearer cpa-key"},
			"Anthropic-Beta": []string{"prompt-caching-2024-07-31"},
		},
	}, 1, "http://127.0.0.1:8317/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != "http://127.0.0.1:8317/v1/messages" {
		t.Fatalf("url=%q", gotURL)
	}
	if gotKeep != "1" || gotAuth != "Bearer cpa-key" || gotBeta != "prompt-caching-2024-07-31" {
		t.Fatalf("headers keep=%q auth=%q beta=%q", gotKeep, gotAuth, gotBeta)
	}
	if !strings.Contains(string(gotBody), `"max_tokens":1`) {
		t.Fatalf("body=%s", gotBody)
	}
	if strings.Contains(string(gotBody), `"stream":true`) {
		t.Fatal("stream should be false")
	}
}

func TestPingSessionRejectsNonOK(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	orig := pingDo
	pingDo = func(url string, headers http.Header, body []byte) (int, []byte, error) {
		return http.StatusUnauthorized, []byte(`{"error":{"message":"missing key"}}`), nil
	}
	t.Cleanup(func() { pingDo = orig })
	err := pingSession(session{
		Body: []byte(`{"model":"claude-opus-5","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`),
	}, 1, defaultMessagesURL)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err=%v", err)
	}
}

func TestIsCompactRequest(t *testing.T) {
	cont := claudeBody("This session is being continued from a previous conversation that ran out of context. The conversation is summarized below:", "sys")
	if !isCompactRequest(nil, cont) {
		t.Fatal("post-compact continuation should match")
	}
	summary := []byte(`{"model":"claude-opus-5","max_tokens":64000,"stream":true,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":"CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\nYour task is to create a detailed summary of the conversation so far\nWrite an <analysis> block followed by a <summary> block"}]}`)
	if !isCompactRequest(nil, summary) {
		t.Fatal("compact summarization prompt should match")
	}
	api := []byte(`{"model":"claude-opus-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"context_management":{"edits":[{"type":"compact_20260112"}]}}`)
	if !isCompactRequest(nil, api) {
		t.Fatal("compact API edit should match")
	}
	beta := http.Header{"Anthropic-Beta": []string{"prompt-caching-2024-07-31,compact-2026-01-12"}}
	if !isCompactRequest(beta, claudeBody("普通一问", "sys")) {
		t.Fatal("compact beta header should match")
	}
	if isCompactRequest(nil, claudeBody("How does /compact work?", "sys")) {
		t.Fatal("talking about compact should not match")
	}
}
