package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Kiowx/opencode-cc/internal/config"
)

func TestSetZenSessionHeadersForwardsClientValues(t *testing.T) {
	up, _ := http.NewRequest(http.MethodPost, "http://x/v1/responses", nil)
	down := http.Header{}
	// Fresh genuine-shaped session: must pass through untouched (shape +
	// freshness are both required since the Oct 2026 gate).
	fresh := "ses_" + descendingSessionPrefix(time.Now().UnixMilli(), 3) + "abcdefghijklmn"
	down.Set("x-opencode-session", fresh)
	down.Set("x-opencode-request", "msg_abcdefghijklmnopqrstuvwxyz")
	down.Set("x-opencode-project", "prj_9")
	down.Set("x-opencode-client", "opencode")
	setZenSessionHeaders(up, down, "sticky-should-lose")
	if got := up.Header.Get("x-opencode-session"); got != fresh {
		t.Errorf("session = %q, want forwarded client value %q", got, fresh)
	}
	if got := up.Header.Get("x-opencode-request"); got != "" {
		t.Errorf("request = %q, want obsolete request header omitted", got)
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
	if got := up.Header.Get("x-opencode-request"); got != "" {
		t.Errorf("request = %q, want obsolete request header omitted", got)
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

// TestDescendingSessionPrefixVector pins the opencode Identifier replication
// to a known vector (computed from schema/identifier.ts descending()): any
// drift here means Zen's free-tier gate starts rejecting our sessions.
func TestDescendingSessionPrefixVector(t *testing.T) {
	if got := descendingSessionPrefix(1791904000000, 7); got != "eca32d7ffff8" {
		t.Errorf("prefix = %q, want eca32d7ffff8", got)
	}
	if got := descendingSessionPrefix(1791904000000, 1); got != "eca32d7ffffe" {
		t.Errorf("prefix = %q, want eca32d7ffffe", got)
	}
}

// decodeSessionMs36 inverts a session prefix back to the millisecond
// timestamp it encodes, reduced mod 2^36 (the prefix only carries the low
// 48 bits of ts*0x1000+counter, i.e. ts mod 2^36 in ms units).
func decodeSessionMs36(t *testing.T, session string) uint64 {
	t.Helper()
	prefix := strings.TrimPrefix(session, "ses_")[:12]
	v, err := strconv.ParseUint(prefix, 16, 48)
	if err != nil {
		t.Fatalf("prefix %q is not hex: %v", prefix, err)
	}
	cur := ^v & 0xffffffffffff
	return (cur >> 12) & 0xfffffffff
}

// sessionAgeMs returns (nowMs - sessionTs) mod 2^36 in milliseconds.
func sessionAgeMs(nowMs int64, recMs36 uint64) uint64 {
	return (uint64(nowMs)&0xfffffffff - recMs36) & 0xfffffffff
}

// TestSynthSessionIDGateShape is the regression test for the Oct 2026 Zen
// free-tier gate (FreeTierError "can only be used from within OpenCode"):
// every synthesized session must have the genuine descending-timestamp shape,
// never the old all-hex sha256 shape.
func TestSynthSessionIDGateShape(t *testing.T) {
	got := synthSessionID("sticky-key-abc")
	if !canonicalSessionPattern.MatchString(got) {
		t.Fatalf("synth session = %q, want canonical shape", got)
	}
	if hexOnlySessionPattern.MatchString(got) {
		t.Fatalf("synth session = %q, all-hex shape is rejected by Zen", got)
	}
	// Prefix must decode to a recent timestamp (well inside Zen's >=24h
	// acceptance window, probed live 2026-10-13).
	if age := sessionAgeMs(time.Now().UnixMilli(), decodeSessionMs36(t, got)); age > 24*3600*1000 {
		t.Errorf("synth session = %q decodes to %dms old, want <24h", got, age)
	}
	// Deterministic within a bucket (stable sticky routing).
	if again := synthSessionID("sticky-key-abc"); again != got {
		t.Errorf("synth not stable: %q vs %q", got, again)
	}
	if other := synthSessionID("sticky-key-other"); other == got {
		t.Errorf("synth collision for different sticky keys: %q", got)
	}
	if empty := synthSessionID("  "); empty != "" {
		t.Errorf("synth(empty) = %q, want empty", empty)
	}
}

// TestFallbackSessionIDGateShape covers the no-sticky-key path: same gate,
// random tail, bucketed timestamp prefix.
func TestFallbackSessionIDGateShape(t *testing.T) {
	got := fallbackSessionID()
	if !canonicalSessionPattern.MatchString(got) {
		t.Fatalf("fallback session = %q, want canonical shape", got)
	}
	if hexOnlySessionPattern.MatchString(got) {
		t.Fatalf("fallback session = %q, all-hex shape is rejected by Zen", got)
	}
	if age := sessionAgeMs(time.Now().UnixMilli(), decodeSessionMs36(t, got)); age > 24*3600*1000 {
		t.Errorf("fallback session = %q decodes to %dms old, want <24h", got, age)
	}
}

// TestHexDownstreamSessionResynthed: an all-hex downstream session can never
// come from a genuine client, so it must be re-synthesized, never forwarded.
func TestHexDownstreamSessionResynthed(t *testing.T) {
	up, _ := http.NewRequest(http.MethodPost, "http://x/v1/responses", nil)
	down := http.Header{}
	down.Set("x-opencode-session", "ses_bf7555312fd072af4c7bbd197a")
	setZenSessionHeaders(up, down, "sticky-key-abc")
	got := up.Header.Get("x-opencode-session")
	if got == "ses_bf7555312fd072af4c7bbd197a" {
		t.Fatalf("all-hex downstream session was forwarded; Zen rejects it")
	}
	if !canonicalSessionPattern.MatchString(got) || hexOnlySessionPattern.MatchString(got) {
		t.Fatalf("resynthed session = %q, want genuine descending shape", got)
	}
}

// TestStaleDownstreamSessionResynthed: a genuine-shaped but >24h-old
// downstream session is re-synthesized (fresh timestamp, same stickiness)
// instead of risking a FreeTierError on Zen's recency check.
func TestStaleDownstreamSessionResynthed(t *testing.T) {
	stale := "ses_" + descendingSessionPrefix(time.Now().UnixMilli()-25*3600*1000, 3) + "abcdefghijklmn"
	if isForwardableSession(stale, time.Now().UnixMilli()) {
		t.Fatalf("stale session %q considered forwardable", stale)
	}
	up, _ := http.NewRequest(http.MethodPost, "http://x/v1/responses", nil)
	down := http.Header{}
	down.Set("x-opencode-session", stale)
	setZenSessionHeaders(up, down, "sticky-key-abc")
	got := up.Header.Get("x-opencode-session")
	if got == stale {
		t.Fatalf("stale downstream session was forwarded; refresh it")
	}
	if !isForwardableSession(got, time.Now().UnixMilli()) {
		t.Fatalf("resynthed session = %q, want fresh genuine shape", got)
	}
}

// TestSessionPrefixAgeDecodes: the age helper round-trips the construction.
func TestSessionPrefixAgeDecodes(t *testing.T) {
	now := time.Now().UnixMilli()
	fresh := descendingSessionPrefix(now, 5)
	age, ok := sessionPrefixAgeMs(fresh, now)
	if !ok || age > 60*1000 {
		t.Errorf("fresh prefix age = %d,%v, want <1min", age, ok)
	}
	old := descendingSessionPrefix(now-25*3600*1000, 5)
	age, ok = sessionPrefixAgeMs(old, now)
	if !ok || age < 24*3600*1000 || age > 26*3600*1000 {
		t.Errorf("25h-old prefix age = %d,%v, want ~25h", age, ok)
	}
	if _, ok := sessionPrefixAgeMs("zzzzzzzzzzzz", now); ok {
		t.Errorf("non-hex prefix decoded without error")
	}
}

func TestSetZenSessionHeadersOmitsUnknownSession(t *testing.T) {
	// Since 2026-09 Zen hard-rejects free-tier requests without
	// x-opencode-session (MissingSessionID), so the header is never
	// omitted: with no sticky key a process-stable-tail fallback id
	// is used instead (stable tail, so provider pinning stays coherent).
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
	// Fresh genuine-shaped downstream session: forwarded untouched.
	fresh := "ses_" + descendingSessionPrefix(time.Now().UnixMilli(), 3) + "abcdefghijklmn"
	req.Header.Set("x-opencode-session", fresh)
	rec := httptest.NewRecorder()
	srv.ResponsesProxy().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotSession != fresh {
		t.Errorf("upstream session = %q, want forwarded downstream value %q", gotSession, fresh)
	}
	if gotClient == "" || gotRequest != "" {
		t.Errorf("upstream client=%q request=%q, want client set and obsolete request absent", gotClient, gotRequest)
	}
}

func TestSetZenSessionHeadersPreservesChildAndParent(t *testing.T) {
	fresh := "ses_" + descendingSessionPrefix(time.Now().UnixMilli(), 1) + "abcdefghijklmn"
	child := "ses_" + descendingSessionPrefix(time.Now().UnixMilli(), 2) + "ABCDEFGHIJKLMN"
	down := http.Header{}
	down.Set("x-opencode-session", fresh)
	down.Set("x-opencode-session-id", child)
	down.Set("x-opencode-parent-session-id", fresh)
	down.Set("x-parent-session-id", fresh)
	down.Set("x-opencode-request", "msg_obsolete")
	up, _ := http.NewRequest(http.MethodPost, "http://x", nil)
	up.Header.Set("x-opencode-request", "msg_obsolete")
	setZenSessionHeaders(up, down, "sticky")
	for name, want := range map[string]string{
		"x-opencode-session": fresh, "x-opencode-session-id": child,
		"x-session-affinity": fresh, "x-session-id": fresh,
		"x-opencode-parent-session-id": fresh, "x-parent-session-id": fresh,
		"x-opencode-request": "",
	} {
		if got := up.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
