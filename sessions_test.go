package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	if sessionKey("claude-opus-5", nil, a) == sessionKey("claude-opus-5", nil, b) {
		t.Fatal("different first user messages should not share a session")
	}
	grown := []byte(`{"model":"claude-opus-5","max_tokens":16000,"messages":[{"role":"user","content":"第一段任务"},{"role":"assistant","content":"ok"},{"role":"user","content":"继续"}],"system":[{"type":"text","text":"same-system-prefix","cache_control":{"ttl":"1h"}}]}`)
	if sessionKey("claude-opus-5", nil, a) != sessionKey("claude-opus-5", nil, grown) {
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

func TestRenameSessionPersistsAcrossUpsert(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("保活开关", "sys"))
	id := listSessions()[0].ID
	if !renameSession(id, "工作仓库") {
		t.Fatal("rename failed")
	}
	updated := []byte(`{"model":"claude-opus-5","max_tokens":8000,"messages":[{"role":"user","content":"保活开关"},{"role":"assistant","content":"later"}],"system":[{"type":"text","text":"sys","cache_control":{"ttl":"1h"}}]}`)
	upsertSession("claude-opus-5", "claude", "claude", nil, updated)
	got := listSessions()[0]
	if got.Label != "工作仓库" || !got.CustomLabel {
		t.Fatalf("custom name should survive new turns: %+v", got)
	}
	if !renameSession(id, "  ") {
		t.Fatal("blank rename should reset")
	}
	got = listSessions()[0]
	if got.CustomLabel || got.Label != "保活开关" {
		t.Fatalf("blank name should revert to first user text: %+v", got)
	}
}

func TestHandleStatusRenameRedirects(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("开关跳转", "sys"))
	id := listSessions()[0].ID
	raw, err := json.Marshal(pluginapi.ManagementRequest{
		Path:  "/v0/resource/plugins/claude-cache-keepalive/status",
		Query: url.Values{"rename": {id}, "name": {"实验室"}},
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
		t.Fatalf("status=%d", out.StatusCode)
	}
	if listSessions()[0].Label != "实验室" {
		t.Fatalf("label=%s", listSessions()[0].Label)
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

func TestPingOnceSkipsFreshSession(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("刚聊完", "sys"))
	sent, n, err := pingOnce()
	if sent || n != 0 || err != nil {
		t.Fatalf("fresh session must wait until last request + interval: sent=%v n=%d err=%v", sent, n, err)
	}
}

func TestSubagentKindFromAgentIdHeader(t *testing.T) {
	h := http.Header{"X-Claude-Code-Agent-Id": []string{"a2ecf920ff13c2c95"}}
	if subagentKind(h, claudeBody("看起来像主对话", "sys")) != "subagent" {
		t.Fatal("agent-id header marked every subagent dump")
	}
}

func TestSubagentKindFromBillingFlag(t *testing.T) {
	sub := "x-anthropic-billing-header: cc_version=2.1.251.b36; cc_entrypoint=cli; cc_is_subagent=true;"
	if subagentKind(nil, claudeBody("搜代码", sub)) != "subagent" {
		t.Fatal("cc_is_subagent=true in system should mark a subagent")
	}
	mainSys := "x-anthropic-billing-header: cc_version=2.1.251.0de; cc_entrypoint=cli;"
	if subagentKind(nil, claudeBody("主对话", mainSys)) != "" {
		t.Fatal("main billing line must not be treated as subagent")
	}
}

func TestSubagentKindFromParentAgentHeader(t *testing.T) {
	h := http.Header{"X-Claude-Code-Parent-Agent-Id": []string{"parent-1"}}
	if subagentKind(h, claudeBody("搜一下", "explore")) != "subagent" {
		t.Fatal("parent agent id should mark a subagent")
	}
	if subagentKind(nil, claudeBody("主对话", "sys")) != "" {
		t.Fatal("plain chat should not be a subagent")
	}
}

func TestSubagentKindFromBackgroundAppHeader(t *testing.T) {
	h := http.Header{"X-App": []string{"cli-bg"}}
	if subagentKind(h, claudeBody("后台任务", "sys")) != "background" {
		t.Fatal("x-app=cli-bg should mark a background agent")
	}
}

func TestSubagentKindFromTaskBudget(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","max_tokens":8000,"messages":[{"role":"user","content":"搜代码"}],"system":"explore","task_budget":{"type":"tokens","total":100000,"remaining":80000}}`)
	if subagentKind(nil, body) != "subagent" {
		t.Fatal("task_budget should mark a subagent")
	}
}

func TestUpsertSkipsSubagentSessions(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	h := http.Header{
		"X-Claude-Code-Parent-Agent-Id": []string{"abc"},
		"X-Claude-Code-Agent-Id":        []string{"child"},
	}
	upsertSession("claude-opus-5", "claude", "claude", h, claudeBody("Explore the repo", "You are an explore agent"))
	if len(listSessions()) != 0 {
		t.Fatal("subagent requests must not occupy keepalive slots")
	}
	html := renderStatusHTML()
	if !strings.Contains(html, "已跳过") || !strings.Contains(html, "Explore the repo") {
		t.Fatalf("status page should explain skipped subagent: %s", html)
	}
}

func TestDueSnapshotsUsesLastRequestNotLastPing(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("到期检查", "sys"))
	id := listSessions()[0].ID
	now := time.Now()
	mu.Lock()
	sessions[id].LastSeen = now.Add(-70 * time.Minute)
	sessions[id].LastPingAt = now.Add(-15 * time.Minute)
	mu.Unlock()
	due := dueSnapshots(now, 50*time.Minute)
	if len(due) != 0 {
		t.Fatal("current slot already pinged; last ping time must not pull the next slot earlier")
	}
	mu.Lock()
	sessions[id].LastPingAt = time.Time{}
	mu.Unlock()
	due = dueSnapshots(now, 50*time.Minute)
	if len(due) != 1 {
		t.Fatalf("expected due after last request + interval, got %d", len(due))
	}
}

