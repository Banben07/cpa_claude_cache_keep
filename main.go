package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginID      = "claude-cache-keepalive"
	pluginVersion = "0.8.0"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	RequestInterceptor bool `json:"request_interceptor"`
	ManagementAPI      bool `json:"management_api"`
	UsagePlugin        bool `json:"usage_plugin"`
}

type statusPage struct {
	Sessions          []session
	EnabledCount      int
	IntervalMin       int
	MaxTokens         int
	MaxSessions       int
	IdleEvictMin      int
	LastPingAt        time.Time
	LastPingError     string
	LoopStartedAt     time.Time
	NextPingAt        time.Time
	Now               time.Time
	Budget            budgetSnapshot
	QuotaLog          []quotaSample
	Version           string
	SubagentSkipped   int
	LastSubagentKind  string
	LastSubagentAt    time.Time
	LastSubagentLabel string
}

type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

var (
	mu                sync.Mutex
	cfg               = defaultConfig()
	sessions          = map[string]*session{}
	lastPingAt        time.Time
	lastErr           string
	loopStartedAt     time.Time
	pinging           bool
	stopCh            chan struct{}
	subagentSkipped   int
	lastSubagentAt    time.Time
	lastSubagentKind  string
	lastSubagentLabel string
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	stopLoop()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister:
		applyConfig(request)
		startLoop()
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginReconfigure:
		applyConfig(request)
		startLoop()
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginShutdown:
		stopLoop()
		return okEnvelope(map[string]any{})
	case pluginabi.MethodRequestInterceptBefore:
		return handleInterceptBefore(request)
	case pluginabi.MethodRequestInterceptAfter:
		saveSnapshot(request)
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodUsageHandle:
		handleUsage(request)
		return okEnvelope(map[string]any{})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(map[string]any{
			"resources": []map[string]string{{
				"Path":        "/status",
				"Menu":        "缓存保活",
				"Description": "按对话分别保活 Claude prompt cache，可勾选开关。",
			}},
		})
	case pluginabi.MethodManagementHandle:
		return handleStatusRequest(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginID,
			Version:          pluginVersion,
			Author:           "Banben07",
			GitHubRepository: "https://github.com/Banben07/cpa_claudec_keep",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "interval_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "How often to replay enabled sessions. Default 50."},
				{Name: "max_tokens", Type: pluginapi.ConfigFieldTypeInteger, Description: "Output cap on keepalive pings. Default 1."},
				{Name: "max_sessions", Type: pluginapi.ConfigFieldTypeInteger, Description: "Max remembered conversations. Default 8."},
				{Name: "idle_evict_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "Drop unchecked sessions after this idle time. Default 180. 0 keeps them until replaced."},
				{Name: "window_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "Usage window matching Claude's 5-hour session limit. Default 300."},
				{Name: "stop_percent", Type: pluginapi.ConfigFieldTypeInteger, Description: "Stop CPA chat at this 5-hour utilization percent. Default 95. Status page can change it; 60-99."},
				{Name: "reserve_percent", Type: pluginapi.ConfigFieldTypeInteger, Description: "How much of the 5-hour window to leave after stop_percent. Default 5. Ignored if stop_percent is set."},
				{Name: "five_hour_budget", Type: pluginapi.ConfigFieldTypeInteger, Description: "Fallback weighted budget if upstream 5h headers are missing. 0 uses Anthropic utilization headers."},
				{Name: "guard_chat", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Block new Claude chat through CPA once the 5-hour window hits the reserve line. Default true."},
			},
		},
		Capabilities: registrationCapabilities{
			RequestInterceptor: true,
			ManagementAPI:      true,
			UsagePlugin:        true,
		},
	}
}

func applyConfig(request []byte) {
	var req lifecycleRequest
	if len(request) > 0 {
		_ = json.Unmarshal(request, &req)
	}
	next := parseConfig(req.ConfigYAML)
	if stop, ok := loadPersistedStopPercent(); ok {
		next.ReservePercent = 100 - stop
	}
	mu.Lock()
	cfg = next
	mu.Unlock()
}

func saveSnapshot(raw []byte) {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return
	}
	if isKeepaliveRequest(req.Headers) {
		return
	}
	if !isClaudeUpstream(req.ToFormat, req.Model) {
		return
	}
	if len(req.Body) == 0 {
		return
	}
	upsertSession(req.Model, req.SourceFormat, req.ToFormat, req.Headers, req.Body)
}

func handleInterceptBefore(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}
	if isKeepaliveRequest(req.Headers) {
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	}
	if !isClaudeUpstream(req.ToFormat, req.Model) || !isKeepaliveCandidate(req.Body) {
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	}
	if shouldBlockChat(time.Now(), req.Model, req.Headers, req.Body) {
		return okEnvelope(chatGuardResponse())
	}
	return okEnvelope(pluginapi.RequestInterceptResponse{})
}

func handleUsage(raw []byte) {
	var rec pluginapi.UsageRecord
	if len(raw) == 0 {
		return
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return
	}
	recordUsage(rec)
}

func handleStatusRequest(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}
	redirect := false
	if id := sanitizeSessionID(queryGet(req.Query, "toggle")); id != "" {
		on := queryGet(req.Query, "on")
		setSessionEnabled(id, on != "0")
		redirect = true
	}
	if id := sanitizeSessionID(queryGet(req.Query, "forget")); id != "" {
		forgetSession(id)
		redirect = true
	}
	if id := sanitizeSessionID(queryGet(req.Query, "rename")); id != "" {
		renameSession(id, queryGet(req.Query, "name"))
		redirect = true
	}
	if stop, ok := parseStopQuery(queryGet(req.Query, "stop")); ok {
		setStopPercent(stop)
		redirect = true
	}
	if redirect && strings.TrimSpace(req.Path) != "" {
		return okEnvelope(map[string]any{
			"StatusCode": http.StatusSeeOther,
			"Headers":    http.Header{"Location": []string{req.Path}},
			"Body":       []byte{},
		})
	}
	return okEnvelope(map[string]any{
		"StatusCode": http.StatusOK,
		"Headers": http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		"Body": []byte(renderStatusHTML()),
	})
}

