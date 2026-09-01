package main

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"gopkg.in/yaml.v3"
)

type bodyInfo struct {
	HasMaxTokens        bool
	Stream              bool
	MessageCount        int
	CacheControlCount   int
	CacheTTL            string
}

type pluginConfig struct {
	IntervalMinutes int `yaml:"interval_minutes"`
	MaxTokens       int `yaml:"max_tokens"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{IntervalMinutes: 50, MaxTokens: 1}
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
	}
	switch {
	case strings.Contains(string(body), `"ttl":"1h"`) || strings.Contains(string(body), `"ttl": "1h"`):
		info.CacheTTL = "1h"
	case strings.Contains(string(body), `"ttl":"5m"`) || strings.Contains(string(body), `"ttl": "5m"`):
		info.CacheTTL = "5m"
	}
	return info
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
