package main

import (
	"strings"

	"github.com/tidwall/sjson"
	"gopkg.in/yaml.v3"
)

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
