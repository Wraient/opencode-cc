package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kiowx/opencode-cc/internal/config"
)

func TestSetZenSessionHeadersForwardsClientValues(t *testing.T) {
	up, _ := http.NewRequest(http.MethodPost, "http://x/v1/responses", nil)
	down := http.Header{}
	down.Set("x-opencode-session", "ses_0123456789abcdefghijklmnop")
	down.Set("x-opencode-request", "msg_abcdefghijklmnopqrstuvwxyz")
	down.Set("x-opencode-project", "prj_9")
	down.Set("x-opencode-client", "opencode")
	setZenSessionHeaders(up, down, "sticky-should-lose")
	if got := up.Header.Get("x-opencode-session"); got != "ses_0123456789abcdefghijklmnop" {
		t.Errorf("session = %q, want forwarded client value", got)
	}
	if got := up.Header.Get("x-opencode-request"); got != "msg_abcdefghijklmnopqrstuvwxyz" {
		t.Errorf("request = %q, want forwarded client value", got)
	}
	if got := up.Header.Get("x-opencode-project"); got != "prj_9" {
		t.Errorf("project = %q, want forwarded client value", got)
	}
	if got := up.Header.Get("x-opencode-client"); got != "opencode" {
		t.Errorf("client = %q, want forwarded client value", got)
	}
}

func TestSetZenSessionHeadersNormalizesLegacyIdentity(t *testing.T) {
	up, _ := http.NewRequest(http.MethodPost, "http://x/v1/chat/completions", nil)
	down := http.Header{}
	down.Set("x-session-id", "550e8400-e29b-41d4-a716-446655440000")
	setZenSessionHeaders(up, down, "sticky-key")
	got := up.Header.Get("x-opencode-session")
	if !canonicalSessionPattern.MatchString(got) {
		t.Fatalf("normalized session = %q, want canonical shape", got)
	}
	if up.Header.Get("x-session-affinity") != got || up.Header.Get("x-session-id") != got {
		t.Fatalf("affinity headers do not match normalized session: %q", got)
	}
}

func TestSetZenSessionHeadersSynthesizes(t *testing.T) {
	up, _ := http.NewRequest(http.MethodPost, "http://x/v1/chat/completions", nil)
	setZenSessionHeaders(up, http.Header{}, "sticky-key-abc")
	if got := up.Header.Get("x-opencode-session"); !strings.HasPrefix(got, "ses_") || len(got) != 30 {
		t.Errorf("session = %q, want stock ses_ format", got)
	}
	// Deterministic per sticky key: same session across turns/restarts.
	up2, _ := http.NewRequest(http.MethodPost, "http://x/v1/chat/completions", nil)
	setZenSessionHeaders(up2, http.Header{}, "sticky-key-abc")
	if up2.Header.Get("x-opencode-session") != up.Header.Get("x-opencode-session") {
		t.Errorf("session not stable for same sticky key")
	}
	if got := up.Header.Get("x-opencode-request"); !strings.HasPrefix(got, "msg_") || len(got) != 30 {
		t.Errorf("request = %q, want stock msg_ format", got)
	}
	if got := up.Header.Get("x-opencode-client"); got != "cli" {
		t.Errorf("client = %q, want stock cli", got)
	}
	if got := up.Header.Get("x-opencode-project"); got != "global" {
		t.Errorf("project = %q, want global fallback", got)
	}
	if got := up.Header.Get("x-session-affinity"); got != up.Header.Get("x-opencode-session") {
		t.Errorf("session affinity = %q, want it to match x-opencode-session", got)
	}
	if got := up.Header.Get("x-session-id"); got != up.Header.Get("x-opencode-session") {
		t.Errorf("session id = %q, want it to match x-opencode-session", got)
	}
	// No proxy fingerprint anywhere.
	for _, h := range []string{"x-opencode-session", "x-opencode-request", "x-opencode-client"} {
		if strings.Contains(strings.ToLower(up.Header.Get(h)), "opencode-cc") {
			t.Errorf("header %s leaks proxy identity: %q", h, up.Header.Get(h))
		}
	}
}

func TestSetZenSessionHeadersOmitsUnknownSession(t *testing.T) {
	// Since 2026-09 Zen hard-rejects free-tier requests without
	// x-opencode-session (MissingSessionID), so the header is never
	// omitted: with no sticky key a single process-stable fallback id
	// is used instead (stable, so provider pinning stays coherent).
	up, _ := http.NewRequest(http.MethodPost, "http://x/v1/chat/completions", nil)
	setZenSessionHeaders(up, http.Header{}, "")
	got := up.Header.Get("x-opencode-session")
	if !strings.HasPrefix(got, "ses_") || len(got) != 30 {
		t.Fatalf("session = %q, want stock ses_ fallback format", got)
	}
	up2, _ := http.NewRequest(http.MethodPost, "http://x/v1/chat/completions", nil)
	setZenSessionHeaders(up2, http.Header{}, "")
	if up2.Header.Get("x-opencode-session") != got {
		t.Errorf("fallback session not stable across calls")
	}
}

// TestPassthroughSendsSessionHeaders is the regression test for the Sep 2026
// x-opencode-session requirement: every upstream LLM request must carry the
// session header Zen uses for sticky routing and cache scoping.
func TestPassthroughSendsSessionHeaders(t *testing.T) {
	var gotSession, gotClient, gotRequest string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession = r.Header.Get("x-opencode-session")
		gotClient = r.Header.Get("x-opencode-client")
		gotRequest = r.Header.Get("x-opencode-request")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[]}`)
	}))
	defer upstream.Close()

	st := mustTestStore(t)
	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{{BaseURL: upstream.URL, APIKey: "k", Enabled: true}}
	srv := New(cfg, st)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"muse-spark-1.3-contributor-free","input":"say ok"}`))
	req.Header.Set("x-opencode-session", "ses_0123456789abcdefghijklmnop")
	rec := httptest.NewRecorder()
	srv.ResponsesProxy().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotSession != "ses_0123456789abcdefghijklmnop" {
		t.Errorf("upstream session = %q, want forwarded downstream value", gotSession)
	}
	if gotClient == "" || gotRequest == "" {
		t.Errorf("upstream client=%q request=%q, want both set", gotClient, gotRequest)
	}
}
