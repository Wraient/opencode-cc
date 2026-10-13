package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Kiowx/opencode-cc/internal/config"
)

func TestParseRetryAfterSeconds(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		in   string
		want int
		ok   bool
	}{
		{"delta seconds", "52638", 52638, true},
		{"delta with spaces", "  120 ", 120, true},
		{"zero", "0", 0, true},
		{"http date", now.Add(90 * time.Second).Format(http.TimeFormat), 90, true},
		{"http date in past", now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		{"empty", "", 0, false},
		{"garbage", "soon", 0, false},
		{"negative", "-5", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRetryAfterSeconds(tc.in, now)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got %d)", ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestUpstreamCooldownSecondsPrefersRetryAfter(t *testing.T) {
	now := time.Now()
	h := http.Header{}
	h.Set("Retry-After", "3600")
	secs, ok := upstreamCooldownSeconds(h, now)
	if !ok || secs != 3600 {
		t.Fatalf("got %d ok=%v, want 3600/true", secs, ok)
	}
}

func TestUpstreamCooldownSecondsFallsBackToReset(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	h := http.Header{}
	// Absolute epoch form.
	h.Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10))
	secs, ok := upstreamCooldownSeconds(h, now)
	if !ok || secs != 7200 {
		t.Fatalf("epoch reset: got %d ok=%v, want 7200/true", secs, ok)
	}
	// HTTP-date form.
	h2 := http.Header{}
	h2.Set("X-RateLimit-Reset", now.Add(30*time.Minute).Format(http.TimeFormat))
	secs, ok = upstreamCooldownSeconds(h2, now)
	if !ok || secs != 1800 {
		t.Fatalf("date reset: got %d ok=%v, want 1800/true", secs, ok)
	}
}

func TestUpstreamCooldownSecondsAbsent(t *testing.T) {
	if _, ok := upstreamCooldownSeconds(http.Header{}, time.Now()); ok {
		t.Fatal("empty headers should yield no cooldown")
	}
}

func TestClampCooldownBounds(t *testing.T) {
	if got := clampCooldown(0); got != maxCooldownFloor {
		t.Fatalf("zero should clamp to floor, got %v", got)
	}
	if got := clampCooldown(time.Second); got != maxCooldownFloor {
		t.Fatalf("1s should clamp to floor, got %v", got)
	}
	if got := clampCooldown(48 * time.Hour); got != maxCooldownCeiling {
		t.Fatalf("48h should clamp to ceiling, got %v", got)
	}
	if got := clampCooldown(5 * time.Minute); got != 5*time.Minute {
		t.Fatalf("5m should pass through, got %v", got)
	}
}

// The 14.6h value Zen actually sends must survive the round trip; clamping
// exists to defend against absurd values, not to truncate the real signal.
func TestClampCooldownPreservesZenDailyWindow(t *testing.T) {
	const zenRetryAfter = 52638
	d := clampCooldown(time.Duration(zenRetryAfter) * time.Second)
	if int(d.Seconds()) != zenRetryAfter {
		t.Fatalf("Zen daily window mangled: got %ds want %ds", int(d.Seconds()), zenRetryAfter)
	}
}

func TestCooldownTrackerEscalatesProgressively(t *testing.T) {
	tr := newCooldownTracker()
	want := []time.Duration{
		30 * time.Second,
		2 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
		15 * time.Minute,
		15 * time.Minute, // capped at the last step
	}
	for i, w := range want {
		if got := tr.nextFallback("m"); got != w {
			t.Fatalf("step %d: got %v, want %v", i, got, w)
		}
	}
}

func TestCooldownTrackerSeparateScopes(t *testing.T) {
	tr := newCooldownTracker()
	tr.nextFallback("model-a")
	if got := tr.nextFallback("model-b"); got != cooldownFallbacks[0] {
		t.Fatalf("independent scope should start at first step, got %v", got)
	}
}

func TestCooldownTrackerSuccessResetsLadder(t *testing.T) {
	tr := newCooldownTracker()
	tr.nextFallback("m")
	tr.nextFallback("m")
	tr.noteSuccess("m")
	if got := tr.nextFallback("m"); got != cooldownFallbacks[0] {
		t.Fatalf("after success the ladder should restart, got %v", got)
	}
}

