package server

import (
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Kiowx/opencode-cc/internal/config"
)

// keepAliveInterval is how often each configured upstream is pinged to hold
// its pooled connection open. It must stay comfortably below the transport's
// IdleConnTimeout (30m) and above the upstream's own idle-connection reap
// time (measured warm at 60s+), so the pool never empties between real turns.
const keepAliveInterval = 45 * time.Second

// keepAliveTimeout bounds a single ping. A slow ping must never pile up on the
// shared transport or delay real traffic, so it gets its own short budget and
// its own context (it is NOT tied to any request).
const keepAliveTimeout = 8 * time.Second

// upstreamIdleTimeout is the shared transport's IdleConnTimeout. It must stay
// well above keepAliveInterval so pooled sockets survive between pings, and
// above any plausible human pause between turns. Paired measurement: sockets
// stayed reusable at 60s idle and cost ~610ms more once dropped, so this is
// deliberately long. Bounded so a genuinely dead pool still eventually recycles.
const upstreamIdleTimeout = time.Hour

// keepWarmUpstreams periodically issues one cheap GET /v1/models per enabled
// upstream through the shared transport.
//
// Why this exists: opencode-cc is used interactively, so upstream idleness of
// minutes is the norm (the user reads output, thinks, switches windows). Once
// the pool goes cold the next turn pays a fresh TCP+TLS handshake to the Cloud
// edge, measured at ~610ms of added time-to-first-byte - more than the model's
// own first-token latency on small prompts. The ping is ~200 bytes on an
// already-warm socket and returns as soon as the edge answers, so it keeps the
// first real request of a turn on the fast path.
//
// Design constraints:
//   - It reuses s.httpClient, so it warms the exact pool real traffic uses.
//   - Failures are ignored: a ping that errors means the edge is unreachable,
//     which the real request will report far more usefully. Logging would be
//     pure noise during a genuine outage (health checks already exist).
//   - It never touches the upstream slot semaphores, so pinging cannot
//     serialize or delay real requests.
//   - One ping per upstream per interval, so a multi-upstream pool stays warm.
func (s *Server) keepWarmUpstreams() {
	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.pingUpstreams()
		}
	}
}

// pingUpstreams sends one keep-alive request per enabled upstream. It runs
// concurrently across upstreams so a slow one cannot delay the others.
func (s *Server) pingUpstreams() {
	if s.cfg == nil {
		return
	}
	pool := s.cfg.Snapshot().Upstreams
	var wg sync.WaitGroup
	for _, u := range pool {
		if !u.Enabled || strings.TrimSpace(u.BaseURL) == "" {
			continue
		}
		base := u.BaseURL
		key, err := config.ResolveUpstreamCredential(base, u.APIKey)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			pingUpstream(s.httpClient, base, key)
		}()
	}
	wg.Wait()
}

// pingUpstream performs a single best-effort warm-up GET. The response body is
// drained (up to a small cap) and the connection is returned to the idle pool,
// which is what actually keeps it reusable.
func pingUpstream(client *http.Client, base, key string) {
	ctx, cancel := context.WithTimeout(context.Background(), keepAliveTimeout)
	defer cancel()

	url := strings.TrimRight(base, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	// Same UA the proxy presents upstream: some edges gate even /v1/models on
	// the OpenCode UA shape.
	req.Header.Set("User-Agent", ocUA())

	resp, err := client.Do(req)
	if err != nil {
		return // expected during an upstream outage; the next real request reports it
	}
	// Drain a bounded prefix so the connection can go back to the idle pool.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	_ = resp.Body.Close()
}

// StartKeepAlive launches the connection keep-warm loop. It returns
// immediately; the loop stops when Shutdown closes s.done.
func (s *Server) StartKeepAlive() {
	if s.cfg == nil {
		return
	}
	s.keepAliveOnce.Do(func() {
		go s.keepWarmUpstreams()
		log.Printf("opencode-cc: upstream keep-alive every %s (idle conn timeout %s)",
			keepAliveInterval, upstreamIdleTimeout)
	})
}
