package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func claudeBody(firstUser, system string) []byte {
	return []byte(fmt.Sprintf(
		`{"model":"claude-opus-5","max_tokens":16000,"messages":[{"role":"user","content":%q},{"role":"assistant","content":"ok"}],"system":[{"type":"text","text":%q,"cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
		firstUser, system,
	))
}

func TestSessionKeyStableAndDistinct(t *testing.T) {
	system := "same-system-prefix"
	a := claudeBody("第一段任务", system)
	b := claudeBody("另一段完全不同的任务", system)
	if sessionKey("claude-opus-5", a) == sessionKey("claude-opus-5", b) {
		t.Fatal("different first user messages should not share a session")
	}
	grown := []byte(`{"model":"claude-opus-5","max_tokens":16000,"messages":[{"role":"user","content":"第一段任务"},{"role":"assistant","content":"ok"},{"role":"user","content":"继续"}],"system":[{"type":"text","text":"same-system-prefix","cache_control":{"ttl":"1h"}}]}`)
	if sessionKey("claude-opus-5", a) != sessionKey("claude-opus-5", grown) {
		t.Fatal("later turns should keep the same session key")
	}
}

func TestUpsertSkipsCountTokens(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	upsertSession("claude-opus-5", "claude", "claude", nil, []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`))
	if len(listSessions()) != 0 {
		t.Fatal("count_tokens-like bodies must not be kept")
	}
}

func TestUpsertPreservesEnabled(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	body := claudeBody("保活开关", "sys")
	upsertSession("claude-opus-5", "claude", "claude", nil, body)
	items := listSessions()
	if len(items) != 1 || !items[0].Enabled {
		t.Fatalf("new session should start enabled: %+v", items)
	}
	id := items[0].ID
	if !setSessionEnabled(id, false) {
		t.Fatal("toggle failed")
	}
	updated := []byte(`{"model":"claude-opus-5","max_tokens":8000,"messages":[{"role":"user","content":"保活开关"},{"role":"assistant","content":"later"}],"system":[{"type":"text","text":"sys","cache_control":{"ttl":"1h"}}]}`)
	upsertSession("claude-opus-5", "claude", "claude", nil, updated)
	items = listSessions()
	if len(items) != 1 {
		t.Fatalf("expected one session, got %d", len(items))
	}
	if items[0].Enabled {
		t.Fatal("unchecked session should stay unchecked after a new turn")
	}
	if items[0].Info.MessageCount != 2 {
		t.Fatalf("snapshot should update, messages=%d", items[0].Info.MessageCount)
	}
}

func TestMaxSessionsEvictsDisabledFirst(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	mu.Lock()
	cfg.MaxSessions = 2
	mu.Unlock()

	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("对话甲", "sys"))
	time.Sleep(2 * time.Millisecond)
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("对话乙", "sys"))
	if len(listSessions()) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(listSessions()))
	}
	for _, item := range listSessions() {
		if item.Label == "对话甲" && !setSessionEnabled(item.ID, false) {
			t.Fatal("disable failed")
		}
	}
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("对话丙", "sys"))
	got := map[string]bool{}
	for _, item := range listSessions() {
		got[item.Label] = true
	}
	if got["对话甲"] {
		t.Fatalf("disabled oldest session should be evicted first: %+v", got)
	}
	if !got["对话乙"] || !got["对话丙"] {
		t.Fatalf("expected 乙 and 丙, got %+v", got)
	}
}

func TestIdleEvictSkipsEnabledSessions(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	mu.Lock()
	cfg.IdleEvictMinutes = 1
	mu.Unlock()
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("还在保活", "sys"))
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("已经取消", "sys"))
	items := listSessions()
	var keepID, dropID string
	for _, item := range items {
		if item.Label == "已经取消" {
			dropID = item.ID
		} else {
			keepID = item.ID
		}
	}
	setSessionEnabled(dropID, false)
	mu.Lock()
	sessions[keepID].LastSeen = time.Now().Add(-2 * time.Hour)
	sessions[dropID].LastSeen = time.Now().Add(-2 * time.Hour)
	evictSessionsLocked(time.Now(), cfg.MaxSessions)
	mu.Unlock()
	got := listSessions()
	if len(got) != 1 || got[0].ID != keepID {
		t.Fatalf("enabled idle session must stay, unchecked idle session must drop: %+v", got)
	}
}

func TestForgetSession(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("删掉我", "sys"))
	id := listSessions()[0].ID
	if !forgetSession(id) {
		t.Fatal("forget failed")
	}
	if len(listSessions()) != 0 {
		t.Fatal("session still present")
	}
}

func TestHandleStatusToggleRedirects(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("开关跳转", "sys"))
	id := listSessions()[0].ID
	raw, err := json.Marshal(pluginapi.ManagementRequest{
		Path:  "/v0/resource/plugins/claude-cache-keepalive/status",
		Query: url.Values{"toggle": {id}, "on": {"0"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := handleStatusRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	var out pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatal(err)
	}
	if out.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", out.StatusCode, env.Result)
	}
	if loc := out.Headers.Get("Location"); loc != "/v0/resource/plugins/claude-cache-keepalive/status" {
		t.Fatalf("location=%q", loc)
	}
	if listSessions()[0].Enabled {
		t.Fatal("session should be unchecked after toggle")
	}
}

func TestSanitizeSessionID(t *testing.T) {
	if sanitizeSessionID("deadbeefdeadbeef") != "deadbeefdeadbeef" {
		t.Fatal("valid id rejected")
	}
	if sanitizeSessionID("nope") != "" || sanitizeSessionID("zzzzzzzzzzzzzzzz") != "" {
		t.Fatal("invalid id accepted")
	}
}

func TestPingOnceSkipsWhenNothingEnabled(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("先记下来", "sys"))
	setSessionEnabled(listSessions()[0].ID, false)
	sent, n, err := pingOnce()
	if sent || n != 0 || err != nil {
		t.Fatalf("unchecked sessions must not ping: sent=%v n=%d err=%v", sent, n, err)
	}
}
