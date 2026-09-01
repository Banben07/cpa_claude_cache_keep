package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	keepaliveHeaderKey = "X-Claude-Cache-Keepalive"
	keepaliveHeaderVal = "1"
	defaultWindowMin   = 300
	defaultReservePct  = 10
)

type usageEvent struct {
	At        time.Time
	Units     int64
	Keepalive bool
}

type budgetSnapshot struct {
	WindowMin       int
	ReservePercent  int
	Budget          int64
	ObservedBudget  bool
	ChatUnits       int64
	KeepaliveUnits  int64
	ChatCap         int64
	KeepaliveCap    int64
	KeepaliveRemain int64
	ChatBlocked     bool
	KeepalivePaused bool
	PauseReason     string
	LimitHitUntil   time.Time
	GuardChat       bool
}

var (
	usageEvents     []usageEvent
	observedBudget  int64
	limitHitUntil   time.Time
	keepaliveActive int
	currentPingID   string
)

func isKeepaliveRequest(headers http.Header) bool {
	if headers == nil {
		return false
	}
	return strings.TrimSpace(headers.Get(keepaliveHeaderKey)) != ""
}

func withKeepaliveHeader(headers http.Header) http.Header {
	out := cloneHeader(headers)
	if out == nil {
		out = make(http.Header)
	}
	out.Set(keepaliveHeaderKey, keepaliveHeaderVal)
	return out
}

func weightedUnits(model string, d pluginapi.UsageDetail) int64 {
	cacheRead := d.CacheReadTokens
	if cacheRead == 0 {
		cacheRead = d.CachedTokens
	}
	cacheWrite := d.CacheCreationTokens
	uncached := d.InputTokens - cacheRead - cacheWrite
	if uncached < 0 {
		uncached = 0
	}
	out := d.OutputTokens + d.ReasoningTokens
	if out < 0 {
		out = 0
	}
	// Approximate Anthropic/Claude Code session weighting:
	// uncached 1x, 1h cache write ~2x, cache read 0.1x, output/reasoning ~5x.
	units := uncached + cacheWrite*2 + cacheRead/10 + out*5
	return units * modelWeight(model)
}

func modelWeight(model string) int64 {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return 5
	case strings.Contains(m, "haiku"):
		return 1
	default:
		return 1
	}
}

func estimatePingUnits(item session) int64 {
	if item.LastPingUnits > 0 {
		return item.LastPingUnits
	}
	if item.LastChatUnits > 0 {
		est := item.LastChatUnits / 8
		if est < 1000 {
			est = 1000
		}
		return est
	}
	est := int64(len(item.Body) / 40)
	if est < 1000 {
		est = 1000
	}
	return est
}

func recordUsage(rec pluginapi.UsageRecord) {
	if !isClaudeUpstream(rec.Provider, rec.Model) && !isClaudeUpstream("", rec.Model) {
		return
	}
	if rec.Failed {
		noteUsageFailure(rec)
	}
	units := weightedUnits(rec.Model, rec.Detail)
	if units <= 0 && !rec.Failed {
		return
	}
	now := rec.RequestedAt
	if now.IsZero() {
		now = time.Now()
	}
	mu.Lock()
	defer mu.Unlock()
	keep := keepaliveActive > 0
	if units > 0 {
		usageEvents = append(usageEvents, usageEvent{At: now, Units: units, Keepalive: keep})
		trimUsageLocked(now)
	}
	if rec.Failed {
		return
	}
	if keep {
		if currentPingID != "" {
			if item := sessions[currentPingID]; item != nil {
				item.LastPingUnits = units
				if rec.Detail.CacheCreationTokens > 20000 && rec.Detail.CacheCreationTokens > rec.Detail.CacheReadTokens {
					item.PingExpensive = true
				}
			}
		}
		return
	}
	limitHitUntil = time.Time{}
	for _, item := range sessions {
		if item == nil || !strings.EqualFold(item.Model, rec.Model) {
			continue
		}
		if now.Sub(item.LastSeen) <= 2*time.Minute {
			item.LastChatUnits = units
		}
	}
}

func noteUsageFailure(rec pluginapi.UsageRecord) {
	body := strings.ToLower(rec.Failure.Body)
	status := rec.Failure.StatusCode
	sessionHit := status == 429 || strings.Contains(body, "session limit") || strings.Contains(body, "rate_limit")
	weeklyHit := strings.Contains(body, "weekly limit")
	if !sessionHit && !weeklyHit {
		return
	}
	now := time.Now()
	mu.Lock()
	defer mu.Unlock()
	trimUsageLocked(now)
	chat, keep := sumUsageLocked()
	total := chat + keep
	if sessionHit && !weeklyHit && total > 0 && (observedBudget == 0 || total > observedBudget) {
		observedBudget = total
	}
	backoff := 20 * time.Minute
	if weeklyHit {
		backoff = 2 * time.Hour
	}
	until := now.Add(backoff)
	if limitHitUntil.Before(until) {
		limitHitUntil = until
	}
}

