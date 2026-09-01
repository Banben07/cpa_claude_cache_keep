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
	"html"
	"net/http"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const pluginID = "claude-cache-keepalive"

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
}

type snapshot struct {
	Model        string
	SourceFormat string
	ToFormat     string
	Headers      http.Header
	Body         []byte
	SavedAt      time.Time
}

type statusPage struct {
	HasSnapshot   bool      `json:"has_snapshot"`
	Model         string    `json:"model,omitempty"`
	ToFormat      string    `json:"to_format,omitempty"`
	BodyBytes     int       `json:"body_bytes,omitempty"`
	SavedAt       time.Time `json:"saved_at,omitempty"`
	LastPingAt    time.Time `json:"last_ping_at,omitempty"`
	LastPingError string    `json:"last_ping_error,omitempty"`
	IntervalMin   int       `json:"interval_minutes"`
	MaxTokens     int       `json:"max_tokens"`
}

type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

var (
	mu         sync.Mutex
	cfg        = defaultConfig()
	last       snapshot
	lastPingAt time.Time
	lastErr    string
	stopCh     chan struct{}
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
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodRequestInterceptAfter:
		saveSnapshot(request)
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(map[string]any{
			"resources": []map[string]string{{
				"Path":        "/status",
				"Menu":        "Cache Keepalive",
				"Description": "Last Claude request snapshot and keepalive ping status.",
			}},
		})
	case pluginabi.MethodManagementHandle:
		return okEnvelope(map[string]any{
			"StatusCode": http.StatusOK,
			"Headers":    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			"Body":       []byte(renderStatusHTML()),
		})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginID,
			Version:          "0.1.1",
			Author:           "local",
			GitHubRepository: "https://github.com/local/claude-cache-keepalive",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "interval_minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "How often to replay the last Claude request. Default 50."},
				{Name: "max_tokens", Type: pluginapi.ConfigFieldTypeInteger, Description: "Output cap on keepalive pings. Default 1; 0 is better if the upstream allows it."},
			},
		},
		Capabilities: registrationCapabilities{
			RequestInterceptor: true,
			ManagementAPI:      true,
		},
	}
}

func applyConfig(request []byte) {
	var req lifecycleRequest
	if len(request) > 0 {
		_ = json.Unmarshal(request, &req)
	}
	next := parseConfig(req.ConfigYAML)
	mu.Lock()
	cfg = next
	mu.Unlock()
}

func saveSnapshot(raw []byte) {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return
	}
	if !isClaudeUpstream(req.ToFormat, req.Model) {
		return
	}
	if len(req.Body) == 0 {
		return
	}
	mu.Lock()
	last = snapshot{
		Model:        req.Model,
		SourceFormat: req.SourceFormat,
		ToFormat:     req.ToFormat,
		Headers:      cloneHeader(req.Headers),
		Body:         append([]byte(nil), req.Body...),
		SavedAt:      time.Now(),
	}
	mu.Unlock()
}

func startLoop() {
	stopLoop()
	mu.Lock()
	interval := time.Duration(cfg.IntervalMinutes) * time.Minute
	mu.Unlock()
	ch := make(chan struct{})
	mu.Lock()
	stopCh = ch
	mu.Unlock()
	go pingLoop(interval, ch)
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

func pingLoop(interval time.Duration, stop <-chan struct{}) {
	if interval <= 0 {
		interval = 50 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := pingOnce(); err != nil {
				mu.Lock()
				lastErr = err.Error()
				lastPingAt = time.Now()
				mu.Unlock()
				_ = hostLog("claude-cache-keepalive ping failed: " + err.Error())
			} else {
				mu.Lock()
				lastErr = ""
				lastPingAt = time.Now()
				mu.Unlock()
				_ = hostLog("claude-cache-keepalive ping ok")
			}
		}
	}
}

func pingOnce() error {
	mu.Lock()
	snap := last
	maxTokens := cfg.MaxTokens
	mu.Unlock()
	if len(snap.Body) == 0 {
		return nil
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
	_, err = callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: entry,
			ExitProtocol:  exit,
			Model:         snap.Model,
			Stream:        false,
			Body:          body,
			Headers:       cloneHeader(snap.Headers),
		},
	})
	return err
}

func currentStatus() statusPage {
	mu.Lock()
	defer mu.Unlock()
	page := statusPage{
		IntervalMin:   cfg.IntervalMinutes,
		MaxTokens:     cfg.MaxTokens,
		LastPingAt:    lastPingAt,
		LastPingError: lastErr,
	}
	if len(last.Body) == 0 {
		return page
	}
	page.HasSnapshot = true
	page.Model = last.Model
	page.ToFormat = last.ToFormat
	page.BodyBytes = len(last.Body)
	page.SavedAt = last.SavedAt
	return page
}

func renderStatusHTML() string {
	st := currentStatus()
	errBlock := ""
	if st.LastPingError != "" {
		errBlock = "<p class=\"error\">last ping error: " + html.EscapeString(st.LastPingError) + "</p>"
	}
	saved := "none"
	if st.HasSnapshot {
		saved = st.SavedAt.Format(time.RFC3339)
	}
	pinged := "never"
	if !st.LastPingAt.IsZero() {
		pinged = st.LastPingAt.Format(time.RFC3339)
	}
	return `<!doctype html><meta charset="utf-8"><title>Cache Keepalive</title>
<style>body{font-family:sans-serif;margin:2rem;line-height:1.5}code{background:#f3f4f6;padding:.1rem .3rem;border-radius:4px}.error{color:#b42318}</style>
<h1>Claude cache keepalive</h1>
<p>Replays the last Claude upstream request every <code>` + fmt.Sprintf("%d", st.IntervalMin) + `</code> minutes with <code>max_tokens=` + fmt.Sprintf("%d", st.MaxTokens) + `</code>.</p>
<ul>
<li>snapshot: ` + html.EscapeString(saved) + `</li>
<li>model: ` + html.EscapeString(st.Model) + `</li>
<li>to_format: ` + html.EscapeString(st.ToFormat) + `</li>
<li>body bytes: ` + fmt.Sprintf("%d", st.BodyBytes) + `</li>
<li>last ping: ` + html.EscapeString(pinged) + `</li>
</ul>` + errBlock
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
