package main

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

const (
	minStopPercent      = 60
	maxStopPercent      = 99
	defaultSettingsFile = "claude-cache-keepalive.settings.json"
)

type persistedSettings struct {
	StopPercent int `json:"stop_percent"`
}

var (
	settingsPath    string
	persistSettings = true
)

func clampStopPercent(stop int) int {
	if stop < minStopPercent {
		return minStopPercent
	}
	if stop > maxStopPercent {
		return maxStopPercent
	}
	return stop
}

func stopPercentFromReserve(reserve int) int {
	if reserve <= 0 {
		reserve = defaultReservePct
	}
	return clampStopPercent(100 - reserve)
}

func resolvedSettingsPath() string {
	if settingsPath != "" {
		return settingsPath
	}
	if persistSettings {
		return defaultSettingsFile
	}
	return ""
}

func setStopPercent(stop int) int {
	stop = clampStopPercent(stop)
	mu.Lock()
	cfg.ReservePercent = 100 - stop
	path := resolvedSettingsPath()
	mu.Unlock()
	if path != "" {
		raw, err := json.MarshalIndent(persistedSettings{StopPercent: stop}, "", "  ")
		if err == nil {
			_ = os.WriteFile(path, raw, 0o644)
		}
	}
	return stop
}

func loadPersistedStopPercent() (int, bool) {
	path := resolvedSettingsPath()
	if path == "" {
		return 0, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return 0, false
	}
	var saved persistedSettings
	if err := json.Unmarshal(raw, &saved); err != nil || saved.StopPercent == 0 {
		return 0, false
	}
	return clampStopPercent(saved.StopPercent), true
}

func parseStopQuery(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return clampStopPercent(n), true
}
