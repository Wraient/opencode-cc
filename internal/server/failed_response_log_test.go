package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Kiowx/opencode-cc/internal/store"
)

// TestFailedUpstreamResponseIsKept covers request #30486 (2026-10-23): a 429
// from Zen on /v1/messages was logged with an empty response, so the reset
// time (Retry-After / x-ratelimit-* headers and the error body) was lost. The
// request row must keep the upstream body and headers for any failed request,
// not just the parsed message.
func TestFailedUpstreamResponseIsKept(t *testing.T) {
	const (
		retryAfter = "3600"
		resetAt    = "1792765298"
		bodyJSON   = `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached. Subscribe to OpenCode Go to keep going: https://opencode.ai/go","resetsAt":1792765298}}`
	)
	zen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", retryAfter)
		w.Header().Set("x-ratelimit-reset", resetAt)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, bodyJSON)
	}))
	defer zen.Close()

	srv, st := newTestServer(t, zen.URL)

	body, _ := json.Marshal(map[string]any{
		"model":      "claude-3-5-sonnet-20241022",
		"max_tokens": 32,
		"stream":     true,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	srv.Proxy()(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("client status = %d, want 429; body: %s", rr.Code, rr.Body.String())
	}

	row := waitForLoggedRequest(t, st)
	if row.Status != http.StatusTooManyRequests {
		t.Errorf("logged status = %d, want 429", row.Status)
	}
	if !strings.Contains(row.Error, "Free usage limit reached") {
		t.Errorf("logged error = %q, want the upstream message", row.Error)
	}
	// The response itself must survive, including the reset time.
	if !strings.Contains(row.RespBody, "Free usage limit reached") {
		t.Errorf("resp_body does not keep the upstream error body: %q", row.RespBody)
	}
	if !strings.Contains(row.RespBody, "resetsAt") {
		t.Errorf("resp_body lost the reset field: %q", row.RespBody)
	}
	for _, want := range []string{"Retry-After", retryAfter, "X-Ratelimit-Reset", resetAt} {
		if !strings.Contains(row.RespHeaders, want) {
			t.Errorf("resp_headers missing %q: %q", want, row.RespHeaders)
		}
	}
}

// waitForLoggedRequest polls the store because insert happens in a goroutine.
func waitForLoggedRequest(t *testing.T, st *store.Store) *store.RequestRow {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := st.ListRequests(context.Background(), 10)
		if err != nil {
			t.Fatalf("list requests: %v", err)
		}
		if len(rows) > 0 {
			return &rows[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no request row logged within 3s")
	return nil
}
