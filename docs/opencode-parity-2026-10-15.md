# OpenCode v2 parity audit — 2026-10-15

> **Historical result — regression repaired 2026-10-18.** These benchmark
> numbers describe October 15 only. A later network-path-dependent 403 and an
> expired relay key were repaired with WARP and a read-only reference to the
> CLI's active OAuth credential. See [the recovery and live validation](diagnostics/2026-10-18-free-tier-rejection.md).

Current 403 failures were reproduced, fixed, tested on isolated staging, and deployed. This is an API translator with an audited upstream contract; it is not the entire OpenCode agent, and future gateway compatibility cannot be guaranteed.

## Released source and provenance

- npm stable: `@opencode/cli@2.0.29` ([registry metadata](https://registry.npmjs.org/@opencode/cli/latest)). GitHub’s latest-release feed still points at the v1 line; the v2 npm channel and tag were checked separately.
- Source tag: [`v2.0.29`](https://github.com/anomalyco/opencode/tree/v2.0.29), commit `042d0832f5f3a73eaa039a58a7d565776f6d4290`.
- Full source checkout: `/home/wraient/Projects/opencode-upstream-v2.0.29`.
- Release binary: `/home/wraient/.cache/opencode-cc/releases/2.0.29/opencode`. The npm tarball’s published integrity hash was verified before extracting it.
- Bundled tool reference, source hashes, and MIT license: `internal/proxy/opencode/`.
- `scripts/check_opencode_source.py --source /home/wraient/Projects/opencode-upstream-v2.0.29 --latest` passed all four source hashes and the npm stable-version check.

## Why the previous fixes did not last

The old proxy approximated client identity and tools by hand. The unit tests checked those assumptions rather than an actual released CLI request. Controlled requests with the same key/model established two independent failures:

| Controlled change | Result |
|---|---|
| CLI-generated request with released tool definitions | 200 |
| Replace tools with the old empty-schema shell/read markers | 403 FreeTierError |
| Keep real definitions; add a fresh, correctly shaped x-opencode-request | 403 FreeTierError |
| Keep real definitions; omit the system prompt | 200 |
| Keep real definitions; use a 12-hour-bucket session | 200 |
| Keep real definitions; omit organization header | 200 |
| Keep real definitions; use a 64-hex opaque session | 403 on this free lane |

Therefore a version bump or session-only fix could not address this incident. The gateway implementation is remote; these experiments identify the conditions observed today, not an immutable validation specification.

## Changes

- Removed generation/forwarding of obsolete `x-opencode-request` and its unused ID generator.
- Added `x-opencode-session-id`, preserving valid child IDs separately from parent affinity, and preserved valid parent-session headers.
- Replaced empty placeholder schemas with complete shell/read definitions captured from the released CLI and verified against its source. Both Chat Completions and Responses encodings are covered. Existing client definitions and explicit tool choice remain intact; preparation is idempotent.
- Aligned the fallback User-Agent with the audited release. Installed-version detection now chooses the newest stable version without going below the audited baseline, has a two-second deadline per probe, and refreshes every five minutes. Upgrading an installed CLI no longer requires a service restart to refresh the cached version.
- Changed the default upstream from legacy `/zen` to the documented v2 `/inference` endpoint. Explicit custom/Go upstream settings still take precedence. Your Console configuration also advertises an `/inference/...` route. [Current official endpoints](https://opencode.ai/v2/docs/console/models/) list both tested free models.
- Added released-contract regression tests, a source/release drift check, and a repeatable CLI-generated request benchmark. No recurring updater or network job was installed.

## Measurement method and limits

`scripts/bench_opencode_parity.py` runs the actual released CLI with isolated HOME/XDG directories, a fixed prose prompt, and no interactive stdin. A loopback relay captures and times the primary LLM request; title requests are excluded. The exact primary body is then replayed through staging. Burst tests replay that captured CLI-generated request directly and through the proxy at concurrency two and four.

The measuring relay uses Python requests for its upstream HTTP connection. Thus these numbers compare **CLI-generated request contracts through the same inference endpoint**, not the native CLI TLS transport. CLI wall time is separately recorded and includes startup, title generation, and shutdown. A strict native-transport TTFT comparison remains unmeasured.

TTFT means time to the first nonempty reasoning or text delta. Visible TTFT counts text only. Throughput below means all upstream-reported output tokens divided by full request time, including prefill and hidden reasoning. Muse sends substantial hidden reasoning: a separate diagnostic reported 1,381 output tokens, 1,252 of them reasoning. Its output-token count divided by the short visible-text window produces misleading 1,000+ tok/s figures; those raw stream-window values are retained in the JSON for transparency and must not be treated as model decode speed.

The serial sample is only two paired requests per model, in alternating order. These are observations, not stable percentiles or an endpoint latency guarantee. Cache and upstream scheduling can dominate the local cost.

## Current inference endpoint results

All 32 measured requests returned 200 and complete streams: eight serial requests plus 24 burst requests. No 403, 429, or exposed rate-limit headers were observed.

| Model | CLI-generated TTFT | Proxy TTFT | TTFT gap | CLI whole-request tok/s | Proxy whole-request tok/s |
|---|---:|---:|---:|---:|---:|
| mimo-v2.6-flash-free | 3.94s | 4.03s | +0.10s | 18.1 | 18.5 |
| muse-spark-1.3-contributor-free | 8.49s | 6.96s | -1.53s | 96.7 | 99.9 |

For MiMo, where reasoning deltas are streamed, the measured stream-window rate was 35.5 versus 36.1 tok/s for CLI-generated and proxied requests. Muse stream-window rates are unsuitable for the decode comparison described above.

| Model | Burst | CLI-generated TTFT | Proxy TTFT | CLI whole-request tok/s | Proxy whole-request tok/s |
|---|---:|---:|---:|---:|---:|
| mimo-v2.6-flash-free | 2 | 8.51s | 1.52s | 11.8 | 26.2 |
| mimo-v2.6-flash-free | 4 | 3.36s | 2.67s | 19.8 | 21.7 |
| muse-spark-1.3-contributor-free | 2 | 14.91s | 7.85s | 50.0 | 84.9 |
| muse-spark-1.3-contributor-free | 4 | 7.70s | 17.59s | 78.2 | 43.6 |

A separate local synthetic SSE test used 20 paired requests: direct median TTFT 13.14ms, proxy 15.59ms; added relay cost **2.45ms**. It simulated 32 token frames with a 10ms initial delay and 2ms between frames. It does not measure real inference, long-history translation, or video processing.

## Rate limits and remaining gaps

- Four-request bursts succeeded on both routes. This establishes only the tested burst size. No daily/RPM/TPM ceiling or quota-reset interval was measured, and the tests intentionally did not exhaust the account.
- The proxy retains its shared two-slot admission guard for Responses-native traffic. At four concurrent Muse requests, TTFT increased from 7.70s to 17.59s and per-request whole-request throughput decreased from 78.2 to 43.6 tok/s. The two-slot guard is consistent with this queueing penalty, though the experiment does not isolate every source of delay. It protects prior large-history upstream failures and was not removed based on a short-prompt test.
- Existing tests verify forwarding/synthesis of `Retry-After` on 429 responses. The 403 fixes do not raise the account’s upstream quota.
- API translation, prompt-cache normalization, marker-call filtering, recovery/resampling, video transcoding/size caps, and Go HTTP/1.1 transport remain proxy behavior. They differ from a native OpenCode session. The proxy does not execute its added shell/read definitions.
- An API key in an environment file is not the CLI’s full Console OAuth/device-login lifecycle. No login or external account settings were changed.
- Caller tools are preserved rather than overwritten. Arbitrary caller schemas and future model/provider protocols are not guaranteed to satisfy the free-tier gateway.
- Default model selection still preserves the existing configuration (`glm-4.6`); clients in these tests explicitly select the supported free models. The audit does not certify every paid model or the old default model.
- The source check is opt-in and detects release drift; it cannot patch future changes by itself. Absolute “never needs a fix again” parity would require using the official agent itself, which has a different role from an Anthropic/OpenAI API translator.

## Validation and operation

- `go build ./...`, `go test ./...`, `go vet ./...`, and `git diff --check` passed.
- Staging listened only on `127.0.0.1:18787` with `/tmp/cc-stage-data`. The production port was served only by its systemd unit.
- Real Messages chat, streaming Responses completion, Chat Completions, model listing, and a forced client-declared `get_weather` tool call were verified. The tool call returned `{"city":"Paris"}` without executing a tool on the host.
- Video code and its size/transcoding guards were not changed. No video performance claim is made.
- Contrary to the old operational note, `~/data/config.json` exists and contained a saved Zen upstream pool. Its URL migration was first tested on staging with the saved configuration; only the legacy Zen base URLs were changed. Timestamped backups retain the prior configuration.
- Deployment follows the stop → copy verified staging binary → start procedure, with a saved prior binary and automatic rollback if the health/chat smoke fails. Deployment and smoke evidence is in `deployment.json` below.

## Reproduction

```sh
python scripts/check_opencode_source.py --source /home/wraient/Projects/opencode-upstream-v2.0.29 --latest
# Supply ZEN_API_KEY securely in the environment. Start isolated staging first.
python scripts/bench_opencode_parity.py \
  --cli /home/wraient/.cache/opencode-cc/releases/2.0.29/opencode \
  --proxy http://127.0.0.1:18787 --upstream https://opencode.ai/inference \
  --runs 2 --concurrency 2,4 --out /tmp/opencode-parity.json
```

Raw metrics: [current inference](evidence/opencode-parity-2026-10-15/current-inference.json), [local overhead](evidence/opencode-parity-2026-10-15/local-overhead.json), [initial legacy Zen experiment](evidence/opencode-parity-2026-10-15/legacy-zen-initial.json), [deployment](evidence/opencode-parity-2026-10-15/deployment.json). Credentials and request bodies are excluded.
