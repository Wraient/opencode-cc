package server

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// x-opencode-* identity headers, mirroring what the official opencode client
// sends on every Zen LLM request (see opencode session/llm.ts):
//
//	x-opencode-project  project id
//	x-opencode-session  session id (STICKY ROUTING + prompt-cache scope)
//	x-opencode-request  per-request id
//	x-opencode-client   client name
//
// The session header is load-bearing: Zen uses it as the sticky routing id
// for provider pinning and per-session cache scoping (falling back to the
// coarse workspace/IP when absent), and requests missing it may error after
// 2026-09-06. Zen strips these headers before forwarding to providers, so
// forwarding client values is safe and intended.
const (
	zenHeaderProject = "x-opencode-project"
	zenHeaderSession = "x-opencode-session"
	zenHeaderRequest = "x-opencode-request"
	zenHeaderClient  = "x-opencode-client"

	// zenClientID identifies this proxy when the downstream client did not
	// supply its own x-opencode-client.
	zenClientID = "opencode-cc"
)

// setZenSessionHeaders sets the x-opencode-* identity headers on an upstream
// Zen request.
//
// Precedence per header: forward the downstream client's value when present
// (a real opencode harness already sends its true session/project/request
// ids through us) and only then synthesize:
//   - session: the route's sticky key (stable per client session in
//     practice). Omitted when unknown — Zen then falls back to
//     workspace/IP, which is strictly better than a random per-request id
//     that would defeat stickiness.
//   - request: a fresh UUID per upstream call.
//   - project: never fabricated, omitted when unknown.
//   - client: this proxy's id when the client sent none.
func setZenSessionHeaders(upReq *http.Request, down http.Header, stickyKey string) {
	if upReq == nil {
		return
	}
	if v := strings.TrimSpace(down.Get(zenHeaderSession)); v != "" {
		upReq.Header.Set(zenHeaderSession, v)
	} else if v := strings.TrimSpace(stickyKey); v != "" {
		upReq.Header.Set(zenHeaderSession, v)
	}
	if v := strings.TrimSpace(down.Get(zenHeaderRequest)); v != "" {
		upReq.Header.Set(zenHeaderRequest, v)
	} else {
		upReq.Header.Set(zenHeaderRequest, uuid.NewString())
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
