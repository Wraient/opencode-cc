package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// x-opencode-* identity headers, byte-identical to what the official opencode
// client sends on every Zen LLM request (see opencode session/llm.ts):
//
//	x-opencode-project  hex project id
//	x-opencode-session  ses_<26 alnum> session id (STICKY ROUTING + cache scope)
//	x-opencode-request  msg_<26 alnum> per-request id
//	x-opencode-client   client name ("cli" for stock terminal use,
//	                    "acp" for ACP mode; OPENCODE_CLIENT env overrides)
//
// The session header is load-bearing: Zen uses it as the sticky routing id
// for provider pinning and per-session cache scoping (falling back to the
// coarse workspace/IP when absent), and requests missing it may error after
// 2026-09-06. Zen strips these headers before forwarding to providers, so
// forwarding client values is safe and intended.
//
// This proxy must be indistinguishable from stock opencode upstream: it never
// sends any opencode-cc fingerprint in these headers.
const (
	zenHeaderProject = "x-opencode-project"
	zenHeaderSession = "x-opencode-session"
	zenHeaderRequest = "x-opencode-request"
	zenHeaderClient  = "x-opencode-client"

	// zenClientID is the exact stock-terminal value (Flag.OPENCODE_CLIENT
	// defaults to "cli"), used when the downstream client sent none.
	zenClientID = "cli"
)

// setZenSessionHeaders sets the x-opencode-* identity headers on an upstream
// Zen request.
//
// Precedence per header: forward the downstream client's value when present
// (a real opencode harness already sends its true ses_/msg_ ids through us)
// and only then synthesize in the identical format:
//   - session: ses_ + hash of the route's sticky key (stable per client
//     session across turns AND proxy restarts, so provider pins survive).
//     Omitted when no sticky key exists — Zen then falls back to
//     workspace/IP, which is strictly better than a random per-request id
//     that would defeat stickiness.
//   - request: msg_ + 26 random alphanumerics, one per upstream call.
//   - project: never fabricated, omitted when unknown.
//   - client: stock "cli" when the client sent none.
func setZenSessionHeaders(upReq *http.Request, down http.Header, stickyKey string) {
	if upReq == nil {
		return
	}
	if v := strings.TrimSpace(down.Get(zenHeaderSession)); v != "" {
		upReq.Header.Set(zenHeaderSession, v)
	} else if v := synthSessionID(stickyKey); v != "" {
		upReq.Header.Set(zenHeaderSession, v)
	}
	if v := strings.TrimSpace(down.Get(zenHeaderRequest)); v != "" {
		upReq.Header.Set(zenHeaderRequest, v)
	} else {
		upReq.Header.Set(zenHeaderRequest, synthRequestID())
	}
	if v := strings.TrimSpace(down.Get(zenHeaderProject)); v != "" {
		upReq.Header.Set(zenHeaderProject, v)
	}
	if v := strings.TrimSpace(down.Get(zenHeaderClient)); v != "" {
		upReq.Header.Set(zenHeaderClient, v)
	} else {
		upReq.Header.Set(zenHeaderClient, zenClientID)
	}
}

// synthSessionID derives a stock-formatted session id (ses_ + 26 hex chars)
// deterministically from the sticky key.
func synthSessionID(stickyKey string) string {
	if strings.TrimSpace(stickyKey) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(stickyKey))
	return "ses_" + hex.EncodeToString(sum[:])[:26]
}

const synthIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// synthRequestID returns a stock-formatted request id (msg_ + 26 alphanumerics).
func synthRequestID() string {
	var b [26]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a zero id rather than failing the request.
		return "msg_00000000000000000000000000"
	}
	for i, v := range b {
		b[i] = synthIDAlphabet[int(v)%len(synthIDAlphabet)]
	}
	return "msg_" + string(b[:])
}
