# opencode-cc — local OpenCode Zen proxy (patched)

Local proxy on `http://127.0.0.1:8787` that lets any harness (grok, codex, claude,
opencode, ...) use OpenCode Zen models with **pure passthrough** (no system-prompt
bloat) while sending a **genuine opencode User-Agent** upstream so rate limits match
a real opencode client.

Upstream: `https://opencode.ai/zen` (key taken from `env` file below).
Repo (our patched fork): https://github.com/Wraient/opencode-cc
(default branch `patch/opencode-user-agent`; upstream:
https://github.com/Kiowx/opencode-cc)

## File map

| What | Where |
|---|---|
| Binary | `~/.local/bin/opencode-cc` |
| Source (patched fork) | Local clone `/tmp/opencode/opencode-cc` (remotes: `origin` = Wraient/opencode-cc, `upstream` = Kiowx/opencode-cc; **volatile — see Rebuild**) |
| systemd user unit | `~/.config/systemd/user/opencode-cc.service` |
| API key env file | `~/.config/opencode-cc/env` (`ZEN_API_KEY=sk-...`, chmod 600) |
| Config backups | `~/.grok/config.toml.bak-occ-20260821`, `~/.codex/config.toml.bak-occ-20260821`, `~/.config/opencode/opencode.json.bak-occ-20260821` |

## The patch (why this binary differs from upstream)

All upstream `User-Agent` headers were changed from `opencode-cc/1.x` to the real
opencode fingerprint:

```
opencode/<installed-version> ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.13
```

Version is auto-detected at startup via `opencode --version` (fallback `1.18.15`).
Patch lives in `internal/server/useragent.go` (`ocUA()`), used by `proxy.go`,
`openai_proxy.go`, `responses_proxy.go`, `web_search_proxy.go`.
If you ever rebuild from unpatched upstream source, the UA reverts to
`opencode-cc/1.x` — re-apply the patch or copy `useragent.go` + call sites.

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

- **grok** (`~/.grok/config.toml`): `[model."*"]` entries named `* (OpenCode CC)`
  plus `muse-spark-1.2-contributor-free-occ` (renamed to avoid clashing with the
  older cliproxy entry). Backend: `chat_completions`.
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

5. **Rate limits suddenly worse than opencode's**
   Check the binary still has the UA patch:
   `strings ~/.local/bin/opencode-cc | grep 'provider-utils'` should hit.
   If not, rebuild per Rebuild section.

6. **Configs broken after an agent edited them**
   Restore from the `.bak-occ-20260821` files listed above, then validate:
   ```bash
   python3 -c "import tomllib;tomllib.load(open('/home/wraient/.codex/config.toml','rb'))"
   python3 -c "import tomllib;tomllib.load(open('/home/wraient/.grok/config.toml','rb'))"
   python3 -m json.tool ~/.config/opencode/opencode.json > /dev/null
   ```

## Rebuild from source

```bash
git clone https://github.com/Wraient/opencode-cc /tmp/opencode/opencode-cc
git -C /tmp/opencode/opencode-cc remote add upstream https://github.com/Kiowx/opencode-cc
# patch is already on the default branch (patch/opencode-user-agent) — no manual re-apply needed
cd /tmp/opencode/opencode-cc && go build -o ~/.local/bin/opencode-cc .
systemctl --user restart opencode-cc
```

A pre-built patched copy may still exist at `/tmp/opencode/opencode-cc-patched`.

## Quick smoke test

```bash
curl -s -m 30 http://127.0.0.1:8787/v1/chat/completions \
  -H 'content-type: application/json' -H 'Authorization: Bearer local' \
  -d '{"model":"nemotron-3.5-lightning-free","max_tokens":10,"messages":[{"role":"user","content":"say ok"}]}'
```
