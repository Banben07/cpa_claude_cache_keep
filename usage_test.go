package main

import (
	"encoding/json"
	"fmt"
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
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.96"},
		},
	})
	snap := currentBudget(time.Now())
	if !snap.UsedKnown || snap.UsedPercent < 95 {
		t.Fatalf("util=%v known=%v", snap.UsedPercent, snap.UsedKnown)
	}
	if !snap.ChatBlocked {
		t.Fatal("CPA chat should stop at 95% so a long turn cannot punch through 100%")
	}
	if snap.KeepalivePaused {
		t.Fatal("keepalive should still run in the last 5%")
	}
	if snap.BlockAtPercent != 95 {
		t.Fatalf("block at %d", snap.BlockAtPercent)
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
		t.Fatal("91% should still allow CPA chat when stopping at 95%")
	}
}

func TestDefaultReserveBlocksNinetyEightPercent(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude",
		Model:    "claude-opus-5",
		Detail:   pluginapi.UsageDetail{InputTokens: 100, OutputTokens: 1},
		ResponseHeaders: http.Header{
			"Anthropic-Ratelimit-Unified-5h-Status":      []string{"allowed"},
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.98"},
		},
	})
	snap := currentBudget(time.Now())
	if !snap.ChatBlocked {
		t.Fatal("98% is past the 95% stop line")
	}
	if snap.KeepalivePaused {
		t.Fatal("keepalive should still run between 95% and ~100%")
	}
}

func TestExplicitReserveStillBlocksChatEarly(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	mu.Lock()
	cfg.ReservePercent = 2
	mu.Unlock()
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
	if !snap.ChatBlocked {
		t.Fatal("reserve_percent 2 should still stop CPA chat at 98%")
	}
	if snap.KeepalivePaused {
		t.Fatal("keepalive should keep running in an explicit reserved slice")
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
	zero := parseConfig(nil)
	if zero.ReservePercent != 5 {
		t.Fatalf("default reserve=%d", zero.ReservePercent)
	}
	explicitZero := parseConfig([]byte("reserve_percent: 0\n"))
	if explicitZero.ReservePercent != 5 {
		t.Fatalf("explicit 0 became %d", explicitZero.ReservePercent)
	}
}

func TestRenderStatusHTMLIncludesBudget(t *testing.T) {
	resetSessionsForTest()
	html := renderStatusHTML()
	if !strings.Contains(html, "5 小时已用") {
		t.Fatalf("missing budget panel: %s", html)
	}
	if !strings.Contains(html, "额度记录") {
		t.Fatal("missing quota log section")
	}
	if !strings.Contains(html, pluginVersion) {
		t.Fatal("missing plugin version")
	}
	if !strings.Contains(html, "CPA 对话停在 95%") {
		t.Fatalf("default should stop chat at 95%%: %s", html)
	}
	if !strings.Contains(html, `data-stop`) || !strings.Contains(html, `name="stop"`) {
		t.Fatal("status page should let you change the stop percent")
	}
	if strings.Contains(html, "对话用到 100% 才停") {
		t.Fatal("98/100 is too thin; default must stop earlier")
	}
}

func TestQuotaLogKeepsFiveNewest(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	hdr := func(u string) http.Header {
		return http.Header{
			"Anthropic-Ratelimit-Unified-5h-Status":      []string{"allowed"},
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{u},
		}
	}
	for i := 1; i <= 6; i++ {
		recordUsage(pluginapi.UsageRecord{
			Provider:        "claude",
			Model:           "claude-sonnet-5",
			Detail:          pluginapi.UsageDetail{InputTokens: 100, OutputTokens: 1},
			ResponseHeaders: hdr(fmt.Sprintf("0.%d0", i)),
		})
	}
	got := listQuotaSamples()
	if len(got) != 5 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].RawUtil5h != "0.60" {
		t.Fatalf("newest=%s", got[0].RawUtil5h)
	}
	if got[4].RawUtil5h != "0.20" {
		t.Fatalf("oldest kept=%s", got[4].RawUtil5h)
	}
}

func TestQuotaSectionRendersBelowSessions(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	upsertSession("claude-opus-5", "claude", "claude", nil, claudeBody("主对话", "sys"))
	html := renderStatusHTML()
	sess := strings.Index(html, `class="list"`)
	quota := strings.Index(html, "额度记录")
	if sess < 0 || quota < 0 || quota < sess {
		t.Fatalf("quota should sit below sessions: sess=%d quota=%d", sess, quota)
	}
}

func TestQuotaLogShowsRawFiveHourHeaders(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude",
		Model:    "claude-opus-5",
		Detail:   pluginapi.UsageDetail{InputTokens: 100, OutputTokens: 1},
		ResponseHeaders: http.Header{
			"Anthropic-Ratelimit-Unified-5h-Status":      []string{"allowed"},
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.37"},
			"Anthropic-Ratelimit-Unified-7d-Utilization": []string{"0.12"},
			"Anthropic-Ratelimit-Unified-Status":         []string{"allowed"},
		},
	})
	html := renderStatusHTML()
	if !strings.Contains(html, "5h-utilization=0.37") {
		t.Fatalf("missing raw 5h utilization: %s", html)
	}
	if !strings.Contains(html, "37.0%") && !strings.Contains(html, "37%") {
		t.Fatalf("missing parsed 5h percent: %s", html)
	}
	got := listQuotaSamples()
	if len(got) != 1 || !got[0].HeaderOK || got[0].Util5h < 0.36 {
		t.Fatalf("samples=%+v", got)
	}
}

