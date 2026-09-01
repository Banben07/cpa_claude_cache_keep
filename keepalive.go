package main

import (
	"crypto/sha256"
	"encoding/hex"
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
	IntervalMinutes  int `yaml:"interval_minutes"`
	MaxTokens        int `yaml:"max_tokens"`
	MaxSessions      int `yaml:"max_sessions"`
	IdleEvictMinutes int `yaml:"idle_evict_minutes"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{IntervalMinutes: 50, MaxTokens: 1, MaxSessions: 8, IdleEvictMinutes: 180}
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
	return cfg
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

func sessionKey(model string, body []byte) string {
	first := firstUserText(body)
	system := systemText(body)
	if len(system) > 2048 {
		system = system[:2048]
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(model)) + "\n" + first + "\n" + system))
	return hex.EncodeToString(sum[:8])
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

func nextPingAt(now, started time.Time, interval time.Duration) time.Time {
	if started.IsZero() || interval <= 0 {
		return time.Time{}
	}
	if now.Before(started) {
		return started.Add(interval)
	}
	elapsed := now.Sub(started)
	n := elapsed/interval + 1
	return started.Add(n * interval)
}
