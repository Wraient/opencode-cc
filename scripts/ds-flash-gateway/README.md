# DS Flash Free transport

For a new laptop, follow [the complete installation guide](../../docs/setup/FRIENDS.md). The supplied service file records the reference laptop's phone/reset settings. Override it to `DS_FLASH_ROUTES=direct` and `DS_FLASH_AUTO_RESET=0` before first start unless your own phone and reset integration are already configured and authorized.

Opt-in adapter for **deepseek-v4.1-flash-free only**. The Go relay resolves the
credential against its original trusted HTTPS origin, then sends that model's
Chat request to this loopback Bun transport. Other models and origins keep
their existing transport. The helper holds no credentials on disk and never
refreshes OAuth or changes provider/model/account.

The verified compatibility path retains client instructions, history and tool
schemas after the pinned OpenCode system prelude, consolidates system messages,
uses native-style project/session metadata, and sends with Bun fetch. This adds
about 2–3K context tokens. The pinned prelude includes the public management
tool catalog: it does not register or execute those tools. Real Claude Write
and Read were verified through the exact CPA alias. See `manifest.json` and the
upstream MIT license. Updating the prelude requires new real-harness validation.

Sessions stay sticky across tool turns and refresh protocol timestamps every
four minutes. Prompt cache keys are retained. Actual requests try the current
healthy egress first, then the other configured egress (phone HTTP proxy and
direct). A successful response selects that route for subsequent calls. No
idle model probes consume allowance. Header acquisition is bounded to 30s per
route; a healthy generation can continue streaming beyond that.

401/403 and non-free-limit 429 responses are passed through. If both routes
return the known free-limit response or cannot connect, the original error and
Retry-After remain visible to CPA. Its existing 180s hold remains intact.

With `DS_FLASH_AUTO_RESET=1`, all-route failure also starts one background phone
reset using the user's Telegram IP-reset skill. Resets are single-flight and
persistently limited to one per 30 minutes. The script's private output is
discarded. Recovery is independently checked with HTTPS egress and, where a
pre-reset address was available, a changed IP. The state file stores only the
last-reset time. WARP and Morph services are not restarted or reconfigured.

## Installation and rollback

Run the project build/test/staging deployment procedure first. Copy this
directory plus verified Bun 1.4.2 into a versioned directory beneath
`~/.local/lib/opencode-cc-ds-flash/`, then atomically select it with `current`.
The Bun runtime was downloaded from the official `oven-sh/bun` 1.4.2 baseline
release; archive SHA256:
`c678040f14fe0440eb839d37cbd0ce4c051a32da72806ac97de6a6aab6bf728f`.
Keep the source/version/download recipe in recovery manifests; the runtime is
replaceable and need not be backed up.

Install `opencode-cc-ds-flash.service` in user systemd and `opencode-cc-dropin.conf`
as `~/.config/systemd/user/opencode-cc.service.d/ds-flash.conf`. Enable/start the
helper, atomically deploy the staged relay binary, then restart opencode-cc.
Do not stop Morph. The helper port is 18790; staging relay remains 18789.

To disable, remove only the DS drop-in, reload user systemd and restart
opencode-cc, then disable/stop the helper. Restore the previous versioned helper
and relay binary for a complete rollback. Credentials and CPA configuration
do not need changes.

Validation: `go test ./...` and
`bun test scripts/ds-flash-gateway/gateway.test.mjs`, followed by real Claude Code
file-tool tasks through CPA `deepseek-v4.1-flash-oc`, a brief outage, a sustained
outage and recovery within the same CPA process. A single model reply alone
does not establish reliability. Genuine account/network limits can still be
returned; this adapter does not create free allowance.

Reproduce real acceptance with the installed gateway and staged relay binary:

```
python scripts/test_ds_flash_chain.py --credential saved --out /tmp/ds-proof.jsonl
python scripts/test_ds_flash_chain.py --credential active --out /tmp/ds-proof.jsonl
python scripts/test_ds_flash_chain.py --fault-seconds 8 --out /tmp/ds-proof.jsonl
python scripts/test_ds_flash_chain.py --fault-seconds 195 --out /tmp/ds-proof.jsonl
```

The runner reserves 18789/18319/18794, creates isolated CPA/data/workspaces,
allows only Read/Write for its generated file task, and cleans up its own
processes. Its controlled outage is upstream of the helper, so it does not
trigger Telegram resets or alter production CPA. Long tests preserve one CPA
process across outage and recovery.
# Stream failures (runtime 20261024-v3)

Response headers alone do not establish successful inference. The gateway guards
SSE response reads: a late upstream disconnect emits an explicit API error and
logs sanitized `transport_failure` metadata with `phase=response_stream`. The
Go scanner rejects that envelope, including after partial output. The next
attempt prefers the alternate configured route. No partial generation or tool
call is replayed inside the gateway, and genuine quota/auth responses remain
unchanged. Network reliability failures still reset the monitoring proof window.
