# Free-tier relay recovery — 2026-10-18

**Restored and deployed.** All three requested models now answer through the
production relay at port 8787 using WARP and the official CLI's active credential.
The seven staging checks and seven live checks passed. See the
[sanitized evidence](../evidence/opencode-warp-2026-10-18.json).

## What the controls established

- The direct network path returned 403 `FreeTierError` for DeepSeek V4.1 Flash,
  Space Bunny, and Cat Jam Free, while the official v2.0.24 CLI returned `OK`.
- The original “same API key” comparison used the **legacy auth file**. It did
  not identify the native v2 CLI's actual credential: its database showed the
  old API key inactive and a separate OAuth credential active.
- Through WARP, the relay's old API key returned **401 `api_key_expired`**.
  An independent request to documented Console `GET /api/v2/config` returned
  **401 `ApiKeyExpired`**, confirming the key was expired.
- With the active CLI OAuth access token, direct relay inference still returned
  403, while WARP staging returned 200 for all three models and Messages.

These controls isolate the credential mismatch and a network-path-dependent
acceptance difference. They do not reveal the gateway's exact validation rule.
Other users also reported October 17 failures inside official clients:
[#56397](https://github.com/anomalyco/opencode/issues/56397) and
[#56426](https://github.com/anomalyco/opencode/issues/56426). Those reports are
context, not proof of the cause in this installation.

## Repair

- Installed WARP **2026.8.2110.0**, using the reviewed `cloudflare-warp-bin`
  AUR recipe and the checksum-verified Cloudflare binary. Upstream SHA256:
  `10f006447bf7443262a91687ea35237b39c8e23fc7bc5691f3d686d152df221a`.
- Registered and enabled `warp-svc.service` in **local proxy mode** at
  `127.0.0.1:40000`. Cloudflare trace verified `warp=on` via the proxy and
  `warp=off` directly. Ordinary laptop routes and Morph services remain intact.
- Added `http.ProxyFromEnvironment` to the Go upstream transport. The live
  service environment now sets `HTTP_PROXY` and `HTTPS_PROXY` to the local
  WARP SOCKS proxy, with loopback in `NO_PROXY`.
- Added the explicit credential reference **`opencode://active`**. Saved pool
  entries and the legacy credential setting now use this reference. Each
  upstream selection reads the active native v2 credential database read-only.
  The default database path can be overridden with `OPENCODE_CC_OPENCODE_DB`.
- OpenCode retains ownership of OAuth renewal. The relay never uses the
  refresh token, writes the credential database, or saves resolved access
  tokens in its configuration. Missing, malformed, ambiguous, or expired
  credentials stop locally. References resolve only for HTTPS `opencode.ai`.
- Connection warming resolves the current credential too. Rotation is picked
  up on the next request; an obsolete saved API key is never used as fallback.
- No client identity fields or tool schemas changed in this repair. No new
  account/key, billing change, model substitution, or CLI inference bridge was
  needed.

## Validation

`go build ./...`, `go test ./...`, `go vet ./...`, and `git diff --check` passed.
Regression tests cover credential rotation, inactive legacy credentials,
read-only lookup, expiry, malformed/ambiguous state, origin restrictions,
proxy routing, and loopback bypass.

`python3 scripts/smoke_inference.py --base http://127.0.0.1:18789 --out /tmp/stage.json`
performs the opt-in staging checks. The live run changes the base to port 8787.
It verifies the acknowledgement (`OK` or `OK.`), terminal streaming events,
and an actual forced `get_weather({"city":"Paris"})` call without executing it.

| Live check | Status | Wall time |
|---|---:|---:|
| DeepSeek V4.1 Flash Free chat | 200 | 2.059s |
| Space Bunny Free chat | 200 | 0.924s |
| Cat Jam Free chat | 200 | 0.618s |
| DeepSeek Messages | 200 | 1.697s |
| Cat Jam Chat stream | 200 | 0.588s |
| DeepSeek Responses stream | 200 | 9.252s |
| Forced client tool | 200 | 1.433s |

These are smoke samples. They do not establish stable latency percentiles,
sustained tok/s, daily quotas, or RPM/TPM ceilings. The October 15 benchmark is
historical and must not be treated as a current WARP-versus-native benchmark.

## Deployment and recovery

Staging used port **18789** and an isolated saved configuration/data directory.
Port 18787 belongs to Morph. Staging was stopped and its temporary data removed.

Production's prior binary, config, and environment have timestamped private
backups under `~/.local/state/opencode-cc/backups/`. Installation stopped only
`opencode-cc.service`, atomically replaced the executable, and started the unit.
A plain overwrite is insufficient because two running Morph adapters share the
same binary; atomic replacement leaves their old mapped inode running. The live
health endpoint and all seven real inference checks passed; no rollback was
needed for the successful deployment. Morph processes were not restarted.

A later continuation encountered an unstable WARP tunnel and failed staging with
502/EOF; deployment stayed blocked until fresh checks passed. WireGuard is not
supported by this installed client's local proxy mode. HTTP/2-only MASQUE did not
remain reliable in that test; the final working configuration restores MASQUE's
default automatic HTTP/3/HTTP/2 fallback. Do not change tunnel settings while an
inference or connectivity probe is in flight.

WARP and OAuth reduce the observed failure modes; they do not guarantee upstream
availability. If the CLI connection expires or is revoked, reconnect it through
OpenCode. If WARP is unavailable, these explicitly proxied requests fail rather
than silently changing network paths. The 429 retry plugin is separate and cannot
extend provider quota.