func TestQuotaLogKeepsChatWithoutHeaders(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude",
		Model:    "claude-sonnet-5",
		Detail:   pluginapi.UsageDetail{InputTokens: 200, OutputTokens: 8},
	})
	got := listQuotaSamples()
	if len(got) != 1 || got[0].HeaderOK {
		t.Fatalf("expected one missing-header sample, got=%+v", got)
	}
	html := renderStatusHTML()
	if !strings.Contains(html, "未读到") {
		t.Fatalf("missing missing-header label: %s", html)
	}
}

func TestQuotaLogSkipsEmptyCountTokens(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude",
		Model:    "claude-sonnet-5",
	})
	if len(listQuotaSamples()) != 0 {
		t.Fatal("zero-unit requests without headers should not flood the quota log")
	}
}

func TestQuotaLogParsesLowercaseHeaders(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude",
		Model:    "claude-opus-5",
		Detail:   pluginapi.UsageDetail{InputTokens: 10, OutputTokens: 1},
		ResponseHeaders: http.Header{
			"anthropic-ratelimit-unified-5h-utilization": []string{"0.2"},
			"anthropic-ratelimit-unified-5h-status":      []string{"allowed_warning"},
		},
	})
	got := listQuotaSamples()
	if len(got) != 1 || !got[0].HeaderOK || got[0].Status5h != "allowed_warning" {
		t.Fatalf("samples=%+v", got)
	}
}

func TestRecordUsageReadsHeaderWhenUnitsZero(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude",
		Model:    "claude-sonnet-5",
		Detail:   pluginapi.UsageDetail{},
		ResponseHeaders: http.Header{
			"Anthropic-Ratelimit-Unified-5h-Status":      []string{"allowed"},
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.42"},
		},
	})
	snap := currentBudget(time.Now())
	if !snap.UsedKnown || snap.UsedPercent < 41 || snap.UsedPercent > 43 {
		t.Fatalf("util=%v known=%v", snap.UsedPercent, snap.UsedKnown)
	}
}

func TestCalibratesUnitsPerUtilFromChat(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	hdr := func(u string) http.Header {
		return http.Header{
			"Anthropic-Ratelimit-Unified-5h-Status":      []string{"allowed"},
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{u},
		}
	}
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude", Model: "claude-sonnet-5",
		Detail:          pluginapi.UsageDetail{InputTokens: 10000, OutputTokens: 1},
		ResponseHeaders: hdr("0.80"),
	})
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude", Model: "claude-sonnet-5",
		Detail:          pluginapi.UsageDetail{InputTokens: 10000, OutputTokens: 1},
		ResponseHeaders: hdr("0.90"),
	})
	mu.Lock()
	got := unitsPerUtil
	mu.Unlock()
	// 10000 uncached + 5 output = 10005 units over 0.10 utilization.
	if got < 90000 || got > 110000 {
		t.Fatalf("unitsPerUtil=%v", got)
	}
}

func TestOvershootPredictionBlocksLargeChat(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	body := claudeBody("别把额度用光", "sys")
	mu.Lock()
	fiveHourUtil = 0.90
	fiveHourUtilOK = true
	unitsPerUtil = 100000
	id := sessionKey("claude-opus-5", body)
	sessions[id] = &session{ID: id, Model: "claude-opus-5", Body: body, LastChatUnits: 15000}
	mu.Unlock()
	if !terminatedChat(t, body) {
		t.Fatal("90% + 15% predicted should block before punching through 100%")
	}
}

func TestOvershootAllowsSmallFollowUp(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	body := claudeBody("短回复", "sys")
	mu.Lock()
	fiveHourUtil = 0.90
	fiveHourUtilOK = true
	unitsPerUtil = 100000
	id := sessionKey("claude-opus-5", body)
	sessions[id] = &session{ID: id, Model: "claude-opus-5", Body: body, LastChatUnits: 1000}
	mu.Unlock()
	if terminatedChat(t, body) {
		t.Fatal("90% + 1% should still go upstream")
	}
}

func TestWindowResetClearsCalibration(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	hdr := func(u string) http.Header {
		return http.Header{
			"Anthropic-Ratelimit-Unified-5h-Status":      []string{"allowed"},
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{u},
		}
	}
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude", Model: "claude-sonnet-5",
		Detail:          pluginapi.UsageDetail{InputTokens: 10000, OutputTokens: 1},
		ResponseHeaders: hdr("0.90"),
	})
	recordUsage(pluginapi.UsageRecord{
		Provider: "claude", Model: "claude-sonnet-5",
		Detail:          pluginapi.UsageDetail{InputTokens: 10000, OutputTokens: 1},
		ResponseHeaders: hdr("0.10"),
	})
	mu.Lock()
	got := unitsPerUtil
	ok := lastCalibOK
	mu.Unlock()
	if got != 0 {
		t.Fatalf("calibration should reset after window drop, unitsPerUtil=%v", got)
	}
	if !ok {
		t.Fatal("new window should start calibrating from the reset reading")
	}
}

func terminatedChat(t *testing.T, body []byte) bool {
	t.Helper()
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
	return out.Terminate && out.StatusCode == 429
}
