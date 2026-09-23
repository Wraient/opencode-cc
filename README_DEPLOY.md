# opencode-cc — local OpenCode Zen proxy (patched)

Local proxy on `http://127.0.0.1:8787` that lets any harness (grok, codex, claude,
opencode, ...) use OpenCode Zen models with **pure passthrough** (no system-prompt
bloat) while sending a **genuine opencode User-Agent** upstream so rate limits match
a real opencode client.

Upstream: `https://opencode.ai/zen` (key taken from `env` file below).
Repo (our patched fork): https://github.com/Wraient/opencode-cc
(default branch `patch/opencode-user-agent`; upstream:
https://github.com/Kiowx/opencode-cc)

> **Current free-harness patch (2026-09-23):** free models require the stock
> OpenCode installation UA `opencode/latest/<version>/cli`, a canonical
> `ses_<12 lowercase hex><14 base62>` session, `stream:true`, and lowercase
> `shell`/`read` tool markers. The proxy now supplies all of these and
> aggregates forced SSE back to JSON for non-streaming clients. See
> `internal/proxy/free_tier.go`, `internal/proxy/stream_aggregate.go`, and
> `~/.config/opencode-cc/README.md`.

## File map

| What | Where |
|---|---|
| Binary | `~/.local/bin/opencode-cc` |
| Source (patched fork) | `~/Projects/opencode-cc` (branch `patch/opencode-user-agent`; origin = Wraient/opencode-cc, upstream = Kiowx/opencode-cc) |
| systemd user unit | `~/.config/systemd/user/opencode-cc.service` |
| API key env file | `~/.config/opencode-cc/env` (`ZEN_API_KEY=sk-...`, chmod 600) |
| Config backups | `~/.grok/config.toml.bak-occ-20260821`, `~/.codex/config.toml.bak-occ-20260821`, `~/.config/opencode/opencode.json.bak-occ-20260821` |

## The patch (why this binary differs from upstream)

All upstream `User-Agent` headers use the current OpenCode installation format:

```
opencode/latest/<installed-version>/cli
```

The current stable install is detected at startup (fallback `2.0.12`). The
implementation is in `internal/server/useragent.go` (`ocUA()`), used by every
LLM upstream path. The former `opencode/<version> ai-sdk/...` string is rejected
by the current free-tier gate; do not rebuild from vanilla source without this
patch.

## Service management

```bash
systemctl --user status opencode-cc     # state
journalctl --user -u opencode-cc -n 50  # last 50 log lines
systemctl --user restart opencode-cc    # restart after any change
```

Auto-start: unit is `enabled` + user has `Linger=yes`, so it starts at boot before
login and restarts itself on failure (`Restart=on-failure`, 3s).

## Endpoints

| Endpoint | Wire format | Used by |
|---|---|---|
| `POST /v1/chat/completions` | OpenAI | grok, opencode, anything OpenAI-compatible |
| `POST /v1/responses` | OpenAI Responses | codex |
| `POST /v1/messages` (+ `/count_tokens`) | Anthropic | claude |
| `GET /v1/models` | model list | all |
| `GET /healthz` | liveness | monitoring |

Client auth is not enforced (`require_api_key: false`) — any key string works
(`local` is used in configs).

## Wired harnesses

- **grok** (`~/.grok/config.toml`): default
  `muse-spark-1.3-contributor-free-occ` through
  `http://127.0.0.1:8787/v1` with `api_backend = "responses"` and
  `reasoning_effort = "xhigh"`. Backup:
  `~/.grok/config.toml.bak-muse-xhigh-20260924`.
- **claude** (`~/.claude/settings.json`): default
  `muse-spark-1.3-contributor-free` through `http://127.0.0.1:8787`, with
  `effortLevel = "xhigh"` and custom gateway model discovery enabled. Backup:
  `~/.claude/settings.json.bak-muse-xhigh-20260924`.
- **codex** (`~/.codex/config.toml`): provider `opencode-cc` (`wire_api = "responses"`),
  profiles `occ-muse`, `occ-xpreview`, `occ-pickle`, `occ-mimo`, `occ-hy3`,
  `occ-nemotron-ultra`, `occ-nemotron`, `occ-laguna`. Use: `codex -p occ-muse`.
- **opencode** (`~/.config/opencode/opencode.json`): provider `opencode-cc`
  (`@ai-sdk/openai-compatible`, baseURL `http://127.0.0.1:8787/v1`). Pick via `/models`.

Models added (all free tier): muse-spark-1.2-contributor-free, x-preview-f-free,
big-pickle, mimo-v2.5-free, hy3-free, nemotron-3-ultra-free,
nemotron-3.5-lightning-free, laguna-s-2.1-free.
Deliberately excluded: `muse-spark-1.2` (paid), `deepseek-v4-flash-free`
(unavailable upstream as of 2026-08-21).

## Troubleshooting

1. **Proxy down / connection refused**
   ```bash
   systemctl --user status opencode-cc
   journalctl --user -u opencode-cc --no-pager | tail -30
   systemctl --user restart opencode-cc
   curl -s http://127.0.0.1:8787/healthz
   ```
   Port clash: `ss -ltnp | grep 8787` — kill the stale process, then restart.

2. **"no upstream API key configured"**
   `~/.config/opencode-cc/env` is missing/empty. It must contain
   `ZEN_API_KEY=sk-...` (copy the sk- key from `~/.grok/config.toml` deepseek entry
   or `opencode auth` storage). Then `chmod 600` + restart.

3. **`CreditsError: Insufficient balance`**
   You picked a paid Zen model with a free-tier key. Switch to a `-free` model.

4. **"Model is unavailable"**
   Upstream dropped/renamed that model. Check live catalog:
   `curl -s http://127.0.0.1:8787/v1/models | python3 -m json.tool`
   then update the harness config entry.

5. **Free models return `403 FreeTierError`**
   The current binary must contain the OpenCode installation UA and free-tier
   request adapter. Check `strings ~/.local/bin/opencode-cc | grep
   'opencode/latest'`, then inspect `journalctl --user -u opencode-cc` for the
   upstream status. Direct curl is not a valid free-tier test because Zen
   requires the proxy's canonical session/stream/tool shape.

6. **Configs broken after an agent edited them**
   Restore from the `.bak-occ-20260821` files listed above, then validate:
   ```bash
   python3 -c "import tomllib;tomllib.load(open('/home/wraient/.codex/config.toml','rb'))"
   python3 -c "import tomllib;tomllib.load(open('/home/wraient/.grok/config.toml','rb'))"
   python3 -m json.tool ~/.config/opencode/opencode.json > /dev/null
   ```

## Rebuild from source

Use the safe staging sequence; never run a second binary on port 8787:

```bash
cd ~/Projects/opencode-cc
go build ./... && go test ./...
go build -o /tmp/opencode-cc-stage .
export $(grep -E '^(ZEN_API_KEY|OPENCODE_CC_)' ~/.config/opencode-cc/env | xargs)
OPENCODE_CC_LISTEN='127.0.0.1:18787' nohup /tmp/opencode-cc-stage \
  -data /tmp/cc-stage-data >/tmp/cc-stage.log 2>&1 &
# Exercise Claude Code, Grok Build, /v1/messages, /v1/chat/completions,
# and /v1/responses on port 18787 first.
systemctl --user stop opencode-cc
cp /tmp/opencode-cc-stage ~/.local/bin/opencode-cc
systemctl --user start opencode-cc
```

A pre-built patched copy may still exist at `/tmp/opencode/opencode-cc-patched`.

## Quick smoke test

```bash
curl -fsS http://127.0.0.1:8787/api/health
curl -sS -m 120 http://127.0.0.1:8787/v1/messages \
  -H 'content-type: application/json' -H 'Authorization: Bearer local' \
  -H 'anthropic-version: 2023-06-01' \
  -d '{"model":"muse-spark-1.3-contributor-free","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"Reply with exactly OK"}]}'
claude-oc -p 'Reply with exactly OK' --no-session-persistence --output-format json
grok -p 'Reply with exactly OK' --no-plan --max-turns 1 --output-format json
```
