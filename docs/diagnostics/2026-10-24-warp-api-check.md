# DEPLOYED: Claude Code / DS V4.1 Flash Free repair — 2026-10-24

**The requested live chain is now working:** Claude Code → production CPA (`deepseek-v4.1-flash-oc`) → production opencode-cc → DS compatibility gateway → OpenCode Zen (`deepseek-v4.1-flash-free`). This supersedes the unresolved outcomes in the historical sections below.

The final production file task wrote and read back `LIVE_CPA_DS_FREE_4286`. The real local relay recorded requests **32178, 32179, 32180**, all HTTP **200**, all targeting the exact free model. Claude reported fast mode off. Full sanitized evidence is in `../evidence/2026-10-24-ds-flash-live.jsonl`. A separate live `/v1/messages` smoke returned `LIVE_DS_FREE_9261` with HTTP 200.

## What repaired the path

The deployed fix combines an audited native OpenCode system prelude with preserved Claude instructions/tools/history, consolidated system messages, native-style project/session metadata, and Bun fetch. Parameter-only, token-field-only, tool-renaming and shorter-prelude trials did not repair the request. The combined path was validated in the real harness, rather than inferred from an isolated HTTP 200. The exact private gateway classifier remains unknown; these results do not establish that every free-limit error has the same cause.

Network eligibility was independently isolated earlier with the same request/key/runtime returning direct 200 → phone 429 → direct 200. The gateway now automatically tries phone/direct egress, remembers a successful route, and can request one background Telegram phone reset per 30 minutes when both paths reject or fail. It independently verifies egress recovery. Credentials/accounts, model and upstream quota state are never replaced or cleared.

The user supplied the dashboard figures (5% and 0%) and explicitly asked not to re-check usage bars. Those figures were accepted as given; no dashboard usage checks were made during the repair acceptance.

## Acceptance completed

| Test | Actual result |
|---|---|
| Real Claude → staged CPA → staged relay, supplied key | Write + Read file task, all three model turns 200 |
| Same exact CPA alias, other account's active OAuth credential | File task `CPA_ACTIVE_ACCOUNT_6392`, exit 0 |
| Brief 8-second controlled 429 outage | Automatically recovered and completed `CPA_BRIEF_RECOVERY_5137` |
| Sustained 195-second 429 outage, `Retry-After: 86400` | Original invocation recovered at 213.794s and completed the file task |
| Second invocation after sustained outage, **same CPA process** | File task succeeded in 32.306s; no stale day-long cooldown |
| Production relay smoke | HTTP 200, expected exact acknowledgement |
| Production Claude + CPA alias + file tools | Exit 0 in 19.832s, verified file and three live relay 200s |
| Build/unit validation | `go build ./...`, `go test ./...`, stage build; Bun tests (27 assertions) passed |

Recovery evidence: `../evidence/2026-10-24-ds-flash-recovery.jsonl`. Account acceptance: `../evidence/2026-10-24-ds-flash-acceptance.jsonl`. Repeatable isolated full-chain runner: `scripts/test_ds_flash_chain.py`.

## Installed operation

- Existing production remains `opencode-cc.service`, port 8787. Binary was atomically replaced after staging. Rollback binary is retained under `~/.local/state/opencode-cc-ds-flash/backups/`.
- New enabled user service: `opencode-cc-ds-flash.service`, loopback **18790**, with automatic restart and a 256 MiB memory cap (approximately 29 MiB measured at startup).
- Current versioned runtime: `~/.local/lib/opencode-cc-ds-flash/current` → `releases/20261024-v2`. Bun 1.4.2 was verified against the official release archive. Audited prelude/source/license/manifest are in `scripts/ds-flash-gateway/`.
- The relay drop-in enables the transport with `OPENCODE_CC_DS_FLASH_FETCH_URL=http://127.0.0.1:18790`. The Go transport intercepts only the exact free model on trusted Zen Chat endpoints; other models/origins retain their existing path. OAuth resolution remains on the original HTTPS origin and reads no refresh token into the helper.
- Automatic phone reset uses the explicitly authorized [Telegram IP-reset skill](/home/wraient/.config/opencode/skills/telegram-ip-reset/SKILL.md). Its private output is discarded. The helper persists only a reset timestamp and performs no idle model probes.
- WARP, Morph, Tailscale, CPA production configuration, account limits, and shared model/context defaults were not changed by this deployment. Morph processes remained active through the atomic binary replacement.
- Temporary test listeners 18789/18319/18794 are cleaned up. Port 18790 is now a **production helper** and must not be reused for experiments.

