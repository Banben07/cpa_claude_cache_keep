package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const defaultMessagesURL = "http://127.0.0.1:8317/v1/messages"

// pingDo sends a keepalive replay through CPA's inbound HTTP path (same gin
// /v1/messages handler Claude Code uses). Tests replace this.
var pingDo = httpPingMessages

var pingClient = &http.Client{
	Timeout: 120 * time.Second,
	Transport: &http.Transport{
		Proxy:              nil,
		DisableCompression: true,
	},
}

var hopByHopHeaders = map[string]bool{
	"Accept-Encoding":     true,
	"Connection":          true,
	"Content-Length":      true,
	"Host":                true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func normalizeMessagesURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultMessagesURL
	}
	if strings.HasPrefix(raw, "/") {
		return "http://127.0.0.1:8317" + raw
	}
	return raw
}

func copyPingHeaders(headers http.Header) http.Header {
	out := withKeepaliveHeader(nil)
	for key, values := range headers {
		if hopByHopHeaders[http.CanonicalHeaderKey(key)] {
			continue
		}
		if strings.EqualFold(key, keepaliveHeaderKey) {
			continue
		}
		out[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
	}
	out.Set(keepaliveHeaderKey, keepaliveHeaderVal)
	if out.Get("Content-Type") == "" {
		out.Set("Content-Type", "application/json")
	}
	return out
}

func httpPingMessages(url string, headers http.Header, body []byte) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header = cloneHeader(headers)
	resp, err := pingClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

func replayPing(messagesURL string, headers http.Header, body []byte) error {
	status, raw, err := pingDo(messagesURL, copyPingHeaders(headers), body)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	msg := strings.TrimSpace(string(raw))
	if utf8.RuneCountInString(msg) > 300 {
		msg = string([]rune(msg)[:300]) + "…"
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("POST %s: %d %s", messagesURL, status, msg)
}
