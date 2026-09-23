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
3. Run staging on a **spare port with an isolated data dir**, key from env:
   ```
   export $(grep -E '^(ZEN_API_KEY|OPENCODE_CC_)' ~/.config/opencode-cc/env | xargs)
   OPENCODE_CC_LISTEN='127.0.0.1:18787' nohup /tmp/opencode-cc-stage -data /tmp/cc-stage-data >/tmp/cc-stage.log 2>&1 &
   ```
4. E2E-test through staging on **:18787** (chat + the feature under test).
5. Only when staging is green: `systemctl --user stop opencode-cc`,
   copy the staging binary over `~/.local/bin/opencode-cc`
   (cp fails with "Text file busy" while the service runs — stop first),
   then `systemctl --user start opencode-cc`.
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
- `[[video ~/...]]` markers expand `~`; a `document` URL block only counts
  as video when its media type is empty or `video/*` (else upstream tries to
  download it as media → `media_url_origin_error` 404).
- When touching video: E2E-test staging with the REAL 10MB file
  (`/home/wraient/Videos/video_2026-08-07_00-50-06.mp4`) via an isolated
  Claude Code (`HOME=/tmp/cc-iso-home`, staging as base URL) — small-file
  tests alone hide this trap.

## Notes

- Live config is env-only (`~/.config/opencode-cc/env`); there is no
  `config.json` in `~/data`. Staging must source the same env file.
- Uncommitted work may exist on `patch/opencode-user-agent`; check
  `git status` before building so you know what you're deploying.
