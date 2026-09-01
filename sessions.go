package main

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

type session struct {
	ID            string
	Label         string
	CustomLabel   bool
	Enabled       bool
	Model         string
	SourceFormat  string
	ToFormat      string
	Headers       http.Header
	Body          []byte
	SavedAt       time.Time
	LastSeen      time.Time
	LastPingAt    time.Time
	LastPingError string
	LastChatUnits int64
	LastPingUnits int64
	PingExpensive bool
	Info          bodyInfo
}

func upsertSession(model, sourceFormat, toFormat string, headers http.Header, body []byte) {
	if !isKeepaliveCandidate(body) {
		return
	}
	kind := subagentKind(headers, body)
	id := sessionKey(model, body)
	now := time.Now()
	info := inspectBody(body)
	label := sessionLabel(body)
	mu.Lock()
	defer mu.Unlock()
	if kind != "" {
		subagentSkipped++
		lastSubagentAt = now
		lastSubagentKind = kind
		lastSubagentLabel = label
		return
	}
	if existing, ok := sessions[id]; ok {
		if !existing.CustomLabel {
			existing.Label = label
		}
		existing.Model = model
		existing.SourceFormat = sourceFormat
		existing.ToFormat = toFormat
		existing.Headers = cloneHeader(headers)
		existing.Body = append([]byte(nil), body...)
		existing.SavedAt = now
		existing.LastSeen = now
		existing.Info = info
		existing.PingExpensive = false
		return
	}
	if sessions == nil {
		sessions = map[string]*session{}
	}
	evictSessionsLocked(now, cfg.MaxSessions-1)
	sessions[id] = &session{
		ID:           id,
		Label:        label,
		Enabled:      true,
		Model:        model,
		SourceFormat: sourceFormat,
		ToFormat:     toFormat,
		Headers:      cloneHeader(headers),
		Body:         append([]byte(nil), body...),
		SavedAt:      now,
		LastSeen:     now,
		Info:         info,
	}
}

func setSessionEnabled(id string, enabled bool) bool {
	mu.Lock()
	defer mu.Unlock()
	item, ok := sessions[id]
	if !ok {
		return false
	}
	item.Enabled = enabled
	if enabled {
		item.LastPingError = ""
	}
	return true
}

func forgetSession(id string) bool {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := sessions[id]; !ok {
		return false
	}
	delete(sessions, id)
	return true
}

func renameSession(id, name string) bool {
	name = sanitizeSessionName(name)
	mu.Lock()
	defer mu.Unlock()
	item, ok := sessions[id]
	if !ok {
		return false
	}
	if name == "" {
		item.CustomLabel = false
		item.Label = sessionLabel(item.Body)
		return true
	}
	item.CustomLabel = true
	item.Label = name
	return true
}

func sanitizeSessionName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if r < 32 || r == 127 {
			continue
		}
		b.WriteRune(r)
	}
	return truncateRunes(b.String(), 42)
}

func dueSnapshots(now time.Time, interval time.Duration) []session {
	mu.Lock()
	defer mu.Unlock()
	out := make([]session, 0, len(sessions))
	for _, item := range sessions {
		if item == nil || !item.Enabled || len(item.Body) == 0 {
			continue
		}
		if item.PingExpensive {
			continue
		}
		if !sessionDue(item.LastSeen, item.LastPingAt, now, interval) {
			continue
		}
		out = append(out, cloneSession(item))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out
}

func listSessions() []session {
	mu.Lock()
	defer mu.Unlock()
	out := make([]session, 0, len(sessions))
	for _, item := range sessions {
		if item == nil {
			continue
		}
		out = append(out, cloneSession(item))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out
}

func recordSessionPing(id string, pingedAt time.Time, pingErr error) {
	mu.Lock()
	defer mu.Unlock()
	item, ok := sessions[id]
	if !ok {
		return
	}
	if pingErr != nil {
		item.LastPingError = pingErr.Error()
		return
	}
	item.LastPingAt = pingedAt
	item.LastPingError = ""
}

func evictSessionsLocked(now time.Time, keep int) {
	if keep < 0 {
		keep = 0
	}
	idle := time.Duration(cfg.IdleEvictMinutes) * time.Minute
	if idle > 0 {
		for id, item := range sessions {
			if item == nil {
				delete(sessions, id)
				continue
			}
			if item.Enabled {
				continue
			}
			if now.Sub(item.LastSeen) >= idle {
				delete(sessions, id)
			}
		}
	}
	for len(sessions) > keep {
		dropID := ""
		var dropSeen time.Time
		dropEnabled := true
		for id, item := range sessions {
			if item == nil {
				dropID = id
				break
			}
			if dropID == "" {
				dropID = id
				dropSeen = item.LastSeen
				dropEnabled = item.Enabled
				continue
			}
			if item.Enabled != dropEnabled {
				if !item.Enabled && dropEnabled {
					dropID = id
					dropSeen = item.LastSeen
					dropEnabled = false
				}
				continue
			}
			if item.LastSeen.Before(dropSeen) {
				dropID = id
				dropSeen = item.LastSeen
				dropEnabled = item.Enabled
			}
		}
		if dropID == "" {
			break
		}
		delete(sessions, dropID)
	}
}

func cloneSession(item *session) session {
	copied := *item
	copied.Body = append([]byte(nil), item.Body...)
	copied.Headers = cloneHeader(item.Headers)
	return copied
}

func resetSessionsForTest() {
	mu.Lock()
	sessions = map[string]*session{}
	lastPingAt = time.Time{}
	lastErr = ""
	loopStartedAt = time.Time{}
	pinging = false
	usageEvents = nil
	observedBudget = 0
	limitHitUntil = time.Time{}
	keepaliveActive = 0
	currentPingID = ""
	fiveHourUtil = 0
	fiveHourUtilOK = false
	fiveHourStatus = ""
	lastCalibUtil = 0
	lastCalibOK = false
	unitsPerUtil = 0
	quotaSamples = nil
	subagentSkipped = 0
	lastSubagentAt = time.Time{}
	lastSubagentKind = ""
	lastSubagentLabel = ""
	cfg = defaultConfig()
	settingsPath = ""
	persistSettings = false
	mu.Unlock()
}
