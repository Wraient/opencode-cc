# Claude Code + DeepSeek v4.1 Flash OC acceptance

The requested target is Claude Code (`claudeAgent_2`) with
`deepseek-v4.1-flash-oc`, effort `max`, through opencode-cc. Switching to
Codex/GPT is not an acceptance result for this target.

Fresh T3 thread: `mcp:80645101-0fb3-4d30-86b5-d5bce7788ab2`.
Workspace: `/home/wraient/.t3/scratch/2026-10-20-can-you-use-the-t3code-e7788ab2`.
Its first message is verbatim:

> can you use the t3code visualization to create something really complex and impress me? like stock heat map or really complex scene idk

Additional steering requires schema-aware tool calls, desktop/mobile and
interaction verification, simulated-data labeling, and self-contained
html_preview/html_render publication. No prior artifact was copied.

User clarification: the repair scope is the connection chain, not DeepSeek
model code or generated-application polishing. The visualization is only the
end-to-end workload. Further steering stopped application polishing and asked
for publication through the documented native tools.

## Verified live chain

- The new workspace's actual `claude` process has ANTHROPIC_BASE_URL set to
  `http://127.0.0.1:8317`; auth presence was checked without printing it.
- Live sockets show that Claude process connected to 8317, `cli-proxy-api`
  connected to 8787, and `opencode-cc` connected to WARP on 40000.
- Production request rows show `/v1/messages` arriving as
  `deepseek-v4.1-flash-free`, targeting the same Zen model. T3's alias remains
  `deepseek-v4.1-flash-oc` on `claudeAgent_2`, effort max.
- Context compaction occurred in this run and a subsequent native Read call
  completed successfully, exercising the original failing resume boundary
  without changing harness or model.

## Additional relay cause found

Direct upstream probes captured HTTP 503 JSON:

`{"error":{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}}`

Identical payloads and credentials repeatedly returned 200 or 503 depending
on the Zen session-affinity value. The original diagnostic affinity succeeded
both with and without prompt-cache fields. Alternative affinities 2 and 5
failed; 3 and 4 succeeded. Removing cache fields and changing network path
did not by themselves resolve the failure. The old retry resent the same
unavailable affinity.

`doUpstreamWithRetry` now recognizes only this exact 503 on HTTPS opencode.ai
requests with a model and session. It tries at most three alternative
affinities and retains a successful one for later tool turns. The bounded
in-memory cache is scoped by a hash of credential, origin, model, and original
session. It stores no bearer tokens and expires entries after one hour.
Request body and credentials are unchanged; a distinct actual child session
is preserved. 401/403/429, other origins, and unrelated errors retain their
prior behavior. Cancellation stops retry backoff promptly.

## Validation and deployment

- `go build ./...`, `go test ./...`, staging binary build, and diff checks passed.
- Regression tests cover successful recovery and stable follow-up, unchanged
  payload/credentials, cache isolation, attempt bound, auth/quota boundaries,
  other origins/errors, child identity, and cancellation.
- Real staging at 18789 passed chat, uncached history, and a fresh tool loop
  with thinking removed. Logs show unavailable endpoints recovered after one
  to three alternate affinities.
- Atomically deployed only after staging passed. Rollback binary:
  `~/.local/bin/opencode-cc.bak-affinity-20261021`.
- Live chat and fresh tool loop passed. One deliberately short uncached-history
  probe returned HTTP 200 with max_tokens at 256 tokens; this is not recorded
  as a completed semantic answer.
- Staging stopped and its private saved configuration removed. Morph was not
  restarted; WARP local proxy mode and automatic protocol fallback were kept.

## Tool failures corrected during the acceptance run

- Initial Write arguments were malformed; Claude Code rejected them safely.
  DeepSeek retried with valid Write/Edit calls and built the new artifact.
- A duplicate closing script tag caused syntax validation to fail; DeepSeek
  repaired it and the repeated Node syntax check passed.
- Port 8765 was occupied by the previous thread's server; DeepSeek read the
  failure and served its own workspace on 8766, then navigated successfully.
- A 1280x1758 screenshot was initially interpreted as stale despite a
  728x1000 CSS viewport. Those dimensions match DPR-2 capture downscaling;
  DOM bounds confirmed the viewport and subsequent checks used actual
  controls and snapshot locators.

## Final acceptance result

The fresh thread completed its first run successfully, entirely on
`claudeAgent_2` / `deepseek-v4.1-flash-oc` with effort max. The final thread
configuration was checked again after completion; it was never switched.

Native html_preview at 728 CSS pixels returned contentHeight 1521. Native
html_render accepted the exact same 86,054-character self-contained HTML
string (the two tool inputs were compared for equality), publishing attachment:
`mcp-80645101-0fb3-4d30-86b5-d5bce7788ab2-d4a70724-73b4-4384-a14d-f570b26bd328-html`.
The corresponding production calls returned HTTP 200, including the two
large streamed tool arguments (about 71 and 68 seconds respectively).

Successful native calls exercised discovery, skills, Bash, Write/Edit, Read,
browser snapshots, clicks, typing, key presses, hover, resizing, evaluation,
and final preview/render publication. Initial generation/port/syntax mistakes
were corrected and repeated successfully rather than counted as successes.
Context compaction and later tool calls completed without the original 400.

The workload also verified search, selection, pause/resume, reset, color modes,
table mouse/keyboard selection, tooltip click-through, and a 390-pixel mobile
layout with 96 tiles and no page or label overflow. These are workload evidence,
not an expansion of the connection-repair scope.

Final scope remains Claude Code → CLIProxyAPI → opencode-cc → OpenCode Zen.
No DeepSeek model code, gateway auth state, or OpenCode credential state changed.
