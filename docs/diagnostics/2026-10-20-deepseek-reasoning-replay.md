# DeepSeek tool follow-ups after compaction (2026-10-20)

T3 thread 8020e5dc-1124-4860-ab42-0f774eb51ab8 repeatedly failed with HTTP 400:
`reasoning_content` in thinking mode must be passed back.

The relay preserved nonempty thinking and cached reasoning by tool-call ID, but
serialized an empty reasoning string with omitempty. Real Claude session history
included assistant tool calls with no thinking block, including the final Read
before each failure. Cache expiry, process restarts and compacted history also
leave no recoverable reasoning for some assistant turns.

OpenAIRequest serialization now explicitly includes reasoning_content for every
assistant history message targeting DeepSeek. Existing reasoning is preserved;
unavailable reasoning is represented honestly as an empty string. Other models
retain their existing serialization. This covers the Messages and Responses
translation paths that produce OpenAIRequest; raw Chat Completions passthrough
is unchanged.

Validation:
- go build ./..., go test ./..., staging binary build: passed.
- Regression covers summary-only assistant history, an uncached tool call,
  nonempty thinking preservation, and unchanged GLM empty-field behavior.
- WARP Connected / healthy; proxied Cloudflare trace warp=on.
- Isolated staging at 127.0.0.1:18789 using the live upstream configuration:
  real DeepSeek chat, uncached tool history without thinking, and a fresh tool
  loop with thinking stripped all returned HTTP 200.
- Atomic production binary replacement and service restart; health returned OK.
  The same real chat/tool probes passed on port 8787.
- Upstream intermittently returned 503, including on simple chat. The existing
  retry recovered some requests; this is separate from the missing-field 400.
- The original T3 thread resumed on its original DeepSeek model and successfully
  performed multiple Read/browser tool follow-ups after deployment.

Deployment includes the existing uncommitted reasoning-effort mapping changes
already present in the workspace; those passed the full test suite as well.
Rollback binary: ~/.local/bin/opencode-cc.bak-reasoning-20261020.
Morph's running binary inode and port 18787 were left running.

## Availability follow-up, early 2026-10-21 IST

The resumed DeepSeek thread performed many successful tool turns with no
missing-reasoning 400, then repeatedly exhausted retries on 503 responses.
The local CLIProxyAPI gateway reported auth_unavailable while its sole route
cooled down after WARP SOCKS host-unreachable failures. WARP reported network
unstable; tunnel logs showed roughly 87% packet loss on HTTP/3.

Reconnected WARP and tested HTTP/2-only MASQUE temporarily. Cloudflare trace
and opencode.ai model-list connectivity recovered, and fresh production chat
returned 200, but tool inference still intermittently returned 503. An isolated
staging process with HTTP_PROXY/HTTPS_PROXY/ALL_PROXY removed also returned
200 for chat and 503 for the tool-history probe. Thus the remaining inference
503 cannot be attributed solely to WARP. Both staging processes were stopped
and their private saved configurations removed.

Restored the original automatic HTTP/3 with HTTP/2 fallback after diagnostics;
WarpProxy mode on 127.0.0.1:40000 and production's explicit proxy environment
remain intact. No gateway auth files or OpenCode credential state were changed.

To finish the user-requested visualization despite backend unavailability,
the existing T3 thread was switched from claudeAgent_2 / deepseek-v4.1-flash-oc
to codex / gpt-6.1-sol, reasoningEffort medium. No new thread/subagent was
created. The saved visualization was preserved and the finalization brief
included the already-verified label fix and concrete remaining interactions.

## Completion

T3 run ordinal 6 completed successfully on gpt-6.1-sol. It verified search,
selection, pause/resume, table view, color modes and a 390-pixel mobile layout
(scrollWidth 390, all 265 cells, clickable selection). It fixed an HTML
hidden-attribute/CSS conflict that left an empty-state overlay intercepting clicks.

html_preview succeeded, then html_render published the complete 79,369-character
self-contained document as Interactive Market Map, attachment:
8020e5dc-1124-4860-ab42-0f774eb51ab8-878207ae-ff93-42be-9914-cc8c1349cb6d-html.
The thread's final assistant result confirmed verification and simulated data.

Final checks: production service active; health OK; WARP Connected / healthy;
port 18789 unused; Morph still listening on 18787. git diff --check passed.
