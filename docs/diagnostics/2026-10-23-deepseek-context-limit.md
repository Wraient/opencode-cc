# DeepSeek V4.1 Flash Free: effective context limit through opencode-cc

Date: 2026-10-23. Model: `deepseek-v4.1-flash-free` (OpenCode Zen free route).
Question: is the window 1M, 200K, or something else, and does quality or
availability change past 200K?

## Answer

| Question | Result |
|---|---|
| Is the window 1M? | Not on the tested free route. Largest accepted single request was 260,872 input tokens. Requests at about 280K, 285K, 290K, and 950K were all rejected. |
| Is the window 200K? | No. The upstream accepts about 30% more than 200K. The 200K figure is OpenCode's models.dev metadata (`limit.context: 200000`), not an enforced upstream cap. |
| Does quality drop past 200K? | Not observed. Five-needle recall was 5/5, in order, at every accepted size from 8.7K to 260.9K. |
| Is there evidence of model switching past 200K? | No observed retrieval loss or fallback error; the actual serving model identity is not independently verifiable. |
| What happens above the ceiling? | HTTP 400 in 1 to 4 s, with no prefill. On chat/completions it is `context_length_exceeded`; on `/v1/messages` it is a generic `api_error` with the same message. Not a degraded answer. |

Sources for the advertised windows: DeepSeek V4 announcement (1M standard,
https://api-docs.deepseek.com/news/news260424/) and the V4 Flash model card
(https://huggingface.co/deepseek-ai/DeepSeek-V4-Flash, 1M). OpenCode's
client catalog (models.dev, quoted by https://pi.dev/models/opencode/deepseek-v4-1-flash-free)
lists the free model at `context: 200000`, `output: 32000`, and the paid
`deepseek-v4.1-flash` at 1M.

## Results

Staging relay (installed binary, production config, WARP egress), five-needle
recall at depths 1, 25, 50, 75, 99%. Sorted by size. Raw rows, without prompts
or codes: [evidence/2026-10-23-deepseek-context-probes.json](evidence/2026-10-23-deepseek-context-probes.json).

| Probe | Route | Input tokens | HTTP | Cache read | Recall | Headers (s) |
|---|---|---:|---:|---:|---:|---:|
| p0-cal-32k | /v1/messages | 8,690 | 200 | 0 | 5/5 | 2.4 |
| p1-190k | /v1/messages | 188,480 | 200 | 0 | 5/5 | 7.6 |
| p2-230k | /v1/messages | 230,135 | 200 | 0 | 5/5 | 10.0 |
| b1-250k | /v1/messages | 249,847 | 200 | 0 | 5/5 | 8.9 |
| b3-255k | /v1/messages | 255,558 | 200 | 2,432 | 5/5 | 9.0 |
| b4-256p1k | /v1/messages | 256,202 | 200 | 64,000 | 5/5 | 12.8 |
| o2-openai-route-256k | /v1/chat/completions | 256,244 | 200 | - | 5/5 | - |
| b5-258p6k | /v1/messages | 258,717 | 200 | 2,048 | 5/5 | 9.3 |
| b6-260k | /v1/messages | 260,096 | 200 | 2,560 | 5/5 | 9.7 |
| b7-260k-mt32000 | /v1/messages, `max_tokens` 32000 | 260,188 | 200 | 130,048 | 5/5 | 7.8 |
| b8-260p6k | /v1/messages | 260,689 | 200 | 65,152 | 5/5 | 8.1 |
| b9-260p9k | /v1/messages | 260,872 | 200 | 65,280 | 5/5 | 8.1 |
| b2-261k | /v1/messages | ~261,031 (est.) | 400 | - | - | 1.0 |
| L1, L2, L3 (three other lanes) | /v1/messages | ~279,900 (est.) | 400 | - | - | 1.1 to 1.3 |
| p3, p3b (repeat, same bytes) | /v1/messages | ~284,700 (est.) | 400 | - | - | 1.7, 2.1 |
| c1-cached-prefix-plus-60k | /v1/messages | ~289,800 (est.) | 400 | - | - | 1.6 |
| o1-openai-route-290k | /v1/chat/completions | ~290,000 (est.) | 400 | - | - | 1.2 |
| x1-950k | /v1/messages | ~949,900 (est.) | 400 | - | - | 3.7 |

Estimates come from the calibrated ratio of 3.7597 characters per token
(spread 3.7594 to 3.7600 across eight accepted seed-41 probes). About 2.7M input
tokens were accepted in total. No 429 was returned.

## Production evidence

Read-only aggregates from `~/data/opencode-cc.db`, `/v1/messages`, status 200,
`deepseek-v4.1-flash-free` (1,162 responses):

| Input tokens | Responses |
|---|---:|
| < 50K | 191 |
| 50K to 100K | 445 |
| 100K to 150K | 404 |
| 150K to 190K | 121 |
| 190K to 261K | 0 |
| >= 261K | 1 |

The largest ordinary request was 166,651 tokens. Nothing fell between 190K and
261K. The one row above the ceiling is row 26627, 2026-10-22 03:08:16 IST, with
284,946 input tokens (284,928 cached), 9,800 output tokens, and 69.2 s total
(`POST /v1/messages -> 200 1m9.207s`).

That row is most likely two upstream calls whose usage was summed, not one call:

- Both totals are even: 284,946 = 2 × 142,473 and 284,928 = 2 × 142,464. The
  uncached remainder, 18, is also 2 × 9. That pattern fits two identical calls.
- `handleWebSearchShim` (`internal/server/web_search_proxy.go`) adds the
  second call's usage into the returned `Usage`
  (`answerResp.Usage.InputTokens += finalOpenAIResp.Usage.PromptTokens`). The
  shim runs when the request has a `web_search_*` server tool
  (`IsAnthropicWebSearchTool`, `internal/proxy/request.go`).
- The 69 s duration is roughly six to eight times a single 230K to 260K probe
  (8 to 13 s total).
- The journal has no retry or resample lines for that request.

This is not proven. The logged request body is truncated at 16 KB, so the tool
list is not visible. Streamed responses are not logged (`resp_body` is 10 bytes),
so the call count cannot be read from the DB.

## Interpretation before the cache-accounting repair

1. **Effective ceiling on this route: about 261K input tokens per request.** The
   limit is enforced by Zen. The relay adds no cap of its own: a 3.6 MB body
   reached Zen and came back with Zen's error (the relay's body limit is 32 MiB,
   `maxRequestBytes` in `internal/server/proxy.go`).
2. **Cached prefix tokens count toward the same limit.** c1 (a 230K cached prefix
   plus about 60K new tokens) was rejected at about 290K total.
3. **`max_tokens` does not reduce the input ceiling.** b7 was accepted with 260,188
   input tokens and `max_tokens` 32000.
4. **Same ceiling on both routes.** `/v1/messages` and `/v1/chat/completions`
   both accept 256K and reject 290K.
5. **The production cluster is consistent with early compaction from cache
   double-counting.** The old T3 Claude CPA environment used a 400K maximum and
   a 360K auto-compact window. At the previously inferred ~327K trigger, an
   almost fully cached prompt would reach that reported size at ~164K actual
   input. This fits the production peaks. It is supporting evidence, not a
   directly observed compaction event or a verified fixed-buffer formula.
6. **Confirmed Anthropic usage-format mismatch.** `internal/proxy/response.go`
   and `internal/proxy/stream.go` report OpenAI `prompt_tokens` (including cache)
   as Anthropic `input_tokens`, then report cache again as
   `cache_read_input_tokens`. Anthropic defines total input as the sum of
   uncached input, cache creation, and cache reads. Claude Code's
   [status-line documentation](https://code.claude.com/docs/en/statusline#context-window-fields)
   explicitly uses that sum for live context. Thus this mapping inflates the
   documented client context count. This was verified from source and official
   documentation; no production mapping change was made in this investigation.
   Exact compaction timing in this installed CLI remains unmeasured.
   **Update:** the usage mismatch is fixed and deployed; see the repair section below.
7. **Shim usage returned to the client.** The web-search shim's summed usage goes
   back to the client, not only to the log. If a client uses `input_tokens` as
   its context size, shim turns would look about twice their real size. Not
   verified against Claude Code.

## Client change (2026-10-23)

Chain for this model: T3 Code provider `claudeAgent_2` ("Claude CPA", driver
`claudeAgent`) → Claude Code (`ANTHROPIC_BASE_URL=http://127.0.0.1:8317`) → CPA →
opencode-cc `:8787` → Zen. The `claude-cpa` launcher is not used.

In `~/.t3/userdata/settings.json`, under `providerInstances.claudeAgent_2.environment`:

| Variable | Before | After | Meaning |
|---|---|---|---|
| `CLAUDE_CODE_MAX_CONTEXT_TOKENS` | 400000 | 250000 | Window Claude Code assumes for model IDs it does not recognize, such as `deepseek-v4.1-flash-oc` |
| `CLAUDE_CODE_AUTO_COMPACT_WINDOW` | 360000 | 200000 | Auto-compact window. Overrides `/autocompact`, `--autocompact`, and `autoCompactWindow` |

- Backup: `~/.t3/userdata/settings.json.bak-t3code-20261023`. A diff against it shows
  only these two values changed.
- Scope: the environment belongs to the whole instance, so it applies to every model
  on Claude CPA. Those models are `muse-spark-1.3-contributor-oc`, `space-bunny-oc`,
  `muse-spark-1.3-contributor-muse`, `minimax-m3.1-flash-preview-mcode`,
  `glm-5.3-zcode`, `glm-5.3-flash-zcode`, `mimo-v2.6-flash-oc`, `grok-4.7`,
  `gpt-6-luna`, `glm-5.3-flash-oc`, and `deepseek-v4.1-flash-oc`. The built-in Claude
  models are hidden in that instance's preferences. Because the auto-compact window
  caps each model's window, this changes the compaction configuration for all
  those models; their actual trigger points were not measured.
- Trigger point remains unmeasured. The earlier ~33K buffer estimate would
  imply ~167K in the client's reported units for a 200K window. Those units are
  inflated by the cache mapping; they cannot be equated to actual prompt tokens.
- Follow-up verification: a running `claude` process (PID 2609593 at inspection
  on 2026-10-23) has both requested values, 250000 and 200000, in `/proc/PID/environ`.
  Only those two non-secret variables were inspected for reporting. This proves
  process inheritance, not an observed compaction event.
- T3 source (`apps/server/src/serverSettings.ts`, `startWatcher`) watches settings
  changes, debounces them for 100 ms, invalidates its cache, and emits refreshed
  settings. A direct file edit therefore does not inherently require a T3 restart.
  Existing Claude processes retain their launch environment.
- **These settings did not guarantee compaction at 200K actual input before the repair.**
  With the old relay mapping, mostly cached input could look nearly twice as large.
  Even a 200K reported trigger could then fire near 100K actual input, or earlier
  if the CLI reserves a buffer. The cache-accounting repair below removes that
  inflation. Exact auto-compaction timing, including any client buffer, still
  requires an observed compaction event.

## Caveats

- One retrieval task (five needles, `output_config.effort` low, thinking on).
  It shows no retrieval loss up to the ceiling. It is not a reasoning-quality
  benchmark, so subtler degradation is not ruled out.
- Only the seed-41 lane was tested below 280K. The other lanes (seeds 61 and 62,
  and a Claude Code session header) were tested at about 280K only, and all were
  rejected.
- The `model` field in responses is echoed by the relay
  (`internal/proxy/stream.go`: "model is echoed in events"), so it does not prove
  which upstream model served the request.
- Sizes for rejected probes are estimates. Accepted probes report exact counts.
- The paid `deepseek-v4.1-flash` (1M per models.dev) was not tested. It bills per
  token.

## Method

Staging, with production untouched (:8787 live, :18787 Morph):

- Binary: `~/.local/bin/opencode-cc`, the same file the live `opencode-cc.service`
  runs. Its mtime is 2026-10-22 02:41, and the live process started 02:49.
- Environment: `~/.config/opencode-cc/env` (the production `EnvironmentFile`),
  sourced with `set -a`. `HTTP_PROXY`/`HTTPS_PROXY` point at WARP
  (`warp=on`, verified through the proxy).
- Data: `/tmp/cc-stage-data`, holding a 600-mode copy of `~/data/config.json` so
  `opencode://active` resolves. The listen address is set explicitly with
  `OPENCODE_CC_LISTEN=127.0.0.1:18789`, because the live config's `listen_addr`
  is `:8787`.
- Probe: a deterministic document of filler log lines, with five random
  `ACCESS CODE` lines at 1, 25, 50, 75, and 99% depth. The question comes last.
  Streamed `/v1/messages` with `output_config.effort` low and `max_tokens` 4096
  (unless noted).

Reproduce (absolute paths, from the repo root):

```sh
set -a; . ~/.config/opencode-cc/env; set +a
install -m 600 ~/data/config.json /tmp/cc-stage-data/config.json
OPENCODE_CC_LISTEN=127.0.0.1:18789 ~/.local/bin/opencode-cc -data /tmp/cc-stage-data &
cd scripts/context-probe
CTXPROBE_OUT=/tmp/ctx-results python3 -I probe.py --id p1 --chars 708000 --seed 21
CTXPROBE_OUT=/tmp/ctx-results python3 -I probe.py --id b6 --chars 977400 --seed 41
python3 -I openai_route_probe.py 960000 41
```

`--chars` counts characters, and the filler runs about 3.76 characters per token.
Scripts: [`scripts/context-probe/`](../../scripts/context-probe/). `probe.py` runs
one probe, `cache_probe.py` appends to an accepted prefix, and
`openai_route_probe.py` runs the non-streamed OpenAI route.

Cleanup: the staging process was stopped by PID after confirming its command line.
`/tmp/cc-stage-data` was removed. Production health was checked after the stop.

## Open decisions

- **Paid model.** Testing `deepseek-v4.1-flash` (1M on models.dev) would bill tokens,
  so it needs approval.
- **Verify the compaction point.** In a new Claude CPA thread, run `/autocompact`
  (shows the active window and its source) and `/context` (shows the buffer and free
  space). Measure actual compaction timing after correcting usage translation.
  Claude Code documents adding cache tokens; the relay now supplies disjoint
  input/cache counts. Any client buffer still needs measurement.
- **Possible fixes, none applied.** (a) The web-search shim should report the final
  call's usage, or keep summed usage out of the client-facing `usage` and log it
  separately. (b) If the user wants more than about 190K per session, compaction
  must trigger well below 261K, with correct cache accounting. (c) Record the 261K ceiling for this model
  somewhere the relay can read it.

## Follow-up verification (2026-10-23)

- Production `/api/health`: HTTP 200, `ok: true`, version 1.3.2.
- No additional inference requests or production deployments in this follow-up.
- Read-only DB recheck, excluding ambiguous row 26627: row 27776 reports
  166,950 input and 165,632 cached tokens, yielding 332,582 reported input tokens
  under Anthropic's sum. Row 27356 reports 166,651 + 165,248 = 331,899.
  These examples corroborate the earlier ~327K–333K reported-input cluster.
- Probe counts above remain the upstream inclusive input totals from the original
  measurements. Do not add their cache column when interpreting the actual
  tested prompt sizes.

## Cache-accounting repair and deployment (2026-10-23)

The relay now subtracts cache reads from inclusive OpenAI input before emitting
Anthropic usage. This covers Chat Completions and Responses upstreams, streamed
and non-streamed. The reverse Anthropic → Responses conversion includes cache
reads and writes in total input. Request-log input totals remain inclusive, so
the panel and historical OpenAI-route measurements retain their meaning. The
web-search shim also uses disjoint cache accounting; its separate behavior of
summing multiple upstream calls remains unchanged.

Validation:

- `go build ./...`, `go test ./...`, staging build, and `git diff --check` passed.
- Regression tests cover uncached, partly cached, fully cached, malformed cache
  counts, reverse conversion, cache creation, streamed events, and database totals.
- Staging at 18789 used isolated saved settings and the production environment.
  A live request succeeded. Four repeated five-needle probes all recalled 5/5:
  the cold call reported 12,917 fresh tokens; subsequent streamed and non-streamed
  calls reported 117 fresh + 12,800 cached = the same 12,917 total.
- Atomically installed after staging passed. Rollback binary:
  `~/.local/bin/opencode-cc.bak-cache-accounting-20261023-151633`. Morph processes
  were not restarted and continue using their original binary inode.
- Production health and a real `/v1/messages` call passed (`OK.`).
- An isolated Claude Code invocation used the running T3 Claude CPA environment
  and exact alias `deepseek-v4.1-flash-oc`, through CPA at 8317 and the production
  relay. It generated `function add(a, b) { return a + b; }`, exited successfully,
  and reported `contextWindow: 250000`. Configured auto-compact window stays 200000.
- Staging was stopped and its copied configuration removed.

A standalone proxied Cloudflare trace timed out and WARP logs included a missing
registration warning during inspection. Despite that diagnostic, actual staging
and production inference succeeded through the configured proxy; no WARP settings
were changed.

Evidence: [cache-accounting validation](evidence/2026-10-23-cache-accounting-validation.json).
The original context-limit probe evidence is unchanged. Updated probe scripts
show both fresh input and total input under the corrected Anthropic semantics.

**Remaining limit:** no 200K conversation was grown until auto-compaction during
this repair acceptance run. The context window is verified in the real client;
an exact 200K actual-token compaction threshold is not established. The CLI may
reserve a buffer. The independent ~261K upstream input ceiling still applies.

## Provider isolation update (2026-10-23)

At the user's request, DeepSeek was moved to `claudeAgent_deepseek`
(display name `Claude CPA · DeepSeek`), retaining context maximum 250000 and
auto-compact window 200000. The normal `claudeAgent_2` provider now uses 460000
and 400000 respectively. T3's live catalog confirms DeepSeek appears only in
the dedicated instance; its favorite was migrated. The existing CPA credential
was copied inside T3's private secret store without exposing it in settings.
Existing threads bound to the old instance need to select the dedicated provider
for DeepSeek's launch limits. No upstream capacity or precise compaction trigger
is implied by these settings. Authoritative policy: project `AGENTS.md`.

Backup before splitting: `~/.t3/userdata/settings.json.bak-context-split-20261023-153129`.

## Single-provider correction (2026-10-23)

The user rejected provider splitting. Removed `claudeAgent_deepseek` and its
duplicated credential, retained DeepSeek in `claudeAgent_2`, and repointed its
favorite. One Claude CPA instance now uses shared 460000 context maximum and
400000 auto-compact window. This supersedes the isolation update above.
The upstream free DeepSeek route still has the measured ~261K input ceiling.
Current policy is in the project AGENTS.md.

## Model-specific launch limits in one provider (2026-10-23)

The user wants one Claude CPA provider, with DeepSeek alone at 260000 maximum
context and 200000 auto-compact. The provider-wide defaults remain 460000 and
400000. `~/.local/bin/claude-cpa-context` now wraps the real CLI, reads the
selected `--model`, and overrides those two environment variables only for
`deepseek-v4.1-flash-oc`. Source: `scripts/claude-cpa-context`. Other models and
CLI arguments pass through unchanged. T3 provider `claudeAgent_2` points its
`binaryPath` at this launcher. No second provider was created.

Tests cover separate and equals-style model flags, normal-model passthrough,
version passthrough, and argument preservation. Live acceptance evidence is in
`evidence/2026-10-23-model-specific-context.json`.

**Process lifetime limitation:** T3's live `setModel` can change models without
relaunching Claude. The CLI's environment maximum is fixed for the process, so
stop/relaunch a thread when switching between DeepSeek and other models. This
launcher does not implement dynamic context switching inside an existing CLI.
The configured auto-compact window is not a measured exact trigger point.
