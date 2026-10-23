# AGENTS.md — opencode-cc

## Live proxy: DO NOT touch directly

- The production proxy is `opencode-cc.service` (systemd user unit).
  Binary: `~/.local/bin/opencode-cc`, port **:8787**, data dir `~/data`.
- **Never run a second copy on :8787** (repo cwd, manual `go run`, another
  binary). The service will crash-loop with `bind: address already in use`
  and every editor client (grok/codex/opencode) loses its key. This has
  happened before — do not repeat it.

## Safe deploy procedure (mandatory)

1. `go build ./... && go test ./...` in the repo.
2. Build a staging binary: `go build -o /tmp/opencode-cc-stage .`
3. Check listeners first. **:18787 is now used by Morph**; do not stop or
   reuse it for staging. Use **:18789 only when free**, with an isolated data
   dir and key from env:
   ```
   export $(grep -E '^(ZEN_API_KEY|OPENCODE_CC_)' ~/.config/opencode-cc/env | xargs)
   OPENCODE_CC_LISTEN='127.0.0.1:18789' nohup /tmp/opencode-cc-stage -data /tmp/cc-stage-data >/tmp/cc-stage.log 2>&1 &
   ```
4. E2E-test through staging on **:18789** (chat + the feature under test).
5. Only when staging is green: stop `opencode-cc.service`, install staging
   to `~/.local/bin/opencode-cc.new` (mode 0755), atomically rename it over
   `~/.local/bin/opencode-cc`, then start the production unit. **Morph also
   executes this same binary**: a plain overwrite fails with "Text file busy"
   even after stopping production. Atomic replacement preserves the inode used
   by those running adapters; do not stop Morph to deploy this service.
6. Smoke-test live: `curl :8787/api/health` + one real `/v1/messages` call.
7. Kill staging (`pkill` with a bracket pattern like `[o]pencode-cc-stage`
   so pkill doesn't match its own command line) and remove `/tmp/cc-stage-data`
   if it holds junk.

## Video ingress limits (learned Sep 2026 — do not regress)

- Plain local `.mp4` paths in user text are read off disk and sent upstream
  as inline base64 `input_video`. Code: `internal/proxy/video.go`.
- Upstream `POST /v1/responses` degrades hard with inline size (staging
  probes): 2MB→11s, 5MB→18s, 7MB→65s w/ retry, **10MB→never responds**
  (~110s of header-timeout retries, then 502). There is NO duration limit —
  only size matters (bitrate × seconds).
- A hung video request holds 1 of only 2 muse-spark upstream slots
  (`museSparkUpstreamSlots`), so **one big video wedges all other threads**.
- Current guards: files over 4MB are ffmpeg-transcoded toward ~4MB
  (480p/crf28 → 360p/crf32, full duration kept; ffmpeg best-effort, never a
  hard dep); anything still over 8MB (`maxVideoBytes`) fails FAST with a
  clear error instead of hanging. Resolved videos are cached by
  path+size+mtime (4 entries) so repeat turns don't re-transcode.
- Explicit video markers expand leading `~` to home (e.g. `[[video <~/clip.mp4>]]`
  form — never write a literal path in docs: Claude Code injects AGENTS.md
  into user-role text and a literal marker 400s every request); a `document` URL block only counts
  as video when its media type is empty or `video/*` (else upstream tries to
  download it as media → `media_url_origin_error` 404).
- When touching video: E2E-test staging with the REAL 10MB file
  (`/home/wraient/Videos/video_2026-08-07_00-50-06.mp4`) via an isolated
  Claude Code (`HOME=/tmp/cc-iso-home`, staging as base URL) — small-file
  tests alone hide this trap.

## Notes

- Reliability acceptance must exercise the exact CPA alias through the full
  connection chain under a brief outage, a sustained outage, and recovery.
  A healthy daemon, direct relay success, or one successful model reply does
  not prove recovery. Keep genuine auth and quota cooldowns intact.

- Claude Code/DeepSeek integration repairs target the connection chain
  **Claude Code → CLIProxyAPI → opencode-cc → OpenCode Zen**. Keep the
  requested harness/model and use generated tasks as end-to-end connection
  evidence. Do not expand this into changes to DeepSeek itself or polishing
  the generated application's code.

- CPA holds Zen 429s (2026-10-22, CPA `8.0.4+local3`): the `claude-2` group
  (prefix `oc`, this relay) has `rate-limit-hold-seconds: 180`. CPA keeps a
  rate-limited request open and retries it with 2→4→8→15s backoff for up to
  3 min, and never stores a long cooldown. Zen's free-tier `Retry-After`
  counts to 00:00 UTC and is not a recovery time (81927s sent, cleared in
  <1h). A 429 that still reaches Claude Code means Zen limited this relay
  for the full 3 min. On 2026-10-22 only the WARP egress was limited: the
  same binary and credential without `HTTP(S)_PROXY` returned 200. See
  `~/.cli-proxy-api/README.md` Troubleshooting 10–12.

- Live config combines `~/.config/opencode-cc/env` with `~/data/config.json`
  (confirmed 2026-10-15). Inspect saved settings without printing credentials.
  A nonempty `upstreams` pool takes precedence over the legacy base URL;
  changing only the default or legacy env URL does not migrate that pool.
  Staging must source the same env file and validate relevant saved settings.
  The live saved upstream was migrated from `/zen` to `/inference` after
  staging tests on 2026-10-15; timestamped config/binary backups are retained.
- Uncommitted work may exist on `patch/opencode-user-agent`; check
  `git status` before building so you know what you're deploying.

## Active CLI authentication and WARP

- The legacy `~/.local/share/opencode/auth.json` is not proof of the credential
  currently used by OpenCode v2. Inspect the active `opencode` row in its
  credential database read-only; compare identities without printing tokens.
- `opencode://active` in a saved upstream's `api_key` opts into reading that
  current credential for each request. OpenCode owns OAuth refresh; never copy
  or reuse its rotating refresh token or modify its credential database.
- WARP runs as a local SOCKS proxy on `127.0.0.1:40000`. The relay uses explicit
  `HTTP_PROXY`/`HTTPS_PROXY` settings; other apps remain direct. WARP proxy mode
  in this installed release requires MASQUE. Use `warp-cli status` and a
  proxied Cloudflare trace to verify the tunnel before inference tests.

## T3 Claude CPA context policy (2026-10-23)

- Keep one Claude CPA provider (`claudeAgent_2`), including DeepSeek; do not
  create a separate DeepSeek provider.
- Preserve shared defaults: context maximum 460000, auto-compact window 400000.
  DeepSeek `deepseek-v4.1-flash-oc` alone must launch with maximum 260000 and
  auto-compact window 200000. Do not change other models' limits for DeepSeek.
- The provider executable is `~/.local/bin/claude-cpa-context`, sourced from
  `scripts/claude-cpa-context`. It overrides only DeepSeek's launch environment
  and otherwise forwards arguments and environment to the real Claude CLI.
- Claude's maximum-context environment is fixed for a process. T3 can switch
  models live; stop/relaunch the thread when switching between DeepSeek and
  other models so the launcher applies the correct limits. Do not claim this
  launch wrapper applies context limits dynamically to live model switches.
- These client settings do not increase upstream model capacity. DeepSeek's
  measured free-route input ceiling is about 261K.
