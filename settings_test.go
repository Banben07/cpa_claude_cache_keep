package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestParseConfigDefaultMessagesURL(t *testing.T) {
	got := parseConfig(nil)
	if got.MessagesURL != defaultMessagesURL {
		t.Fatalf("messages_url=%q", got.MessagesURL)
	}
}

func TestParseStopPercentOverridesReserve(t *testing.T) {
	got := parseConfig([]byte("reserve_percent: 10\nstop_percent: 95\n"))
	if got.ReservePercent != 5 {
		t.Fatalf("reserve=%d", got.ReservePercent)
	}
	got = parseConfig([]byte("stop_percent: 90\n"))
	if got.ReservePercent != 10 {
		t.Fatalf("reserve=%d", got.ReservePercent)
	}
	got = parseConfig([]byte("stop_percent: 200\n"))
	if got.ReservePercent != 1 {
		t.Fatalf("clamped reserve=%d", got.ReservePercent)
	}
}

func TestSetStopPercentPersistsAndReloads(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	mu.Lock()
	settingsPath = path
	persistSettings = true
	mu.Unlock()

	if setStopPercent(90) != 90 {
		t.Fatal("set")
	}
	snap := currentBudget(time.Now())
	if snap.BlockAtPercent != 90 || snap.ReservePercent != 10 {
		t.Fatalf("block=%d reserve=%d", snap.BlockAtPercent, snap.ReservePercent)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved persistedSettings
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.StopPercent != 90 {
		t.Fatalf("saved=%d", saved.StopPercent)
	}

	mu.Lock()
	cfg = defaultConfig()
	mu.Unlock()
	applyConfig(nil)
	snap = currentBudget(time.Now())
	if snap.BlockAtPercent != 90 {
		t.Fatalf("reload block=%d", snap.BlockAtPercent)
	}
}

func TestHandleStatusStopRedirects(t *testing.T) {
	resetSessionsForTest()
	t.Cleanup(resetSessionsForTest)
	dir := t.TempDir()
	mu.Lock()
	settingsPath = filepath.Join(dir, "s.json")
	persistSettings = true
	mu.Unlock()

	raw, err := json.Marshal(pluginapi.ManagementRequest{
		Path:  "/v0/resource/plugins/claude-cache-keepalive/status",
		Query: url.Values{"stop": {"92"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := handleStatusRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatal(err)
	}
	var out pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatal(err)
	}
	if out.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d", out.StatusCode)
	}
	snap := currentBudget(time.Now())
	if snap.BlockAtPercent != 92 {
		t.Fatalf("block=%d", snap.BlockAtPercent)
	}
}