// A long quiet gap means the lane recovered, so escalation restarts even
// without an explicit noteSuccess.
func TestCooldownTrackerQuietGapResetsLadder(t *testing.T) {
	tr := newCooldownTracker()
	base := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	now := base
	tr.now = func() time.Time { return now }

	tr.nextFallback("m")
	tr.nextFallback("m") // escalated
	now = base.Add(time.Hour)
	if got := tr.nextFallback("m"); got != cooldownFallbacks[0] {
		t.Fatalf("after a quiet hour the ladder should restart, got %v", got)
	}
}

func TestApplyCooldownHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	applyCooldownHeaders(rec, 52638*time.Second, 100, 0)

	if got := rec.Header().Get("Retry-After"); got != "52638" {
		t.Fatalf("Retry-After = %q, want 52638", got)
	}
	if got := rec.Header().Get("X-RateLimit-Limit"); got != "100" {
		t.Fatalf("X-RateLimit-Limit = %q, want 100", got)
	}
	reset, err := strconv.ParseInt(rec.Header().Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		t.Fatalf("X-RateLimit-Reset not numeric: %v", err)
	}
	// The epoch form must agree with Retry-After, so a client reading either
	// header waits the same amount of time.
	secs, _ := strconv.Atoi(rec.Header().Get("Retry-After"))
	delta := int64(secs) - int64(time.Until(time.Unix(reset, 0)).Seconds())
	if delta < -2 || delta > 2 {
		t.Fatalf("reset epoch (%.0fs) disagrees with Retry-After (%ds)", time.Until(time.Unix(reset, 0)).Seconds(), secs)
	}
}

func TestApplyCooldownHeadersNeverEmitsZero(t *testing.T) {
	rec := httptest.NewRecorder()
	applyCooldownHeaders(rec, 0, 0, 0)
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want at least 1", got)
	}
}

func TestStampCooldownUsesUpstreamRetryAfter(t *testing.T) {
	s := &Server{cooldowns: newCooldownTracker()}
	rec := httptest.NewRecorder()
	up := http.Header{}
	up.Set("Retry-After", "600")

	s.stampCooldown(rec, up, "space-bunny-free")

	if got := rec.Header().Get("Retry-After"); got != "600" {
		t.Fatalf("Retry-After = %q, want 600 (upstream value)", got)
	}
}

func TestStampCooldownSynthesisesFallbackWhenHeaderMissing(t *testing.T) {
	s := &Server{cooldowns: newCooldownTracker()}
	rec := httptest.NewRecorder()

	s.stampCooldown(rec, http.Header{}, "m")

	got := rec.Header().Get("Retry-After")
	if got == "" {
		t.Fatal("a header-less 429 must still carry a cooldown")
	}
	secs, err := strconv.Atoi(got)
	if err != nil {
		t.Fatalf("Retry-After not numeric: %q", got)
	}
	if secs < int(maxCooldownFloor.Seconds()) {
		t.Fatalf("synthesised cooldown %ds below floor", secs)
	}
}

// The core of the bug: an upstream 429 must reach the client with timing
// information instead of a bare status code.
func TestPassUpstreamError429CarriesRetryAfter(t *testing.T) {
	s, _ := newTestServerWithCfg(t, config.Default())
	rec := httptest.NewRecorder()
	up := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"900"}},
		Body:       http.NoBody,
	}

	s.passUpstreamError(rec, resp, up, "m", "space-bunny-free", []byte("{}"), time.Now())

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "900" {
		t.Fatalf("Retry-After = %q, want 900", got)
	}
}

func TestPassUpstreamError429WithoutUpstreamHeader(t *testing.T) {
	s, _ := newTestServerWithCfg(t, config.Default())
	rec := httptest.NewRecorder()
	up := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{},
		Body:       http.NoBody,
	}

	s.passUpstreamError(rec, resp, up, "m", "m", []byte("{}"), time.Now())

	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("header-less upstream 429 must still get a synthesised cooldown")
	}
}

// Non-429 errors must not claim a cooldown.
func TestPassUpstreamErrorNon429HasNoRetryAfter(t *testing.T) {
	s, _ := newTestServerWithCfg(t, config.Default())
	rec := httptest.NewRecorder()
	up := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{},
		Body:       http.NoBody,
	}

	s.passUpstreamError(rec, resp, up, "m", "m", []byte("{}"), time.Now())

	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Fatalf("400 should not carry Retry-After, got %q", got)
	}
}