func TestClaudeSessionIDKeepsCompactInSameRow(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	h := http.Header{"X-Claude-Code-Session-Id": []string{"sess-keep-1"}}
	upsertSession("claude-opus-5", "claude", "claude", h, claudeBody("写插件", "sys"))
	compact := claudeBody("This session is being continued from a previous conversation that ran out of context. The conversation is summarized below:\n- wrote a plugin", "sys-after-compact")
	upsertSession("claude-opus-5", "claude", "claude", h, compact)
	got := listSessions()
	if len(got) != 1 {
		t.Fatalf("compact must not open a second session, got %d: %+v", len(got), got)
	}
	if got[0].Label != "写插件" {
		t.Fatalf("compact should keep the original label, got %q", got[0].Label)
	}
	if got[0].Info.FirstUser == "写插件" {
		t.Fatal("snapshot body should update to the compacted prompt")
	}
}

func TestCompactWithoutSessionIDStaysSeparate(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("原来的任务", "sys"))
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("This session is being continued from a previous conversation that ran out of context. Please continue the conversation from where it left off.", "new-sys"))
	got := listSessions()
	if len(got) != 2 {
		t.Fatalf("without session id, compact must not guess and merge into another chat, got %d", len(got))
	}
}

func TestDifferentClaudeSessionIDsStayDistinct(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	a := http.Header{"X-Claude-Code-Session-Id": []string{"sess-a"}}
	b := http.Header{"X-Claude-Code-Session-Id": []string{"sess-b"}}
	upsertSession("claude-opus-5", "claude", "claude", a, claudeBody("任务甲", "sys"))
	upsertSession("claude-opus-5", "claude", "claude", b, claudeBody("任务乙", "sys"))
	if len(listSessions()) != 2 {
		t.Fatalf("got %d", len(listSessions()))
	}
}

func TestSessionKeyPrefersClaudeSessionID(t *testing.T) {
	h := http.Header{"X-Claude-Code-Session-Id": []string{"same-session"}}
	a := claudeBody("第一段", "sys")
	b := claudeBody("This session is being continued from a previous conversation that ran out of context.", "other-sys")
	if sessionKey("claude-opus-5", h, a) != sessionKey("claude-opus-5", h, b) {
		t.Fatal("same Claude Code session id should keep one key across compact")
	}
	if sessionKey("claude-opus-5", nil, a) == sessionKey("claude-opus-5", nil, b) {
		t.Fatal("without session id, compact text must not collide with the original first message")
	}
}
