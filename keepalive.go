package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"gopkg.in/yaml.v3"
)

type bodyInfo struct {
	HasMaxTokens      bool
	Stream            bool
	MessageCount      int
	CacheControlCount int
	CacheTTL          string
	FirstUser         string
}

type pluginConfig struct {
	IntervalMinutes  int    `yaml:"interval_minutes"`
	MaxTokens        int    `yaml:"max_tokens"`
	MaxSessions      int    `yaml:"max_sessions"`
	IdleEvictMinutes int    `yaml:"idle_evict_minutes"`
	WindowMinutes    int    `yaml:"window_minutes"`
	ReservePercent   int    `yaml:"reserve_percent"`
	StopPercent      int    `yaml:"stop_percent"`
	FiveHourBudget   int64  `yaml:"five_hour_budget"`
	GuardChat        *bool  `yaml:"guard_chat"`
	MessagesURL      string `yaml:"messages_url"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{IntervalMinutes: 50, MaxTokens: 1, MaxSessions: 8, IdleEvictMinutes: 180, WindowMinutes: defaultWindowMin, ReservePercent: defaultReservePct, MessagesURL: defaultMessagesURL}
}

func parseConfig(raw []byte) pluginConfig {
	cfg := defaultConfig()
	if len(raw) == 0 {
		return cfg
	}
	_ = yaml.Unmarshal(raw, &cfg)
	if cfg.IntervalMinutes <= 0 {
		cfg.IntervalMinutes = 50
	}
	if cfg.MaxTokens < 0 {
		cfg.MaxTokens = 1
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 8
	}
	if cfg.MaxSessions > 32 {
		cfg.MaxSessions = 32
	}
	if cfg.IdleEvictMinutes < 0 {
		cfg.IdleEvictMinutes = 180
	}
	if cfg.WindowMinutes <= 0 {
		cfg.WindowMinutes = defaultWindowMin
	}
	if cfg.WindowMinutes > 24*60 {
		cfg.WindowMinutes = 24 * 60
	}
	if cfg.StopPercent > 0 {
		cfg.ReservePercent = 100 - clampStopPercent(cfg.StopPercent)
	}
	if cfg.ReservePercent <= 0 {
		cfg.ReservePercent = defaultReservePct
	}
	if cfg.ReservePercent > 40 {
		cfg.ReservePercent = 40
	}
	if cfg.FiveHourBudget < 0 {
		cfg.FiveHourBudget = 0
	}
	cfg.MessagesURL = normalizeMessagesURL(cfg.MessagesURL)
	return cfg
}

func (c pluginConfig) guardChat() bool {
	return c.GuardChat == nil || *c.GuardChat
}

func isClaudeUpstream(toFormat, model string) bool {
	f := strings.ToLower(toFormat)
	m := strings.ToLower(model)
	skip := []string{"codex", "gemini", "openai", "grok", "gpt-", "chatgpt"}
	for _, token := range skip {
		if strings.Contains(f, token) || strings.Contains(m, token) {
			return false
		}
	}
	if strings.Contains(f, "claude") || strings.Contains(f, "anthropic") {
		return true
	}
	for _, token := range []string{"claude", "opus", "sonnet", "haiku", "fable"} {
		if strings.Contains(m, token) {
			return true
		}
	}
	return f == "" && m == ""
}

func limitOutput(body []byte, maxTokens int) ([]byte, error) {
	out, err := sjson.SetBytes(body, "max_tokens", maxTokens)
	if err != nil {
		return nil, err
	}
	out, err = sjson.SetBytes(out, "stream", false)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func inspectBody(body []byte) bodyInfo {
	info := bodyInfo{
		HasMaxTokens:      gjson.GetBytes(body, "max_tokens").Exists(),
		Stream:            gjson.GetBytes(body, "stream").Bool(),
		MessageCount:      int(gjson.GetBytes(body, "messages.#").Int()),
		CacheControlCount: strings.Count(string(body), `"cache_control"`),
		FirstUser:         firstUserText(body),
	}
	switch {
	case strings.Contains(string(body), `"ttl":"1h"`) || strings.Contains(string(body), `"ttl": "1h"`):
		info.CacheTTL = "1h"
	case strings.Contains(string(body), `"ttl":"5m"`) || strings.Contains(string(body), `"ttl": "5m"`):
		info.CacheTTL = "5m"
	}
	return info
}

func isKeepaliveCandidate(body []byte) bool {
	return inspectBody(body).HasMaxTokens
}

// subagentKind reports whether this Claude request is a Claude Code Task/Agent
// child. Confirmed against CPA request dumps on the live gateway:
// parent-agent-id, x-app=cli-bg, and task_budget were absent; subagent turns
// always had X-Claude-Code-Agent-Id and system text `cc_is_subagent=true`.
// Main turns shared the same X-Claude-Code-Session-Id and x-app=cli.
func subagentKind(headers http.Header, body []byte) string {
	if headerGetCI(headers, "X-Claude-Code-Parent-Agent-Id") != "" {
		return "subagent"
	}
	if strings.EqualFold(headerGetCI(headers, "X-App"), "cli-bg") {
		return "background"
	}
	if headerGetCI(headers, "X-Claude-Code-Agent-Id") != "" {
		return "subagent"
	}
	if gjson.GetBytes(body, "task_budget").Exists() {
		return "subagent"
	}
	if strings.Contains(strings.ToLower(systemText(body)), "cc_is_subagent=true") {
		return "subagent"
	}
	return ""
}

func sessionKey(model string, headers http.Header, body []byte) string {
	if sid := claudeSessionID(headers, body); sid != "" {
		return sidSessionKey(sid)
	}
	return contentSessionKey(model, body)
}

func claudeSessionID(headers http.Header, body []byte) string {
	if sid := strings.TrimSpace(headerGetCI(headers, "X-Claude-Code-Session-Id")); sid != "" {
		return sid
	}
	if sid := strings.TrimSpace(gjson.GetBytes(body, "metadata.session_id").String()); sid != "" {
		return sid
	}
	return ""
}

func sidSessionKey(sid string) string {
	sum := sha256.Sum256([]byte("cc-session\n" + strings.TrimSpace(sid)))
	return hex.EncodeToString(sum[:8])
}

func contentSessionKey(model string, body []byte) string {
	first := firstUserText(body)
	system := systemText(body)
	if len(system) > 2048 {
		system = system[:2048]
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(model)) + "\n" + first + "\n" + system))
	return hex.EncodeToString(sum[:8])
}

func looksLikeCompact(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return false
	}
	for _, marker := range []string{
		"this session is being continued from a previous conversation",
		"conversation is summarized below",
		"the conversation summary",
		"please continue the conversation from where it left off",
		"ran out of context",
		"compacted conversation",
		"此会话从之前的对话继续",
		"对话摘要如下",
		"上下文已满",
		"会话因上下文",
	} {
		if strings.Contains(t, marker) {
			return true
		}
	}
	return strings.Contains(t, "<summary>") && strings.Contains(t, "continue")
}

func lastUserText(body []byte) string {
	var found string
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		role := strings.ToLower(msg.Get("role").String())
		if role == "user" {
			found = contentText(msg.Get("content"))
		}
		return true
	})
	return strings.TrimSpace(found)
}

func isCompactSummaryPrompt(text string) bool {
	t := strings.ToLower(text)
	if t == "" {
		return false
	}
	markers := []string{
		"critical: respond with text only. do not call any tools.",
		"your task is to create a detailed summary of the conversation so far",
		"an <analysis> block followed by a <summary> block",
	}
	for _, marker := range markers {
		if !strings.Contains(t, marker) {
			return false
		}
	}
	return true
}

func hasCompactAPIEdit(body []byte) bool {
	found := false
	gjson.GetBytes(body, "context_management.edits").ForEach(func(_, edit gjson.Result) bool {
		if strings.Contains(strings.ToLower(edit.Get("type").String()), "compact") {
			found = true
			return false
		}
		return true
	})
	return found
}

func isCompactRequest(headers http.Header, body []byte) bool {
	if looksLikeCompact(firstUserText(body)) || looksLikeCompact(lastUserText(body)) || looksLikeCompact(systemText(body)) {
		return true
	}
	if isCompactSummaryPrompt(lastUserText(body)) {
		return true
	}
	if hasCompactAPIEdit(body) {
		return true
	}
	beta := strings.ToLower(headerGetCI(headers, "Anthropic-Beta"))
	return strings.Contains(beta, "compact-2026")
}

func sessionLabel(body []byte) string {
	text := strings.Join(strings.Fields(firstUserText(body)), " ")
	if text == "" {
		return "未命名对话"
	}
	return truncateRunes(text, 42)
}

func firstUserText(body []byte) string {
	var fallback, found string
	gjson.GetBytes(body, "messages").ForEach(func(idx, msg gjson.Result) bool {
		text := contentText(msg.Get("content"))
		if idx.Int() == 0 {
			fallback = text
		}
		role := strings.ToLower(msg.Get("role").String())
		if role == "user" || (role == "" && idx.Int() == 0) {
			found = text
			return false
		}
		return true
	})
	if strings.TrimSpace(found) != "" {
		return strings.TrimSpace(found)
	}
	return strings.TrimSpace(fallback)
}

func contentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	var b strings.Builder
	content.ForEach(func(_, value gjson.Result) bool {
		if value.Get("type").String() == "text" || value.Get("text").Exists() {
			if t := strings.TrimSpace(value.Get("text").String()); t != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(t)
			}
		}
		return b.Len() < 8000
	})
	return b.String()
}

func systemText(body []byte) string {
	sys := gjson.GetBytes(body, "system")
	if !sys.Exists() {
		return ""
	}
	if sys.Type == gjson.String || sys.IsArray() {
		return contentText(sys)
	}
	return strings.TrimSpace(sys.Raw)
}

func truncateRunes(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}

// sessionNextPingAt aligns keepalive slots to the last real upstream request
// (LastSeen), not to the last keepalive ping and not to plugin start.
func sessionNextPingAt(lastSeen, lastPing, now time.Time, interval time.Duration) time.Time {
	if lastSeen.IsZero() || interval <= 0 {
		return time.Time{}
	}
	elapsed := now.Sub(lastSeen)
	if elapsed < interval {
		return lastSeen.Add(interval)
	}
	n := elapsed / interval
	slotStart := lastSeen.Add(n * interval)
	if lastPing.Before(slotStart) {
		return slotStart
	}
	return slotStart.Add(interval)
}

func sessionDue(lastSeen, lastPing, now time.Time, interval time.Duration) bool {
	if lastSeen.IsZero() || interval <= 0 {
		return false
	}
	if now.Sub(lastSeen) < interval {
		return false
	}
	n := now.Sub(lastSeen) / interval
	slotStart := lastSeen.Add(n * interval)
	return lastPing.Before(slotStart)
}