Original client instructions and tool schemas are retained; the compatibility prelude adds roughly 2–3K context tokens. Existing DeepSeek 260K maximum / 200K auto-compact settings remain. Real 401/403 and all-route quota failures are still returned; successful recovery never fabricates a model answer or switches to another model/paid route.

---

# Recheck with hosted Console evidence — 2026-10-24, 11:10 UTC

**Current outcome: the exact free model has genuinely succeeded through isolated opencode-cc staging, but the real Claude Code request still fails, and later native runs also return 429. Production access is not repaired.** This section supersedes broad conclusions below; the earlier entries retain their original timestamps.

Final current check: at 11:11:32 UTC (16:41 IST), the saved key's fresh native OpenCode direct run also returned 429. Production health remained `ok:true`; temporary listeners 18789/18790 were absent; repository evidence credential scan passed. No production repair is claimed.

## The strongest evidence now comes from the hosted gateway

The authenticated Console API `GET /console/api/request-logs?limit=100` accepts both the supplied service-account API key and the other account's active OAuth credential. This endpoint and its response schema are published in the Console's served [app bundle](https://opencode.ai/console/assets/app-pages-QFrIvWHD.js). Raw responses remain private in `~/.cache/opencode-cc/zen-console-private/`; sanitized results are in `../evidence/2026-10-24-zen-console-logs.jsonl`.

The first 100-row sample contained 91 matching DeepSeek Flash Free rows for the saved credential (36 successes, 55 HTTP 429 rejections) and 89 for active OAuth (70 successes, 19 HTTP 429 rejections). These are recent retained requests, **not remaining-allowance counters**; some history predates this recheck. Successful rows have an upstream attempt with HTTP 200. The sampled 429 rows have `outcome: rejected`, `attemptCount: 0`, and no upstream attempts. Thus these particular failures occur in the hosted OpenCode gateway before a model-provider attempt. They are not inferred from local relay health or a renamed local error.

The Console deliberately sanitizes standard-product error details: its published client schema maps them to `inference_failed` / `The inference request failed.` The available logs do not disclose the private free limiter's decision reason. An empty `metadata` object is not evidence that there is only one quota scope.

## Controlled network and transport reproduction

With an unchanged captured request body, credential, session headers and standalone Bun 1.4.2:

| Case | Result |
|---|---:|
| Direct egress, before | 200 |
| Phone HTTP proxy | 429 |
| Direct egress, after | 200 |

These three rows (`current_matched_network_*`) in `../evidence/2026-10-24-zen-wire-comparison.jsonl` isolate the network path as a causal factor for that request. No IP reset, token replacement or session refresh occurred between them.

A subsequent raw Go H1 direct replay returned 429, and a subsequent Bun direct replay of that same session also returned 429. Another current series recorded native 200 → Bun 200 → Go 429 → Bun 429. This is an observed sequence, **not proof that Go permanently poisons a session, a specific TLS fingerprint is required, or the rejection is independent of changing limiter state**. Earlier body and header conclusions must retain their passing-control requirements.

## Actual relay and harness acceptance tests

1. Native OpenCode 2.1.2, supplied key, direct egress: `RICH_STAGE_9374` returned normally.
2. Its full native request body and fresh diagnostic session headers were sent to isolated opencode-cc on 18789. The existing Go relay forwarded through a temporary Bun helper on 18790, which used direct egress. **The relay returned HTTP 200, text `RICH_STAGE_9374`, and usage: 5263 prompt / 8 completion tokens.** This is real exact-model relay inference, not a health response. See label `rich-native-fixture-direct-bun-stage` in `../evidence/2026-10-24-zen-access-matrix.jsonl`.
3. Native OpenCode then returned a fresh precheck `CLAUDE_PRECHECK_8462` on direct egress.
4. Real Claude Code 2.1.310 was run in isolated state against the same staging relay/Bun/direct path, on `deepseek-v4.1-flash-free`. It retained its real tools and prompt, used fresh diagnostic session headers, and asked for `CLAUDE_STAGE_4682` without executing tools. **It returned the upstream 429.** Its JSON result confirms fast mode off / standard speed. See `../evidence/2026-10-24-zen-claude-stage.jsonl`.
5. Another native precheck `HEADER_CHECK_9361` succeeded. Replaying the real translated Claude body with native headers still returned 429. Merely stripping Anthropic headers is therefore not a demonstrated solution.
6. A `max_tokens` → `max_completion_tokens` trial had a failing native baseline. It does not prove that either field is responsible. No such speculative rewrite was deployed.
7. Later native saved-key and active-OAuth direct runs also returned 429. Earlier successes do not establish either account's current remaining allowance.

The staging success used a full real OpenCode request and forwarded fresh client metadata. It does not establish generic Claude/CPA compatibility, reliable recovery, or an unlimited free API. The requested complete Claude Code → CPA → opencode-cc → Zen acceptance target remains unmet.

## Real interpretation and what is required

Authentication, free-client eligibility, network eligibility and available free capacity are distinct requirements. A different account/key does not isolate the client, session or network conditions. A new exit IP does not establish request eligibility or replenish an account's allowance. A 429 body does not identify which one rejected the call.

The [official model documentation](https://opencode.ai/v2/docs/console/models/) explicitly requires OpenCode for free-tier chat models. The currently verified successful input is an actual OpenCode request on a permitted path. The real Claude request fails even with a healthy transport and diagnostic fresh metadata. To make the requested Claude/CPA route reliable, a provider-supported free-client contract or a gateway-side explanation/repair is still needed; copying a key or upgrading WARP alone is insufficient. This investigation does not prove the exact private algorithm or a reliable local repair.

No automatic IP reset, fabricated post-429 session rotation, quota clearing, paid inference substitution, or production binary/routing change was installed. Temporary staging/forwarder processes are removed after the recheck. The read-only log collector is reproducible with:

```
python scripts/probe_zen_console.py --limit 100 --out /tmp/zen-console-proof.jsonl
```

The collector uses the phone only for the read-only Console API; this is independent of the inference path under test. It never prints credentials, exports refresh tokens, or writes the OpenCode credential database.

---

# Current findings — 2026-10-24, 06:00 UTC

**Outcome: both current credentials work in native OpenCode, but reliable DeepSeek V4.1 Flash Free access through opencode-cc is NOT established. WARP is updated but still intermittently fails HTTPS.** Earlier conclusions that both accounts were simply exhausted are unsupported and superseded below.

## What is proven

- The saved API key (`ebf5047da8ba`) and active OpenCode OAuth access credential (`13896f1944d0`) are different credentials in different workspaces. Both returned HTTP 200 from `/console/api/v2/config`, directly and through the phone. Neither a successful config request nor its model list establishes inference quota.
- Both credentials produced real DeepSeek V4.1 Flash Free answers in isolated native OpenCode 2.1.2. Read-only HTTP hooks recorded the actual authorization fingerprints, exact model, canonical inference URL, and upstream 200. Fresh challenge answers include `ZEN_CHECK_6197` for both credentials; later saved-key challenges also succeeded. This disproves the blanket claim that neither credential can use this model.
- The separate legacy key in `~/.local/share/opencode/auth.json` (`f4e60859b696`) returns 401 `ApiKeyExpired`. It is not the active OAuth credential. Do not label these identities interchangeably.
- Invalid and anonymous inference controls can also return 429 `FreeUsageLimitError`. Consequently this inference error alone does not establish valid authentication, account exhaustion, or the scope of the limiter.
- Seven recorded phone resets independently verified a changed IPv6 egress. Resets sometimes preceded successful native replies, but unchanged native request replays could still fail after a verified reset. A changed IP is not sufficient for reliable access. The phone's public IPv4 also changes between requests without a manual reset.
- The phone proxy on `127.0.0.1:18889` is **HTTP CONNECT**, not SOCKS. WARP on `127.0.0.1:40000` is SOCKS5. Production's `NO_PROXY` includes `opencode.ai`, so its OpenCode traffic currently bypasses WARP.

## Controlled session-header finding

All tests below held credential, phone proxy, canonical URL, body and Bun runtime fixed, except the stated header change. Timestamps are UTC; full results are in `../evidence/2026-10-24-zen-request-ablation.jsonl`.

| Test | Time | HTTP |
|---|---|---:|
| Fresh native headers | 05:52:17 | 200 |
| Older body, fresh headers | 05:52:20 | 200 |
| Fresh body and headers, control | 05:52:23 | 200 |
| Fresh body, older headers | 05:52:24 | 429 |
| Fresh body and headers, restored | 05:52:27 | 200 |
| Only older project header | 05:52:52 | 200 |
| Only older `x-opencode-session` | 05:52:53 | 429 |
| Only older actual-session / affinity / session-id headers | 05:52:56–05:53:01 | 200 each |
| Unchanged fresh control | 05:53:04 | 200 |
| Older native request, only session timestamp prefix refreshed | 05:53:29 | 200 |

The older native session was approximately eight minutes old; the good native session was about one minute old at measurement. The relay fixture used a session timestamp about six hours old because `zen_headers.go` synthesizes IDs in 12-hour buckets. **This demonstrates session-header-dependent acceptance in this controlled run. It does not prove a universal expiration threshold or that changing the timestamp repairs the relay.** A relay-format body with a refreshed session timestamp still returned 429. No quota state was cleared and no session-rotation workaround was deployed.

## Transport and request-shape limits of the evidence

- Exact successful native bodies were replayed successfully by both standalone Bun 1.3.14 and 1.4.2 at 05:28. Upgrading Bun alone is not a demonstrated fix.
- Curl H1, curl H2, visible wire-header parity, and raw Go H1 controls returned 429 in other comparisons. Some of those controls used older sessions, so they do **not** prove a TLS-fingerprint requirement.
- At 05:55:53 native OpenCode returned the fresh challenge `ZEN_CHECK_5283` through the phone. Go and standalone Bun immediately replayed the exact captured body/key/session and returned 429 at 05:55:56–57. This bounds the reproduction gap, but does not identify a private server-side discriminator or prove that transport is the sole cause.
- Body ablations are interpretable only when their adjacent unchanged control succeeds. The first ablation series had a failing baseline; it cannot identify a bad field. Later mixed series also lost their baseline. Do not infer a single required system prompt, tool count, token-limit field, or tool-choice setting from those rows.
- The temporary Bun forwarder also failed with the relay-generated body. It was not deployed; replacing Go transport with Bun has no validated acceptance result.

## What is needed for reliable use

The [official model documentation](https://opencode.ai/v2/docs/console/models/) states that free-tier chat models require OpenCode; use of another client is documented for paid models or Go. The requested target remains the exact free model; no paid route or alternate model was substituted.

A valid API key and reachable network are therefore necessary authentication/connectivity checks, not proof of eligibility for this free route through an arbitrary client. Native OpenCode has demonstrated access, with intermittent free-limit errors. For opencode-cc, a provider-supported free-client contract or server-side explanation of the rejected request IDs is still missing. The private limiter's precise account/IP/session/transport rules cannot be derived definitively from the public 429 response. These tests do not establish which account has remaining daily quota.

Do not ship a speculative header/transport change as a fix, reset IP on 429 automatically, erase genuine cooldowns, or claim the end-to-end Claude Code → CPA → opencode-cc chain passes. It has not passed this investigation's acceptance target.

## Operational state and reproduction

- WARP was upgraded to official 2026.8.2115.0 and restarted. Default MASQUE/endpoint settings and local proxy mode remain. A final HTTPS trace through WARP timed out despite its connected/healthy status. Root cause of its path instability remains unproven.
- Production health returned `ok:true`, version 1.3.2; opencode-cc, Morph OpenCode and Morph relay services remained active. The supplied replacement key remains saved. No production Go binary, proxy routing, Morph service or OAuth database was changed by the extended experiments.
- Temporary staging and forwarder processes are removed at the end of the run. Private bearer-free fixtures remain under `~/.cache/opencode-cc/zen-proof-fixture` with restricted permissions.
- Diagnostic helpers are `scripts/probe_zen_free_access.py`, `scripts/probe_zen_native_cli.py`, `scripts/observe_zen_native.mjs`, `scripts/probe_zen_cli_contract.py`, and `scripts/probe_zen_fetch.mjs`. Run helpers with `--help`; native probes retain the exact free model and never execute tools for their challenge prompts. These are opt-in diagnostics, not background retries.
- Evidence JSONL files retain credential fingerprints, request hashes, statuses, timings and bounded sanitized response text. They contain no API bearer or OAuth refresh token.
- Example native reproduction: `python scripts/probe_zen_native_cli.py --credential saved --route phone --observe --prompt 'Reply exactly MY_CHALLENGE. Do not use tools or inspect files.' --out /tmp/zen-native-proof.jsonl`.
- IP resets used the explicitly requested [Telegram IP reset skill](/home/wraient/.config/opencode/skills/telegram-ip-reset/SKILL.md). Its completion message was unreliable; independent trace verification, rather than the script exit code, established each recorded change.

---

## Historical observations (superseded where corrected above)

The following log preserves earlier measurements. In particular, the initial native 403 came from a diagnostic tool-denial configuration, and its blanket negative conclusion was superseded by successful native requests. The initial administrator-restart blocker was subsequently resolved.

# WARP and DeepSeek API check — 2026-10-24 IST

- Production's process environment contains `NO_PROXY=127.0.0.1,localhost,::1,opencode.ai`. Therefore production requests to OpenCode bypass WARP even though HTTP_PROXY/HTTPS_PROXY point to its SOCKS listener. Do not infer WARP use from those two variables alone.
- The user supplied a replacement API key from a separate account. It is saved in the enabled OpenCode upstream; do not copy the key into diagnostics. The prior configuration used `opencode://active`.
- Both the new saved credential and the active OpenCode credential returned 429 `Free usage limit reached` directly.
- Staging used port 18789, isolated mode-0700 temporary data directories, and the production process environment with OpenCode removed from NO_PROXY. Temporary configs and processes were removed after each test. No production binary or routing change was deployed.
- Initial forced-WARP inference attempts returned 502/EOF after about 51 seconds or SOCKS host-unreachable errors. HTTPS homepage success did not establish inference success.
- WARP HTTP trace confirmed `warp=on` and a changed IPv6 exit address after reconnection. HTTPS trace was intermittent.
- After setting MASQUE preference to `h2-only` and reconnecting, **the new saved key returned a genuine upstream 429 quota response through forced-WARP staging in 1.59 seconds**. Thus quota rejection also occurred through WARP; changing egress did not establish usable quota.
- This did not establish a stable transport repair: the old-credential follow-up returned EOF, and the next new-key attempt failed at SOCKS. Settings reported HTTP/2 preference, while daemon logs/stats still showed HTTP/3/QUIC activity.
- Daemon logs showed repeated stream-blocked retries, QUIC idle timeouts and `ConnectionDriverStopped`. Tunnel stats reported approximately 60–85% estimated loss on sampled connections. These observations do not establish whether the underlying fault is the client, ISP path, or Cloudflare edge.
- Alternate endpoint probes (IPv6 endpoint, IPv4 port 4500, second IPv4 endpoint) did not establish reliable HTTPS. Endpoint and MASQUE overrides were reset to automatic defaults afterward; local proxy mode was preserved.
- `sudo -n` was unavailable. `systemctl --no-ask-password restart warp-svc.service` was denied because interactive administrator authentication is required. A daemon restart and subsequent forced-WARP inference test remain outstanding.

Build validation before staging: `go build ./...`, `go test ./...`, and `go build -o /tmp/opencode-cc-stage .` passed. No application code changed.

## Follow-up after administrator-authorized daemon restart

- Restarted `warp-svc.service` successfully at 00:23:55 IST. Credentials used for administrator authentication were not persisted in project files.
- Forced-WARP staging returned genuine upstream 429 `Free usage limit reached` for **both credentials**: new saved key in 16.23 seconds, active OpenCode credential in 43.06 seconds.
- HTTPS trace still timed out in this run. Restart therefore restored neither consistently low latency nor proven tunnel stability, despite status reporting connected/healthy.
- Both credentials were rejected through direct egress and WARP. The observed WARP route did not restore usable free quota. This does not identify how the upstream scopes quota or prove that every exit IP would behave identically.
- Production retains the user's new key and its existing OpenCode NO_PROXY bypass. No production binary change was needed; temporary staging processes/configs were removed.

## Official package update and retest

- Updated Arch package `cloudflare-warp-bin` from 2026.8.2110.0-1 to 2026.8.2115.0-1. AUR still listed the older version; Cloudflare's official resolute package index listed the newer binary. Reused the reviewed AUR packaging/install recipe with updated version and official SHA256 `021bfd4741366ee8cb24b71609bbfdf2df4182c7b72fd63fd1520f43092f5747`; makepkg checksum validation passed. Installed `chrpath` from the system repository to build the package.
- Restarted the system daemon after upgrade. `warp-cli --version` reports 2026.8.2115.0, build date 2026-10-19.
- Post-update forced-WARP staging: new saved key returned **429 Free usage limit reached in 1.92 seconds**; active OpenCode credential returned **502 upstream EOF in 45.11 seconds**.
- HTTPS trace timed out and WARP reported network instability. The update did not establish a stable tunnel or usable DeepSeek free quota.
- WARP remains in local proxy mode with default endpoint selection and automatic MASQUE fallback. Production routing and saved key remain unchanged.

## Extended diagnosis after update

- Native OpenCode v2.1.2 was exercised with isolated HOME/XDG locations, the new key supplied only through its environment, and DeepSeek V4.1 Flash Free. The forced-SOCKS-WARP run kept retrying until a 70-second deadline; the direct run returned 403 FreeTierError. These are diagnostic controls, not successful acceptance.
- Curl HTTP/1.1 and HTTP/2 trace comparisons were inconsistent: one comparison timed out only on H1, but a subsequent H1 request succeeded. No protocol-specific relay code change was justified.
- Replaced the local WARP SOCKS connection with HTTP CONNECT in isolated staging: 502 Bad Gateway after 38.83 seconds. No production change.
- Set h2-only preference and fully restarted the upgraded daemon. Settings reported HTTP/2, but live tunnel stats still reported UDP. HTTPS trace timed out and staging inference returned EOF. Restored default MASQUE preferences.
- The current IPv4 path to WARP had learned MTU 1372. DF ping payloads 1200 and 1344 succeeded, while payload 1400 was rejected locally as too large. A temporary host route with MTU locked at 1372 was tested only for 162.159.198.2. Trace still alternated success/timeouts; inference eventually returned 429 after 42.69 seconds. Removed the temporary host route; no persistent network changes.
- Tested documented protocol-specific upstream base `https://opencode.ai/inference/openai` with the new saved key: direct staging returned 429 in 2.05 seconds; forced-WARP staging returned EOF after 50.28 seconds. Retained the existing production upstream base because this comparison did not establish an improvement.
- WARP instability remains unresolved. Completed inference transports have consistently produced upstream free-usage rejection, independently of the transport errors. No successful DeepSeek response was obtained and no alternate model, paid route, or new inference account was substituted.

Related primary-source service report: https://github.com/anomalyco/opencode/issues/56219 (OpenCode's issue bot explains that free-model limits are server-side and directs account-limit inquiries to support). This contextual report does not identify the exact limiter applied to these keys or predict their reset time.

## 15:56 UTC stream reliability repair

Catch-up monitoring found two upstream response-body resets after HTTP 200 headers (requests 32217 and 32923), with no 429s. Runtime 20261024-v3 now guards SSE reads, reports an explicit API failure and selects the alternate route for the next request. The Go scanner rejects error envelopes after partial output; no generation/tool-call replay occurs inside the helper. All Go checks and six Bun tests passed, plus a staged real Claude/CPA file task and a live exact-model Messages smoke. Atomic relay replacement and versioned helper selection were deployed; Morph was untouched. The genuine healthy-hour window restarted after deployment; the 24-hour watcher remains enabled at five minutes.
