# Deployment of the patched Wraient fork

The complete, maintained setup guide is **[docs/setup/FRIENDS.md](docs/setup/FRIENDS.md)**.
It is designed to be handed to a friend's agent and covers all prerequisites,
private configuration templates, the exact CPA build, Bun, auth, WARP, phone
Tailscale egress, Telegram control, staging, tests, limitations and rollback.

Do not use old copied deployment snippets. In particular:

- Build this fork, branch `patch/opencode-user-agent`; upstream's GHCR image is
  not this patched build. There is no Makefile: build the Vite UI, copy its dist
  to `internal/assets/dist`, then build Go (1.24+).
- Default new installs to `127.0.0.1:8787`, a private panel password, and
  `require_api_key=true`. Create CPA/client keys in the panel.
- Explicitly pass `-data` in the service. The reference data is `~/data`, and
  enabled saved `upstreams` entries override legacy env upstream/key settings.
- DS helper source and service: `scripts/ds-flash-gateway/`. Production helper
  uses :18790; new installs start direct-only with auto-reset off. See the guide
  before enabling phone egress or Telegram resets.
- Exact CPA recovery requires the local patch bundle in `contrib/cliproxyapi/`.
- WARP is optional SOCKS5 :40000 in local proxy mode. Phone :18889 is HTTP
  CONNECT. The Bun helper explicitly uses phone/direct, not the Go relay's
  WARP environment. `NO_PROXY` can make Go requests bypass WARP.

## Updating Wraient's existing production

Follow root `AGENTS.md` as well as the guide. Do not change the running
production settings just because new-install examples use safer defaults.

1. Check `git status` so the deployed source is known.
2. `go build ./... && go test ./...`; build `/tmp/opencode-cc-stage`.
3. Check listeners. Stage on **18789 only when free**, isolated data/config.
   **18787 is Morph; 18790 is the production helper.** Never stop those to
   free a test port. Use `scripts/test_ds_flash_chain.py` for the exact CPA
   alias/file tools, brief and sustained outages and same-process recovery.
4. When staging is green, back up current private config/binary, stop only
   `opencode-cc.service`, install to `~/.local/bin/opencode-cc.new` (0755),
   atomically rename over `~/.local/bin/opencode-cc`, start the unit. Morph also
   executes the old inode; never overwrite it in place or stop Morph to deploy.
5. Smoke `/api/health` and a real `/v1/messages` request, then verify the live
   CPA file-tool flow. Health alone does not prove inference/recovery.
6. Stop only your staging processes and remove private temporary data. Keep
   usable rollback backups; do not commit credentials or databases.

Historical accepted tests and subsequent stream failure repair are recorded in
[the diagnostic report](docs/diagnostics/2026-10-24-warp-api-check.md). Its earlier
hypotheses are retained as history and are superseded where later sections say
so. The later monitoring preference is **hourly**, including after failures;
old five-minute watcher text is historical. No watcher is installed by this repo.
