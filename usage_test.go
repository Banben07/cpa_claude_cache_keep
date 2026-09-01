package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestWeightedUnitsPrefersCacheRead(t *testing.T) {
	d := pluginapi.UsageDetail{
		InputTokens:         100000,
		CacheReadTokens:     90000,
		CacheCreationTokens: 5000,
		OutputTokens:        1,
	}
	got := weightedUnits("claude-sonnet-5", d)
	// uncached 5000 + write 10000 + read 9000 + out 5
	if got != 24005 {
		t.Fatalf("units=%d", got)
	}
	opus := weightedUnits("claude-opus-5", d)
	if opus != 24005*5 {
		t.Fatalf("opus units=%d", opus)
	}
}

func TestKeepaliveReserveBlocksChatWhenBudgetKnown(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	mu.Lock()
	cfg.FiveHourBudget = 100000
	cfg.ReservePercent = 10
	usageEvents = []usageEvent{{At: time.Now(), Units: 91000, Keepalive: false}}
	mu.Unlock()
	snap := currentBudget(time.Now())
	if !snap.ChatBlocked {
		t.Fatal("expected CPA chat to stop before eating keepalive reserve")
	}
	if snap.KeepalivePaused {
		t.Fatal("keepalive must keep running in the reserved slice")
	}
}

func TestFiveHourHeaderBlocksChatNotKeepalive(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude",
		Model:    "claude-opus-5",
		Detail:   pluginapi.UsageDetail{InputTokens: 100, OutputTokens: 1},
		ResponseHeaders: http.Header{
			"Anthropic-Ratelimit-Unified-5h-Status":      []string{"allowed"},
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.985"},
		},
	})
	snap := currentBudget(time.Now())
	if !snap.UsedKnown || snap.UsedPercent < 98 {
		t.Fatalf("util=%v known=%v", snap.UsedPercent, snap.UsedKnown)
	}
	if !snap.ChatBlocked {
		t.Fatal("CPA chat should stop at 98% to leave 2% for keepalive")
	}
	if snap.KeepalivePaused {
		t.Fatal("keepalive should still run in the last 2%")
	}
}

func TestDefaultReserveDoesNotTripAtNinetyPercent(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude",
		Model:    "claude-opus-5",
		Detail:   pluginapi.UsageDetail{InputTokens: 100, OutputTokens: 1},
		ResponseHeaders: http.Header{
			"Anthropic-Ratelimit-Unified-5h-Status":      []string{"allowed"},
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.91"},
		},
	})
	snap := currentBudget(time.Now())
	if snap.ChatBlocked {
		t.Fatal("91% should still allow CPA chat when only 2% is reserved")
	}
}

func TestKeepaliveNotPausedJustBecauseItUsedASlice(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	mu.Lock()
	cfg.ReservePercent = 10
	usageEvents = []usageEvent{
		{At: time.Now(), Units: 90000, Keepalive: false},
		{At: time.Now(), Units: 20000, Keepalive: true},
	}
	mu.Unlock()
	snap := currentBudget(time.Now())
	if snap.KeepalivePaused {
		t.Fatal("do not pause keepalive to give the window back to CPA chat")
	}
}

func TestSessionLimitTeachesBudget(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	mu.Lock()
	usageEvents = []usageEvent{{At: time.Now(), Units: 50000, Keepalive: false}}
	mu.Unlock()
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude",
		Model:    "claude-opus-5",
		Failed:   true,
		Failure:  pluginapi.UsageFailure{StatusCode: 429, Body: "You've hit your session limit"},
	})
	mu.Lock()
	got := observedBudget
	until := limitHitUntil
	mu.Unlock()
	if got != 50000 {
		t.Fatalf("observed=%d", got)
	}
	if until.IsZero() {
		t.Fatal("expected backoff")
	}
	if !currentBudget(time.Now()).KeepalivePaused {
		t.Fatal("keepalive should pause after session limit")
	}
}

func TestPickDueWithinBudgetSkipsExpensive(t *testing.T) {
	cheap := session{ID: "a", LastPingUnits: 1000}
	costly := session{ID: "b", LastPingUnits: 50000, PingExpensive: true}
	got := pickDueWithinBudget([]session{costly, cheap}, 4000)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got=%+v", got)
	}
	if pickDueWithinBudget([]session{cheap}, 0) != nil {
		t.Fatal("no remaining budget should skip pings")
	}
}

func TestGuardChatIntercept(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	mu.Lock()
	cfg.FiveHourBudget = 1000
	cfg.ReservePercent = 10
	usageEvents = []usageEvent{{At: time.Now(), Units: 950, Keepalive: false}}
	mu.Unlock()
	body := claudeBody("别把额度用光", "sys")
	raw, _ := json.Marshal(pluginapi.RequestInterceptRequest{
		ToFormat: "claude",
		Model:    "claude-opus-5",
		Body:     body,
	})
	resp, err := handleInterceptBefore(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	var out pluginapi.RequestInterceptResponse
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Terminate || out.StatusCode != 429 {
		t.Fatalf("terminate=%v status=%d", out.Terminate, out.StatusCode)
	}
}

func TestSaveSnapshotSkipsKeepaliveReplay(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	body := claudeBody("保活自己", "sys")
	raw, _ := json.Marshal(pluginapi.RequestInterceptRequest{
		ToFormat: "claude",
		Model:    "claude-opus-5",
		Body:     body,
		Headers:  withKeepaliveHeader(nil),
	})
	saveSnapshot(raw)
	if len(listSessions()) != 0 {
		t.Fatal("keepalive replay must not reset last request time")
	}
}

func TestParseReservePercent(t *testing.T) {
	got := parseConfig([]byte("reserve_percent: 80\nwindow_minutes: 300\n"))
	if got.ReservePercent != 40 {
		t.Fatalf("reserve=%d", got.ReservePercent)
	}
	if got.WindowMinutes != 300 {
		t.Fatalf("window=%d", got.WindowMinutes)
	}
	if !got.guardChat() {
		t.Fatal("guard_chat should default on")
	}
}

func TestRenderStatusHTMLIncludesBudget(t *testing.T) {
	resetSessionsForTest()
	html := renderStatusHTML()
	if !strings.Contains(html, "5 小时已用") {
		t.Fatalf("missing budget panel: %s", html)
	}
}