func queryGet(query map[string][]string, key string) string {
	if query == nil {
		return ""
	}
	values := query[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func sanitizeSessionID(id string) string {
	id = strings.TrimSpace(strings.ToLower(id))
	if len(id) != 16 {
		return ""
	}
	for _, c := range id {
		hex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !hex {
			return ""
		}
	}
	return id
}

func startLoop() {
	stopLoop()
	mu.Lock()
	loopStartedAt = time.Now()
	mu.Unlock()
	ch := make(chan struct{})
	mu.Lock()
	stopCh = ch
	mu.Unlock()
	go pingLoop(ch)
}

func stopLoop() {
	mu.Lock()
	ch := stopCh
	stopCh = nil
	mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

const checkEvery = 30 * time.Second

func pingLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			sent, n, err := pingOnce()
			if !sent {
				continue
			}
			mu.Lock()
			lastPingAt = time.Now()
			if err != nil {
				lastErr = err.Error()
			} else {
				lastErr = ""
			}
			mu.Unlock()
			if err != nil {
				_ = hostLog(fmt.Sprintf("claude-cache-keepalive ping failed (%d sessions): %s", n, err.Error()))
			} else {
				_ = hostLog(fmt.Sprintf("claude-cache-keepalive ping ok (%d sessions)", n))
			}
		}
	}
}

func pingOnce() (bool, int, error) {
	mu.Lock()
	if pinging {
		mu.Unlock()
		return false, 0, nil
	}
	pinging = true
	maxTokens := cfg.MaxTokens
	interval := time.Duration(cfg.IntervalMinutes) * time.Minute
	mu.Unlock()
	defer func() {
		mu.Lock()
		pinging = false
		mu.Unlock()
	}()

	targets := dueSnapshots(time.Now(), interval)
	if currentBudget(time.Now()).KeepalivePaused {
		return false, 0, nil
	}
	if len(targets) == 0 {
		return false, 0, nil
	}
	var errs []string
	for _, snap := range targets {
		err := pingSession(snap, maxTokens)
		recordSessionPing(snap.ID, time.Now(), err)
		if err != nil {
			errs = append(errs, snap.Label+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return true, len(targets), fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return true, len(targets), nil
}

func pingSession(snap session, maxTokens int) error {
	if len(snap.Body) == 0 {
		return fmt.Errorf("empty snapshot")
	}
	body, err := limitOutput(snap.Body, maxTokens)
	if err != nil {
		return err
	}
	entry := snap.SourceFormat
	exit := snap.ToFormat
	if entry == "" {
		entry = "claude"
	}
	if exit == "" {
		exit = entry
	}
	mu.Lock()
	keepaliveActive++
	currentPingID = snap.ID
	mu.Unlock()
	defer func() {
		mu.Lock()
		keepaliveActive--
		if keepaliveActive < 0 {
			keepaliveActive = 0
		}
		currentPingID = ""
		mu.Unlock()
	}()
	_, err = callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: entry,
			ExitProtocol:  exit,
			Model:         snap.Model,
			Stream:        false,
			Body:          body,
			Headers:       withKeepaliveHeader(snap.Headers),
		},
	})
	return err
}

func currentStatus() statusPage {
	mu.Lock()
	now := time.Now()
	interval := time.Duration(cfg.IntervalMinutes) * time.Minute
	page := statusPage{
		IntervalMin:       cfg.IntervalMinutes,
		MaxTokens:         cfg.MaxTokens,
		MaxSessions:       cfg.MaxSessions,
		IdleEvictMin:      cfg.IdleEvictMinutes,
		LastPingAt:        lastPingAt,
		LastPingError:     lastErr,
		LoopStartedAt:     loopStartedAt,
		Now:               now,
		SubagentSkipped:   subagentSkipped,
		LastSubagentKind:  lastSubagentKind,
		LastSubagentAt:    lastSubagentAt,
		LastSubagentLabel: lastSubagentLabel,
	}
	mu.Unlock()
	page.Budget = currentBudget(now)
	page.QuotaLog = listQuotaSamples()
	page.Version = pluginVersion
	page.Sessions = listSessions()
	for _, item := range page.Sessions {
		if !item.Enabled {
			continue
		}
		page.EnabledCount++
		next := sessionNextPingAt(item.LastSeen, item.LastPingAt, now, interval)
		if next.IsZero() {
			continue
		}
		if page.NextPingAt.IsZero() || next.Before(page.NextPingAt) {
			page.NextPingAt = next
		}
	}
	return page
}

func hostLog(message string) error {
	_, err := callHost(pluginabi.MethodHostLog, map[string]string{"message": message})
	return err
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", method, err)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	code := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("%s returned empty, code=%d", method, int(code))
	}
	var env envelope
	if err := json.Unmarshal(rawResponse, &env); err != nil {
		return nil, fmt.Errorf("decode %s: %w", method, err)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("%s failed", method)
	}
	if code != 0 {
		return nil, fmt.Errorf("%s code=%d", method, int(code))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func cloneHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	cloned := make(http.Header, len(headers))
	for key, values := range headers {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}
