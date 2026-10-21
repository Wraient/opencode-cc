# WARP connection failure and full-chain recovery — 2026-10-22

**Fixed and deployed in both services.** The live request through port 8317,
model `deepseek-v4.1-flash-oc`, returned HTTP 200. A real Claude Code 2.1.306
run through that exact alias completed Write and Read; the resulting JSON file
was independently verified. Its model usage contained only the requested alias.
[Sanitized evidence](../evidence/network-recovery-2026-10-22.json).

## Failure chain

The reported failure occurred during SOCKS connection establishment:
`host unreachable` between the configured WARP local proxy and OpenCode.
It was not evidence that the OpenCode credential expired. The immediate later
`auth_unavailable` response represented CLIProxyAPI selection/cooldown state.
The tunnel and original alias had recovered by the initial live reproduction.

Two local gaps amplified the transient network problem:

1. opencode-cc retried timeouts/EOF/reset failures but did not retry the actual
   SOCKS host-unreachable error. It returned a generic 502 without a retry hint.
2. CLIProxyAPI's Claude adapter used Anthropic quota-reset padding for standard
   5xx service retry hints, and did not retain the Retry-After response header.
   The full-chain fault test caught this: adding a relay-only hint was insufficient.
   The configured generic transient cooldown was 60 seconds.

## Changes

- Retry known temporary SOCKS/HTTP-proxy connection failures through the same
  route, within the existing three-attempt bound and two-second backoff.
- After exhaustion, return **503 with Retry-After: 2** on Chat, Messages,
  Responses, and their translation paths. Log the matching HTTP status.
- In CLIProxyAPI, parse standard service deadlines without quota-window grace
  for HTTP 408 and 5xx. Preserve only the Retry-After header on those errors.
  Invalid/non-finite/overflowing retry hints are rejected.
- Retain existing auth/quota rules. Real 401/403/429 responses are not replayed
  or shortened by this repair. Neither model routing nor WARP identity changed.
  No live cooldown files were deleted, and no global cooling policy was disabled.

CLIProxyAPI source: [4639715](https://github.com/Wraient/CLIProxyAPI/commit/4639715),
based on the installed local v8.0.4 patch line. The deployed binary identifies as
`8.0.4+local2`. The relay repair is separate from pre-existing, uncommitted
reasoning/session-affinity work; that working-tree content was preserved.

## Real fault-injection acceptance

`scripts/test_proxy_recovery.py` starts isolated CPA and opencode-cc instances
on 18317 and 18789. A local SOCKS shim returns real SOCKS5 reply 4 (host
unreachable), then transparently forwards to the existing WARP proxy. The final
answer still comes from the real DeepSeek model. Production WARP is never
disconnected. Fixture credentials, config, and cooldown state remain isolated.

| Same DeepSeek alias through CPA | Result |
|---|---|
| First three SOCKS connections fail, then network recovers | Same request returns 200 in 9.084s |
| Persistent outage | Bounded six connection attempts across both retry layers; 503 with Retry-After: 2 in 10.657s |
| Restore connectivity after that outage | Same alias returns 200 in 4.410s, with no auth reset |

Reproduce after building both patched staging binaries:

```sh
python3 scripts/test_proxy_recovery.py --out /tmp/recovery.json
```

The script uses the installed native credential read-only and requires a
connected WARP local proxy. It runs no host tools from model responses.

## Validation and deployment

- opencode-cc: full build/test suite and vet passed. Real SOCKS regression tests
  cover recovery, bounded exhaustion, Messages/Chat status propagation, and
  unchanged 401/403/429 responses with a one-hour quota hint.
- CLIProxyAPI: executable build passed; targeted Claude service/rate-limit and
  retry tests passed, plus SDK auth/quota/cooldown and API error-response tests.
- Both previous binaries and configs were backed up privately. Each production
  unit was stopped, its executable replaced atomically, and the unit restarted.
  Config, credential state, WARP settings, and Morph services were not changed.
- Live relay Messages: 200 in 2.306s.
- The user's exact curl payload through 8317: 200 in 1.914s.
- Messages through the same CPA alias: 200 in 1.429s.
- Claude Code / `deepseek-v4.1-flash-oc`: actual Write and Read completed in
  7.485s; expected JSON verified on disk. No other model appears in model usage.
- Isolated services were stopped and temporary fixture data removed.

This is recovery under tested failure conditions, not a claim that a network
or provider can never be unavailable. Sustained outages intentionally return
an accurate retryable 503 after bounded attempts. Once connectivity returns,
the same credential/model becomes usable without a manual reset.
