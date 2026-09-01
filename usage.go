package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	keepaliveHeaderKey = "X-Claude-Cache-Keepalive"
	keepaliveHeaderVal = "1"
	defaultWindowMin   = 300
	defaultReservePct  = 2
	maxQuotaSamples    = 5
)

type usageEvent struct {
	At        time.Time
	Units     int64
	Keepalive bool
}

type quotaSample struct {
	At            time.Time
	Keepalive     bool
	Failed        bool
	Model         string
	HeaderOK      bool
	Util5h        float64
	Status5h      string
	Reset5h       time.Time
	RawUtil5h     string
	RawStatus5h   string
	RawReset5h    string
	Header7dOK    bool
	Util7d        float64
	Status7d      string
	RawUtil7d     string
	RawStatus7d   string
	UnifiedStatus string
	Units         int64
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
	UsedPercent     float64
	UsedKnown       bool
	BlockAtPercent  int
}

var (
	usageEvents     []usageEvent
	observedBudget  int64
	limitHitUntil   time.Time
	keepaliveActive int
	currentPingID   string
	fiveHourUtil    float64
	fiveHourUtilOK  bool
	fiveHourStatus  string
	lastCalibUtil   float64
	lastCalibOK     bool
	unitsPerUtil    float64 // weighted units that move utilization by 1.0 (100%)
	quotaSamples    []quotaSample
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

func headerGetCI(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if v := strings.TrimSpace(headers.Get(name)); v != "" {
		return v
	}
	for k, vals := range headers {
		if strings.EqualFold(k, name) && len(vals) > 0 {
			return strings.TrimSpace(vals[0])
		}
	}
	return ""
}

func parseUtilFraction(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	if v > 1.5 {
		v = v / 100
	}
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return v, true
}

func parseUnixTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if sec, err := strconv.ParseFloat(raw, 64); err == nil && sec > 1e8 {
		return time.Unix(int64(sec), 0).UTC()
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func parseQuotaSample(rec pluginapi.UsageRecord, now time.Time, keepalive bool) quotaSample {
	h := rec.ResponseHeaders
	rawUtil5h := headerGetCI(h, "Anthropic-Ratelimit-Unified-5h-Utilization")
	rawStatus5h := headerGetCI(h, "Anthropic-Ratelimit-Unified-5h-Status")
	rawReset5h := headerGetCI(h, "Anthropic-Ratelimit-Unified-5h-Reset")
	rawUtil7d := headerGetCI(h, "Anthropic-Ratelimit-Unified-7d-Utilization")
	rawStatus7d := headerGetCI(h, "Anthropic-Ratelimit-Unified-7d-Status")
	sample := quotaSample{
		At:            now,
		Keepalive:     keepalive,
		Failed:        rec.Failed,
		Model:         rec.Model,
		RawUtil5h:     rawUtil5h,
		RawStatus5h:   strings.ToLower(rawStatus5h),
		RawReset5h:    rawReset5h,
		RawUtil7d:     rawUtil7d,
		RawStatus7d:   strings.ToLower(rawStatus7d),
		UnifiedStatus: strings.ToLower(headerGetCI(h, "Anthropic-Ratelimit-Unified-Status")),
		Reset5h:       parseUnixTime(rawReset5h),
	}
	if rec.Failed {
		sample.Units = 0
	} else {
		sample.Units = weightedUnits(rec.Model, rec.Detail)
	}
	if v, ok := parseUtilFraction(rawUtil5h); ok {
		sample.Util5h = v
		sample.HeaderOK = true
	}
	if sample.RawStatus5h != "" {
		sample.Status5h = sample.RawStatus5h
		sample.HeaderOK = true
		if rawUtil5h == "" && sample.Status5h == "rejected" {
			sample.Util5h = 1
		}
	}
	if v, ok := parseUtilFraction(rawUtil7d); ok {
		sample.Util7d = v
		sample.Header7dOK = true
	}
	if sample.RawStatus7d != "" {
		sample.Status7d = sample.RawStatus7d
		sample.Header7dOK = true
	}
	return sample
}

func parseFiveHourUtilization(headers http.Header) (util float64, status string, ok bool) {
	sample := parseQuotaSample(pluginapi.UsageRecord{ResponseHeaders: headers}, time.Time{}, false)
	return sample.Util5h, sample.Status5h, sample.HeaderOK
}

func appendQuotaSampleLocked(sample quotaSample) {
	if !sample.HeaderOK && !sample.Failed && sample.Units <= 0 {
		return
	}
	quotaSamples = append(quotaSamples, sample)
	if extra := len(quotaSamples) - maxQuotaSamples; extra > 0 {
		quotaSamples = append([]quotaSample(nil), quotaSamples[extra:]...)
	}
}

func listQuotaSamples() []quotaSample {
	mu.Lock()
	defer mu.Unlock()
	out := make([]quotaSample, len(quotaSamples))
	for i := range quotaSamples {
		out[len(quotaSamples)-1-i] = quotaSamples[i]
	}
	return out
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
	now := rec.RequestedAt
	if now.IsZero() {
		now = time.Now()
	}
	units := weightedUnits(rec.Model, rec.Detail)
	sample := parseQuotaSample(rec, now, false)
	if !rec.Failed {
		sample.Units = units
	}
	mu.Lock()
	defer mu.Unlock()
	keep := keepaliveActive > 0
	sample.Keepalive = keep
	appendQuotaSampleLocked(sample)
	if sample.HeaderOK {
		if fiveHourUtilOK && sample.Util5h+0.05 < fiveHourUtil {
			// Window rolled over; previous units-per-percent no longer applies.
			unitsPerUtil = 0
			lastCalibOK = false
			lastCalibUtil = 0
		}
		if units > 0 && !rec.Failed && !keep && lastCalibOK && sample.Util5h > lastCalibUtil {
			delta := sample.Util5h - lastCalibUtil
			if delta >= 0.005 {
				calib := float64(units) / delta
				if unitsPerUtil <= 0 {
					unitsPerUtil = calib
				} else {
					unitsPerUtil = unitsPerUtil*0.65 + calib*0.35
				}
			}
		}
		fiveHourUtil = sample.Util5h
		fiveHourStatus = sample.Status5h
		fiveHourUtilOK = true
		lastCalibUtil = sample.Util5h
		lastCalibOK = true
		if sample.Status5h == "rejected" || sample.Util5h >= 0.995 {
			until := now.Add(20 * time.Minute)
			if limitHitUntil.Before(until) {
				limitHitUntil = until
			}
		}
	}
	if units <= 0 && !rec.Failed {
		return
	}
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

func estimateBodyUnits(model string, body []byte) int64 {
	tokens := int64(len(body) / 4)
	if tokens < 1 {
		tokens = 1
	}
	return weightedUnits(model, pluginapi.UsageDetail{InputTokens: tokens, OutputTokens: 1})
}

func estimateNextChatUnitsLocked(model string, body []byte) int64 {
	if item := sessions[sessionKey(model, body)]; item != nil && item.LastChatUnits > 0 {
		return item.LastChatUnits
	}
	return estimateBodyUnits(model, body)
}

// shouldBlockChat is local: it never contacts Anthropic. CPA chat is stopped
// when the last known 5h utilization already hit the reserve line, or when
// this request is predicted to land past that line (so a fat prompt at 97%
// does not punch through 100% and start a streak of upstream 429s).
func shouldBlockChat(now time.Time, model string, body []byte) bool {
	mu.Lock()
	defer mu.Unlock()
	snap := currentBudgetLocked(now)
	if snap.ChatBlocked {
		return true
	}
	if !snap.GuardChat || !fiveHourUtilOK || unitsPerUtil <= 0 {
		return false
	}
	est := estimateNextChatUnitsLocked(model, body)
	if est <= 0 {
		return false
	}
	blockAt := float64(snap.BlockAtPercent) / 100
	predicted := fiveHourUtil + float64(est)/unitsPerUtil
	return predicted >= blockAt
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
		BlockAtPercent: 100 - reserve,
	}
	blockAt := float64(snap.BlockAtPercent) / 100
	if fiveHourUtilOK {
		snap.UsedKnown = true
		snap.UsedPercent = fiveHourUtil * 100
		if snap.GuardChat && fiveHourUtil >= blockAt {
			snap.ChatBlocked = true
		}
		if fiveHourStatus == "rejected" || fiveHourUtil >= 0.995 {
			snap.KeepalivePaused = true
			snap.PauseReason = "5 小时额度已经用尽，保活也先停，等窗口回落"
		}
	}
	if !limitHitUntil.IsZero() && now.Before(limitHitUntil) && (fiveHourStatus == "rejected" || !fiveHourUtilOK) {
		snap.KeepalivePaused = true
		if snap.PauseReason == "" {
			snap.PauseReason = "刚撞到用量上限，保活先停，等窗口回落"
		}
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
		if !snap.UsedKnown && snap.GuardChat && chat >= snap.ChatCap {
			snap.ChatBlocked = true
		}
	} else {
		snap.KeepaliveCap = floor
	}
	snap.KeepaliveRemain = snap.KeepaliveCap - keep
	if snap.KeepaliveRemain < 0 {
		snap.KeepaliveRemain = 0
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
	msg := "已为缓存保活预留下 5 小时额度，CPA 对话先停。保活仍会继续刷新 prompt cache；窗口回落后再发消息。"
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
