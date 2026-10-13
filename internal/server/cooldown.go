package server

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rate-limit / cooldown propagation.
//
// Zen signals exhaustion with `retry-after` on its 429 (observed: 52638s,
// i.e. the remainder of the free-tier daily window). This proxy used to drop
// that header on the floor, so every downstream hop — cliproxyapi, then the
// user's harness — saw a bare `429` with no timing information. Each hop then
// invented its own short backoff, retried into the same wall, and the whole
// chain stalled even though the cooldown was known and long.
//
// This file makes the signal survive the hop:
//
//	upstream 429 -> opencode-cc re-emits Retry-After (+ anthropic-beta style
//	             hints) -> cliproxyapi reads it and cools the credential down
//	             for that long -> the harness sees Retry-After and backs off.
//
// When an upstream gives no usable value, a floor is used instead of an
// invented instant retry, so a missing header can never turn into a hot loop.

// Cooldown header names. Retry-After is the standard and the one cliproxyapi
// and OpenAI/Anthropic SDKs both read.
const (
	headerRetryAfter         = "Retry-After"
	headerRateLimitReset     = "X-RateLimit-Reset"
	headerRateLimitRemaining = "X-RateLimit-Remaining"
	headerRateLimitLimit     = "X-RateLimit-Limit"
)

// cooldownFallbacks are the progressive backoffs used when an upstream sends a
// 429 with no usable Retry-After. They grow so a genuinely exhausted lane
// eventually stops being hammered, while a transient blip still recovers fast.
var cooldownFallbacks = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
	15 * time.Minute,
}

const (
	// maxCooldownCeiling caps any propagated value. A hostile or buggy upstream
	// must not park a client for days on a single header; 24h matches Zen's own
	// daily free-tier window, so the real signal still gets through intact.
	maxCooldownCeiling = 24 * time.Hour
	// maxCooldownFloor keeps a sub-second value from becoming a hot retry loop.
	maxCooldownFloor = 5 * time.Second
)

// cooldownTracker hands out the progressive fallback durations. It is
// per-server (not per-model) because the limit that matters is the upstream
// account's, which is shared by every model behind one key.
type cooldownTracker struct {
	mu   sync.Mutex
	seen map[string]cooldownState
	now  func() time.Time
}

// cooldownState is the escalation position for one scope.
type cooldownState struct {
	at   time.Time
	step int // index into cooldownFallbacks
}

func newCooldownTracker() *cooldownTracker {
	return &cooldownTracker{seen: map[string]cooldownState{}, now: time.Now}
}

// nextFallback returns the escalation step for scope. Back-to-back 429s walk
// up the ladder; a quiet gap at least as long as the step just handed out
// means the lane recovered, so the ladder restarts.
func (c *cooldownTracker) nextFallback(scope string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now == nil {
		c.now = time.Now
	}
	now := c.now()

	step := 0
	if prev, ok := c.seen[scope]; ok {
		if now.Sub(prev.at) >= cooldownFallbacks[prev.step] {
			step = 0 // recovered during the quiet gap
		} else {
			step = prev.step + 1
		}
	}
	if step >= len(cooldownFallbacks) {
		step = len(cooldownFallbacks) - 1
	}
	c.seen[scope] = cooldownState{at: now, step: step}
	return cooldownFallbacks[step]
}

// noteSuccess clears the escalation for scope once the lane answers again.
func (c *cooldownTracker) noteSuccess(scope string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.seen, scope)
}

// parseRetryAfterSeconds reads a Retry-After value, accepting both forms from
// the RFC 7231 spec: delta-seconds and an HTTP-date.
func parseRetryAfterSeconds(v string, now time.Time) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return secs, true
	}
	if t, err := http.ParseTime(v); err == nil {
		secs := int(t.Sub(now).Round(time.Second) / time.Second)
		if secs < 0 {
			secs = 0
		}
		return secs, true
	}
	return 0, false
}

// upstreamCooldownSeconds extracts how long to wait from an upstream 429,
// preferring the authoritative Retry-After and falling back to the
// x-ratelimit-reset family. Returns 0 when the upstream gave nothing usable.
func upstreamCooldownSeconds(h http.Header, now time.Time) (int, bool) {
	if secs, ok := parseRetryAfterSeconds(h.Get(headerRetryAfter), now); ok {
		return secs, true
	}
	// Some gateways expose an absolute reset instant instead of a delta.
	if reset := strings.TrimSpace(h.Get(headerRateLimitReset)); reset != "" {
		if secs, ok := parseRetryAfterSeconds(reset, now); ok {
			// A bare number here is an epoch second, not a delta.
			if secs > 1_000_000_000 {
				return secs - int(now.Unix()), true
			}
			return secs, true
		}
		if t, err := http.ParseTime(reset); err == nil {
			secs := int(t.Sub(now).Round(time.Second) / time.Second)
			if secs < 0 {
				secs = 0
			}
			return secs, true
		}
	}
	return 0, false
}

// clampCooldown keeps a value inside the floor/ceiling bounds.
func clampCooldown(d time.Duration) time.Duration {
	if d < maxCooldownFloor {
		return maxCooldownFloor
	}
	if d > maxCooldownCeiling {
		return maxCooldownCeiling
	}
	return d
}

// applyCooldownHeaders stamps the downstream response with the cooldown so the
// next hop can honour it. retryAfter is the already-resolved duration.
func applyCooldownHeaders(w http.ResponseWriter, retryAfter time.Duration, limit, remaining int) {
	secs := int(retryAfter.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set(headerRetryAfter, strconv.Itoa(secs))
	if limit > 0 {
		w.Header().Set(headerRateLimitLimit, strconv.Itoa(limit))
	}
	if remaining >= 0 {
		w.Header().Set(headerRateLimitRemaining, strconv.Itoa(remaining))
	}
	// Epoch form, for clients that read the reset family instead of Retry-After.
	w.Header().Set(headerRateLimitReset, strconv.FormatInt(
		time.Now().Add(retryAfter).Unix(), 10))
}