func trimUsageLocked(now time.Time) {
	window := windowDurationLocked()
	cut := now.Add(-window)
	i := 0
	for i < len(usageEvents) && usageEvents[i].At.Before(cut) {
		i++
	}
	if i > 0 {
		usageEvents = append([]usageEvent(nil), usageEvents[i:]...)
	}
}

func windowDurationLocked() time.Duration {
	min := cfg.WindowMinutes
	if min <= 0 {
		min = defaultWindowMin
	}
	return time.Duration(min) * time.Minute
}

func sumUsageLocked() (chat, keepalive int64) {
	for _, ev := range usageEvents {
		if ev.Keepalive {
			keepalive += ev.Units
			continue
		}
		chat += ev.Units
	}
	return chat, keepalive
}

func currentBudget(now time.Time) budgetSnapshot {
	mu.Lock()
	defer mu.Unlock()
	return currentBudgetLocked(now)
}

func currentBudgetLocked(now time.Time) budgetSnapshot {
	trimUsageLocked(now)
	chat, keep := sumUsageLocked()
	reserve := cfg.ReservePercent
	if reserve <= 0 {
		reserve = defaultReservePct
	}
	windowMin := cfg.WindowMinutes
	if windowMin <= 0 {
		windowMin = defaultWindowMin
	}
	budget := cfg.FiveHourBudget
	observed := false
	if budget <= 0 && observedBudget > 0 {
		budget = observedBudget
		observed = true
	}
	snap := budgetSnapshot{
		WindowMin:      windowMin,
		ReservePercent: reserve,
		Budget:         budget,
		ObservedBudget: observed,
		ChatUnits:      chat,
		KeepaliveUnits: keep,
		GuardChat:      cfg.guardChat(),
		LimitHitUntil:  limitHitUntil,
	}
	if !limitHitUntil.IsZero() && now.Before(limitHitUntil) {
		snap.KeepalivePaused = true
		snap.PauseReason = "刚撞到用量上限，先停保活，避免把窗口堵死"
	}
	interval := time.Duration(cfg.IntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = 50 * time.Minute
	}
	floor := keepaliveFloorLocked(now, interval, windowMin)
	if budget > 0 {
		snap.KeepaliveCap = budget * int64(reserve) / 100
		if snap.KeepaliveCap < floor {
			snap.KeepaliveCap = floor
		}
		if snap.KeepaliveCap > budget/2 {
			snap.KeepaliveCap = budget / 2
		}
		snap.ChatCap = budget - snap.KeepaliveCap
		if snap.ChatCap < 0 {
			snap.ChatCap = 0
		}
		snap.ChatBlocked = snap.GuardChat && chat >= snap.ChatCap
	} else {
		fromChat := int64(0)
		if reserve < 100 {
			fromChat = chat * int64(reserve) / int64(100-reserve)
		}
		snap.KeepaliveCap = fromChat
		if snap.KeepaliveCap < floor {
			snap.KeepaliveCap = floor
		}
	}
	snap.KeepaliveRemain = snap.KeepaliveCap - keep
	if snap.KeepaliveRemain < 0 {
		snap.KeepaliveRemain = 0
	}
	if !snap.KeepalivePaused && snap.KeepaliveRemain == 0 && keep >= snap.KeepaliveCap && snap.KeepaliveCap > 0 {
		snap.KeepalivePaused = true
		snap.PauseReason = "保活已用完预留额度，先让对话把 5 小时窗口用在正事上"
	}
	return snap
}

func keepaliveFloorLocked(now time.Time, interval time.Duration, windowMin int) int64 {
	if interval <= 0 {
		return 0
	}
	pings := int64(time.Duration(windowMin)*time.Minute/interval + 1)
	if pings < 1 {
		pings = 1
	}
	var floor int64
	for _, item := range sessions {
		if item == nil || !item.Enabled || item.PingExpensive {
			continue
		}
		floor += estimatePingUnits(*item) * pings
	}
	return floor
}

func pickDueWithinBudget(due []session, remain int64) []session {
	if remain <= 0 || len(due) == 0 {
		return nil
	}
	out := make([]session, 0, len(due))
	left := remain
	for _, item := range due {
		if item.PingExpensive {
			continue
		}
		est := estimatePingUnits(item)
		if est > left {
			continue
		}
		out = append(out, item)
		left -= est
	}
	return out
}

func chatGuardResponse() pluginapi.RequestInterceptResponse {
	msg := "已为缓存保活预留下 5 小时额度，当前对话用量已到上限。保活仍会继续；窗口回落后再发消息。"
	body, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "rate_limit_error",
			"message": msg,
		},
	})
	return pluginapi.RequestInterceptResponse{
		Terminate:  true,
		StatusCode: http.StatusTooManyRequests,
		ResponseHeaders: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		},
		ResponseBody: body,
	}
}
