// Package server wires the HTTP layer: routes, auth, upstream proxying with
// streaming, and request logging. The panel API lives in package api.
package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// writeJSON writes v as JSON with the given status. Shared by handlers in this
// package (proxy has its own copy to avoid a cycle).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAnthropicError writes an Anthropic-shaped error response.
func writeAnthropicError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": errType, "message": msg},
	})
}

// writeOpenAIError writes an OpenAI-compatible error response.
func writeOpenAIError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    errType,
			"param":   nil,
			"code":    nil,
		},
	})
}

func (s *Server) upstreamClient(stream bool, timeoutSeconds int) *http.Client {
	if stream || timeoutSeconds <= 0 {
		return s.httpClient
	}
	// Reuse the hardened shared transport (H1-only, header timeout) so
	// timeout clients don't silently bypass it via http.DefaultTransport.
	return &http.Client{Transport: s.httpClient.Transport, Timeout: time.Duration(timeoutSeconds) * time.Second}
}

// doUpstreamWithRetry performs one upstream request, retrying ONCE when the
// first attempt never yields a usable response: transport timeouts (Zen
// intermittently stalls >30s awaiting response headers on big streaming
// histories) or upstream 5xx. Safe to retry: at Do-time nothing has been
// written to the downstream client yet, and a fresh /v1/responses POST has
// no side effects. No retry when the downstream client already went away.
func doUpstreamWithRetry(client *http.Client, template *http.Request, body []byte) (*http.Response, error) {
	var lastErr error
	var lastStatus int
	var lastDetail string
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			if template.Context().Err() != nil {
				break // downstream went away; don't hammer upstream
			}
			time.Sleep(2 * time.Second)
		}
		req := template.Clone(template.Context())
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode < 500 {
			return resp, nil
		}
		if err != nil {
			lastErr = err
			if !isTimeoutErr(err) {
				return nil, err
			}
			if attempt == 0 {
				log.Printf("opencode-cc: upstream attempt %d timeout (%s), retrying once", attempt+1, truncateErr(err.Error()))
				continue
			}
			break
		}
		lastStatus = resp.StatusCode
		lastDetail = "upstream HTTP " + resp.Status
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		if attempt == 0 {
			log.Printf("opencode-cc: upstream attempt %d got %s, retrying once", attempt+1, resp.Status)
		}
	}
	if lastErr != nil && lastStatus == 0 {
		return nil, lastErr
	}
	if lastStatus >= 500 {
		return nil, &upstreamStatusError{status: lastStatus, detail: lastDetail}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &upstreamStatusError{status: http.StatusBadGateway, detail: "upstream unavailable"}
}

type upstreamStatusError struct {
	status int
	detail string
}

func (e *upstreamStatusError) Error() string {
	return "upstream request failed after retry: " + e.detail
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if te, ok := err.(interface{ Timeout() bool }); ok && te.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout awaiting response headers") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe")
}

func truncateErr(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
