package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"sync"
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
	zenHeaderProject   = "x-opencode-project"
	zenHeaderSession   = "x-opencode-session"
	zenHeaderRequest   = "x-opencode-request"
	zenHeaderClient    = "x-opencode-client"
	zenHeaderAffinity  = "x-session-affinity"
	zenHeaderSessionID = "x-session-id"

	// zenClientID is the exact stock-terminal value (Flag.OPENCODE_CLIENT
	// defaults to "cli"), used when the downstream client sent none.
	zenClientID = "cli"
)

var canonicalSessionPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// setZenSessionHeaders sets the x-opencode-* identity headers on an upstream
// Zen request. Downstream session IDs are accepted when they already have the
// current OpenCode shape; UUIDs and older ses_ formats are deterministically
// normalized so Claude Code, Grok Build, and OpenCode all reach the same
// sticky-routing lane.
func setZenSessionHeaders(upReq *http.Request, down http.Header, stickyKey string) {
	if upReq == nil {
		return
	}

	session := strings.TrimSpace(down.Get(zenHeaderSession))
	if session == "" {
		session = strings.TrimSpace(down.Get(zenHeaderAffinity))
	}
	if session == "" {
		session = strings.TrimSpace(down.Get(zenHeaderSessionID))
	}
	if session == "" {
		session = strings.TrimSpace(down.Get("x-claude-code-session-id"))
	}
	if !canonicalSessionPattern.MatchString(session) {
		session = synthSessionID(session + "|" + strings.TrimSpace(stickyKey))
	}
	if session == "" {
		session = fallbackSessionID()
	}
	upReq.Header.Set(zenHeaderSession, session)
	// The current OpenCode request path also sends these two affinity forms;
	// keeping them equal avoids a second identity path in Zen's router.
	upReq.Header.Set(zenHeaderAffinity, session)
	upReq.Header.Set(zenHeaderSessionID, session)

	request := strings.TrimSpace(down.Get(zenHeaderRequest))
	if request == "" || !strings.HasPrefix(request, "msg_") || len(request) != 30 {
		request = synthRequestID()
	}
	upReq.Header.Set(zenHeaderRequest, request)

	project := strings.TrimSpace(down.Get(zenHeaderProject))
	if project == "" {
		project = "global"
	}
	upReq.Header.Set(zenHeaderProject, project)

	client := strings.TrimSpace(down.Get(zenHeaderClient))
	if client == "" {
		client = zenClientID
	}
	upReq.Header.Set(zenHeaderClient, client)
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

// fallbackSessionID is the process-stable session id used when neither the
// downstream client nor the sticky key yields one. It follows Zen's current
// canonical shape: ses_ + 12 lowercase hex + 14 base62 characters.
var (
	fallbackSessionOnce sync.Once
	fallbackSessionVal  string
)

func fallbackSessionID() string {
	fallbackSessionOnce.Do(func() {
		var b [20]byte
		if _, err := rand.Read(b[:]); err != nil {
			fallbackSessionVal = "ses_00000000000000000000000000"
			return
		}
		var tail [14]byte
		for i := range tail {
			tail[i] = synthIDAlphabet[int(b[6+i])%len(synthIDAlphabet)]
		}
		fallbackSessionVal = "ses_" + hex.EncodeToString(b[:6]) + string(tail[:])
	})
	return fallbackSessionVal
}

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
