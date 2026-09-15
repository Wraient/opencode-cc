package server

// Transparent recovery for stale encrypted reasoning.
//
// When a client replays Responses history carrying encrypted-reasoning blobs
// issued to a different caller identity, upstream fails fast with HTTP 400
// "reasoning `encrypted_content` was not issued to this caller". The blobs
// carry no visible text, so instead of surfacing the error, the proxy strips
// them from the upstream request and retries once — the client receives the
// correct response. Used by every path that posts to upstream /v1/responses
// (Responses passthrough + Anthropic bridge), so all harnesses (claude, grok,
// codex, opencode) are covered without client changes.

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/Kiowx/opencode-cc/internal/proxy"
)

// recoveryMaxErrBody caps how much of an upstream error body is buffered for
// signature matching. Real 400 payloads are <1KB; beyond the cap the response
// is relayed untouched (never retried blind).
const recoveryMaxErrBody = 64 * 1024

// maybeRetryStaleReasoning inspects an upstream 400 for the stale-reasoning
// signature and, on a match, replays the request once with encrypted blobs
// stripped. It returns the replacement response and true when a retry was
// performed (the original resp is closed); otherwise the original resp with
// its body restored for the normal relays, and false.
func (s *Server) maybeRetryStaleReasoning(
	client *http.Client,
	template *http.Request,
	upBody []byte,
	resp *http.Response,
	incomingModel, targetModel string,
	stream bool,
	start time.Time,
) (*http.Response, bool) {
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		return resp, false
	}
	saved, err := io.ReadAll(io.LimitReader(resp.Body, recoveryMaxErrBody+1))
	_ = resp.Body.Close()
	if err != nil || len(saved) > recoveryMaxErrBody {
		// Unreadable or implausibly large: restore what we can and bail out.
		resp.Body = io.NopCloser(bytes.NewReader(saved))
		return resp, false
	}
	if !proxy.IsStaleReasoningError(resp.StatusCode, saved) {
		resp.Body = io.NopCloser(bytes.NewReader(saved))
		return resp, false
	}
	cleaned, stripped, err := proxy.StripStaleReasoningBlobs(upBody)
	if err != nil || stripped == 0 {
		// Nothing to fix (e.g. the poison lives server-side behind a
		// previous_response_id chain): relay the original error.
		resp.Body = io.NopCloser(bytes.NewReader(saved))
		return resp, false
	}
	retryReq := template.Clone(template.Context())
	retryReq.Body = io.NopCloser(bytes.NewReader(cleaned))
	retryReq.ContentLength = int64(len(cleaned))
	log.Printf("opencode-cc: stale-reasoning retry model=%s target=%s stream=%v stripped=%d",
		incomingModel, targetModel, stream, stripped)
	newResp, err := doUpstreamWithRetry(client, retryReq, cleaned)
	if err != nil {
		log.Printf("opencode-cc: stale-reasoning retry failed model=%s: %v", incomingModel, err)
		resp.Body = io.NopCloser(bytes.NewReader(saved))
		return resp, false
	}
	log.Printf("opencode-cc: stale-reasoning retry model=%s -> upstream %s after=%s",
		incomingModel, newResp.Status, time.Since(start).Round(time.Millisecond))
	return newResp, true
}
