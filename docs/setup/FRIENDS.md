# Set up Wraient's relay stack on another laptop

This is the agent handoff and installation guide for the **patched Wraient fork**,
branch `patch/opencode-user-agent`. Read this before executing setup commands.
The target is Linux with systemd user services. Other operating systems need
adapted service/network instructions; they have not been accepted here.

The exact tested chain is:

```text
Claude Code -- deepseek-v4.1-flash-oc --> CLIProxyAPI :8317 (patched)
  --> opencode-cc :8787 -- deepseek-v4.1-flash-free --> Bun helper :18790
  --> phone HTTP CONNECT :18889 OR direct laptop Internet --> OpenCode Zen

Other models: opencode-cc Go transport --> its configured HTTPS_PROXY / NO_PROXY
Optional WARP: selected application --> SOCKS5 :40000 --> Cloudflare --> Internet
Phone route: :18889 --> isolated userspace Tailscale --> Android exit node --> cellular
Telegram control: local script --> Bot API --> phone automation --> cellular reset
```

**Ready for a technical friend's supervised installation, not a turnkey or
unlimited free service.** Use your own account, key, phone, tailnet and bot. A
working key, healthy daemon, changed IP or HTTP 200 headers alone do not prove
successful inference. Free model eligibility, authentication, network conditions
and quota are separate. Provider changes can break this compatibility path.

Latest validation: [2026-10-27 setup evidence](../evidence/2026-10-27-friend-setup.json).
Real saved-key/active-OAuth file tasks, brief outage and sustained outage/recovery
passed on the reference laptop with a freshly built CPA and separate direct-only
helper. Direct and phone HTTPS traces passed; WARP HTTPS timed out, and its CLI
requested Terms acceptance. No new device enrollment, real Telegram reset, full
container chain or second physical laptop installation was tested.

## 1. What is included and what is private

| Component | Included here | Friend must supply |
|---|---|---|
| Go relay, web dashboard, translations, auth | Source and tests | Go 1.24+, Node 20+/npm to build UI |
| Exact DS Flash compatibility helper | `scripts/ds-flash-gateway/`, pinned prelude + MIT notice | Bun; validated production version 1.4.2 |
| CPA retry behavior | `contrib/cliproxyapi/` patch bundle + example config | Upstream v8.0.4 source, Go toolchain requested by its go.mod |
| Claude Code | Acceptance runner and wrappers | Installed official CLI; tested here with 2.1.310 |
| OpenCode account | Read-only active credential resolver | Own Zen key or logged-in OpenCode v2; native fixture was 2.1.2 |
| WARP | Instructions below | Own installed/registered WARP; optional |
| Phone HTTP proxy | Userspace Tailscale recipe below | Own tailnet and approved Android exit node |
| Telegram reset | `contrib/telegram-ip-reset/` sender and template | Own bot/chat and **phone-side automation**; no phone automation export is included |
| Credentials/state | None | Private env files, SQLite, bot tokens, Tailscale enrollment |

Do not copy Wraient's `~/data`, OpenCode database, WARP registration, Tailscale
state, `~/.local/state/morph-relay`, Telegram env files, CPA auth pool or editor
settings. Historical diagnostics contain machine paths, status/timing evidence
and short credential fingerprints; they are not installation configuration.

Agent rule: inspect existing listeners/services/config before installing. Do not
stop other applications, switch models/accounts, enroll another person's node,
change system-wide VPN routing, or trigger a phone reset as an installation test.
If a required private component is missing, report it and keep its feature off.

## 2. Ports and isolation

| Port | Meaning on the reference laptop |
|---|---|
| 8787 | Production relay; exactly one process |
| 8317 | Production CPA |
| 18790 | Production DS helper; loopback only |
| 18889 | Phone HTTP CONNECT proxy; loopback only |
| 40000 | Optional WARP SOCKS5 proxy |
| 18789 | Isolated staging relay |
| 18319 | Isolated acceptance CPA |
| 18794 | Acceptance fault injector |
| 18787 / 18788 / 18890 | Morph services on Wraient's machine; leave alone |

```bash
ss -ltnp
systemctl --user status opencode-cc opencode-cc-ds-flash cliproxyapi --no-pager
```

Check ports before starting. Acceptance reserves 18789/18319/18794 and refuses
occupied ports. The helper and relay have separate config; changing the relay
listen address does not move the helper. No public hostname or Cloudflare Worker
is required for this laptop installation.

## 3. Build the fork and install privately

Commands below assume Bash, run from the repository root, and a fresh setup.
Existing installations must use the staging/atomic update steps in section 10.
There is no Makefile. The upstream GHCR image does **not** include this fork's
unpublished/local fixes or the Bun sidecar; build this checkout yourself.

```bash
git clone --branch patch/opencode-user-agent https://github.com/Wraient/opencode-cc.git
cd opencode-cc
(cd web && npm ci --no-audit --no-fund && npm run build)
mkdir -p internal/assets/dist
cp -a web/dist/. internal/assets/dist/
go build ./...
go test ./...
go build -o /tmp/opencode-cc-stage .
install -d -m 700 "$HOME/.config/opencode-cc" "$HOME/data"
install -d "$HOME/.local/bin" "$HOME/.config/systemd/user"
install -m 755 /tmp/opencode-cc-stage "$HOME/.local/bin/opencode-cc.new"
mv -f "$HOME/.local/bin/opencode-cc.new" "$HOME/.local/bin/opencode-cc"
```

Create `~/.config/opencode-cc/env` using a private editor, mode **0600**:

```dotenv
OPENCODE_CC_LISTEN=127.0.0.1:8787
OPENCODE_CC_PANEL_TOKEN=REPLACE_WITH_YOUR_OWN_LONG_RANDOM_PANEL_PASSWORD
OPENCODE_CC_REQUIRE_API_KEY=true
OPENCODE_CC_UPSTREAM=https://opencode.ai/inference
```

Create `~/data/config.json`, mode **0600**, with your own key in place of the
placeholder (or the literal reference `opencode://active`, described below):

```json
{
  "upstreams": [{
    "name": "my-zen",
    "base_url": "https://opencode.ai/inference",
    "api_key": "REPLACE_WITH_YOUR_OWN_ZEN_KEY",
    "enabled": true
  }],
  "default_model": "deepseek-v4.1-flash-free",
  "log_requests": false
}
```

A nonempty enabled `upstreams` pool takes precedence over the legacy
`ZEN_API_KEY`/`OPENCODE_CC_UPSTREAM` pair. Editing only env does not migrate that
pool. UI settings are persisted in this file; process env overrides supported
fields at load. Never commit this directory. Logging can capture prompts, code,
tool arguments and responses; turn it on only when needed and protect the DB.

Install this user unit as `~/.config/systemd/user/opencode-cc.service`:

```ini
[Unit]
Description=Local OpenCode Zen relay
After=network-online.target

[Service]
ExecStart=%h/.local/bin/opencode-cc -data %h/data
EnvironmentFile=%h/.config/opencode-cc/env
UMask=0077
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
```

Use an explicit `-data`: the reference machine historically relied on its service
working directory yielding `~/data`; a different cwd silently selects a different
config/database. Enable boot-before-login only if wanted with
`loginctl enable-linger "$USER"` (may require administrator authorization).

### Key vs active OAuth

`opencode://active` reads exactly one active OpenCode v2 `credential` row for
integration `opencode`, read-only, on each upstream selection. It is accepted
only for HTTPS `opencode.ai`. Default DB:
`${XDG_DATA_HOME:-$HOME/.local/share}/opencode/opencode.db`; override with
`OPENCODE_CC_OPENCODE_DB=/absolute/path/to/opencode.db` in the service env.

The official OpenCode CLI owns login/refresh. The relay does not refresh OAuth
and rejects expired or nearly expired access tokens. Use the CLI to restore the
connection. The legacy `~/.local/share/opencode/auth.json` can contain an expired,
different credential: it is not evidence of the active v2 identity. Never copy
refresh tokens or write its credential database. A static Zen key avoids that
refresh dependency but still has the account's real permissions and limits.

## 4. Install the DS Bun helper

The helper intercepts **only** POST Chat requests for
`deepseek-v4.1-flash-free` on the allowed `https://opencode.ai` inference/Zen
paths. Other models and origins use the existing Go transport. It is not a
standalone account/model router.

Install Bun from its [official installation instructions](https://bun.sh/docs/installation),
verify the download and `bun --version`. Production here uses **1.4.2**; its
Linux x64 baseline archive checksum and provenance are in the helper README.
That checksum is not valid for ARM or other archives. Do not blindly install
whatever latest release happens to be available and call it the same runtime.

```bash
# Set this to the verified executable, not a directory.
BUN_BIN="$HOME/.bun/bin/bun"
"$BUN_BIN" --version
"$BUN_BIN" test scripts/ds-flash-gateway/gateway.test.mjs
release_name="$(date -u +%Y%m%dT%H%M%SZ)"
release_root="$HOME/.local/lib/opencode-cc-ds-flash"
install -d "$release_root/releases/$release_name"
cp scripts/ds-flash-gateway/{gateway.mjs,system.txt,manifest.json,LICENSE} "$release_root/releases/$release_name/"
install -m 755 "$BUN_BIN" "$release_root/releases/$release_name/bun"
ln -s "releases/$release_name" "$release_root/current.new"
mv -Tf "$release_root/current.new" "$release_root/current"
install -m 644 scripts/ds-flash-gateway/opencode-cc-ds-flash.service "$HOME/.config/systemd/user/"
install -d "$HOME/.config/systemd/user/opencode-cc-ds-flash.service.d"
```

The supplied unit records Wraient's production settings (`phone,direct`, auto
reset enabled). **Before first start on a friend's laptop**, create
`~/.config/systemd/user/opencode-cc-ds-flash.service.d/local.conf`:

```ini
[Service]
Environment=DS_FLASH_ROUTES=direct
Environment=DS_FLASH_AUTO_RESET=0
```

This starts with a working, independent local route and does not assume a phone
or send Telegram messages. Section 7 explains when to enable the full setup.

```bash
install -d "$HOME/.config/systemd/user/opencode-cc.service.d"
install -m 644 scripts/ds-flash-gateway/opencode-cc-dropin.conf "$HOME/.config/systemd/user/opencode-cc.service.d/ds-flash.conf"
systemctl --user daemon-reload
systemctl --user enable --now opencode-cc-ds-flash opencode-cc
curl --noproxy '*' -fsS http://127.0.0.1:18790/health
curl --noproxy '*' -fsS http://127.0.0.1:8787/api/health
```

The helper adds a pinned OpenCode prelude (about 2–3K context tokens), consolidates
text system messages, and retains client instructions, tool schemas/history,
limits and reasoning fields. Its prelude's public tool catalog does not register
or execute those tools. Session affinity is scoped by authorization + incoming
session; metadata timestamps refresh after four minutes. This is observed
compatibility behavior, not a provider-supported free-client contract.

It tries the preferred route, then the other configured route for a connection
failure or known `FreeUsageLimitError`. A successful route becomes preferred.
401/403 and other 429s pass through. All-route quota failure preserves the
upstream response and Retry-After. It does not replace accounts/models or create
allowance. Header acquisition is bounded to 30 seconds per route; a healthy
stream can continue longer. Body limit is 8 MiB. The eight-request admission
counter covers header acquisition, **not eight guaranteed active-generation
slots**. Bun idle timeout is 255 seconds; other upstream/client limits still
apply. Late SSE disconnects become explicit API errors, choose the alternate
route for the next attempt, and do not replay partial text/tool calls internally.

## 5. CPA: the local patches are required for the exact recovery behavior

Follow [the bundled CPA build/config instructions](../../contrib/cliproxyapi/README.md).
Stock CPA v8.0.4 does not implement `rate-limit-hold-seconds`; copying the YAML
field into stock CPA can fail configuration validation. Do not claim outage
recovery equivalence from a generic CPA release.

The reference patch sets a 180-second hold **only** on the relay's `claude-2`
group. For opted-in credentials it retries 429s with 2→4→8→15-second backoff,
returns the real last error after the hold expires, and caps/restores stale
local 429 cooldown state under that policy. This intentionally changes local
retry scheduling; it does not clear upstream quota or blanket-disable cooling
for other providers. Unrelated auth/quota errors must remain visible.

Use the example YAML in `contrib/cliproxyapi/config.example.yaml`, replacing
both keys: the client→CPA key and the CPA→relay key created in the panel. They
are different from the upstream Zen key. Alias:
`deepseek-v4.1-flash-oc` → `deepseek-v4.1-flash-free`. If other providers share
CPA, do not replace their config; merge only the dedicated relay group. Local
production uses port 8317; staging acceptance uses 18319.

Claude speaks Anthropic to CPA at `http://127.0.0.1:8317` (no `/v1` suffix).
Set `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` to the private CPA client key,
and launch with `--model deepseek-v4.1-flash-oc`. Keep fast mode off. For this
model set `CLAUDE_CODE_MAX_CONTEXT_TOKENS=260000` and
`CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000`. These are launch-time limits and do
not increase upstream capacity (measured input ceiling was about 261K).
Restart the Claude process when switching between DeepSeek and other models.

The reference T3 setup uses one Claude CPA provider (`claudeAgent_2`), with
shared defaults 460000 / 400000 for other models. This is a local convention,
not a requirement to create or overwrite a friend's T3 configuration.
`scripts/claude-cpa-context` contains Wraient's absolute Claude executable path;
edit a personal installed copy for your machine. Do not point it at itself.
`scripts/claude-cpa` assumes `~/.cli-proxy-api/api-key.txt` and a models.env slot
configuration; use its documented env overrides or configure your harness
directly. Neither wrapper installs CPA or creates credentials.

## 6. WARP: optional application proxy, separate from the phone

WARP encrypts selected traffic to Cloudflare and gives it a Cloudflare Internet
exit. It is not OpenCode authentication, an Android exit, an inbound tunnel, or
a guarantee of free-model eligibility. In **local proxy mode** only applications
explicitly pointed at its listener use it; the rest of the laptop retains its
normal routing. See [Cloudflare's mode documentation](https://developers.cloudflare.com/warp-client/warp-modes/)
and [Linux installation/registration](https://developers.cloudflare.com/warp-client/get-started/linux/).

Reference machine: Arch Linux, reviewed `cloudflare-warp-bin` AUR recipe wrapping
the official binary, version **2026.8.2115.0**, system `warp-svc.service`, consumer
registration, SOCKS5 `127.0.0.1:40000`. Arch packaging is community-maintained;
verify its source/checksum. Other distros should use Cloudflare's package repo.
Register your own client; do not clone Wraient's registration state. The CLI
may require first-run Terms acceptance interactively before status/configuration.

After installing the package, on a new consumer registration:

```bash
sudo systemctl enable --now warp-svc
warp-cli registration new   # only if not already registered
warp-cli mode proxy
warp-cli proxy port 40000
warp-cli tunnel protocol set MASQUE
warp-cli connect
warp-cli status
curl --noproxy '' --max-time 15 --proxy socks5h://127.0.0.1:40000 https://www.cloudflare.com/cdn-cgi/trace
```

Require `warp=on` from that **proxied** trace; status alone is insufficient. The
installed reference version requires MASQUE for local proxy mode. Its automatic
HTTP/3/HTTP/2 fallback is retained; don't force protocol, MTU or endpoint tweaks
from old experiments into persistent configuration. CLI syntax changes across
versions: consult `warp-cli mode --help` / `warp-cli tunnel protocol --help`.
Disconnect with `warp-cli disconnect`; this stops the optional proxy's egress.

To opt **the Go relay** into WARP, put these in its private env and restart:

```dotenv
HTTP_PROXY=socks5h://127.0.0.1:40000
HTTPS_PROXY=socks5h://127.0.0.1:40000
NO_PROXY=127.0.0.1,localhost,::1
```

Check lowercase proxy variables too; avoid conflicting values. `NO_PROXY` can
bypass an explicit proxy: adding `opencode.ai` makes matching Zen requests go
direct. The reference production process had that bypass during investigation;
its proxy env therefore did not establish that Zen used WARP. Inspect the
**running service** environment locally without printing credentials; a shell's
environment or the env file alone may differ from the process until restart.
Go's environment proxy selection is cached; restart after a change.

The Bun helper has explicit `proxy` values: `phone` means HTTP
`http://127.0.0.1:18889`, `direct` means `proxy:false`. **It does not support a
`warp` route**, and relay HTTPS_PROXY does not change these helper choices.
Do not label DeepSeek as WARP-routed simply because the Go relay env uses WARP.
`socks5h` asks curl to resolve names through SOCKS; do not use that scheme for
the phone's HTTP CONNECT listener.

Observed caveats: WARP sometimes reported connected while HTTPS timed out;
a successful trace did not prove inference. WARP egress was 429-limited while
direct worked in one controlled comparison. Neither observation is universal.
There are no model probes on idle; don't add tight retry/monitor loops.

## 7. Phone egress and Telegram recovery

### Isolated Tailscale HTTP proxy

Use your own Android phone with cellular data and a working Tailscale exit-node
configuration. Enable/approve that exit node and tailnet permissions using
[Tailscale's exit-node instructions](https://tailscale.com/docs/features/exit-nodes).
On Android this is userspace forwarding; power saving, VPN permission, carrier
NAT, IPv4/IPv6 and mobile data coverage affect availability. Keep the phone
powered and allow the relevant apps to run in the background.

The laptop uses an **additional userspace tailscaled daemon**, with its own
state and socket, so choosing the phone exit only affects this proxy. It does
not replace the laptop's ordinary Tailscale daemon or default route. See
[Tailscale userspace proxy documentation](https://tailscale.com/docs/concepts/userspace-networking).

Example user unit `~/.config/systemd/user/phone-egress.service` (install Tailscale
first; adapt binary paths for your distro):

```ini
[Unit]
Description=Isolated phone egress HTTP proxy
After=network-online.target

[Service]
ExecStart=/usr/bin/tailscaled --tun=userspace-networking --statedir=%h/.local/state/phone-egress --socket=%h/.local/state/phone-egress/tailscaled.sock --outbound-http-proxy-listen=127.0.0.1:18889
UMask=0077
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
```

```bash
install -d -m 700 "$HOME/.local/state/phone-egress"
systemctl --user daemon-reload
systemctl --user enable --now phone-egress
# Enroll interactively in YOUR tailnet; approve/login at the private URL shown.
tailscale --socket="$HOME/.local/state/phone-egress/tailscaled.sock" up --accept-dns=false
# Substitute your own approved phone's name or Tailscale address.
tailscale --socket="$HOME/.local/state/phone-egress/tailscaled.sock" set --exit-node=YOUR_PHONE
curl --noproxy '' --max-time 15 --proxy http://127.0.0.1:18889 https://www.cloudflare.com/cdn-cgi/trace
```

Keep enrollment URLs, auth keys and daemon state private. This loopback proxy is
not authenticated: do not bind it to the LAN or publish it through a tunnel.
Verify real HTTPS egress, not just a health port. The trace reveals the phone's
public exit IP; keep personal IP logs out of public reports. A cellular IPv4 can
change between calls even without a reset; IPv6 behavior may differ.

**Reference-only context:** Wraient's proxy is provided by
`morph-tailscale.service` under `~/.local/state/morph-relay`, using the saved
`ts-sidecar` identity while its original AWS instance is off. Running that
identity in both places causes a collision. Friends must enroll a fresh node,
not copy it. Morph's relay/harbor/opencode/tunnel/Worker/VPC integration is a
separate product and is not needed here. The old farm path on port 18888 is
also separate; it is not the helper's 18889 phone listener.

Once phone egress is verified, change the helper override to:

```ini
[Service]
Environment=DS_FLASH_ROUTES=phone,direct
Environment=DS_FLASH_AUTO_RESET=0
```

Restart only the helper. Route order is startup preference; successful failover
can make direct preferred. It does not force all laptop traffic through cellular.

### Telegram is a control channel, not the data proxy

The laptop sends a fixed message through Telegram Bot API to a private chat.
Automation **on the phone** watches the authorized chat/sender and exact text,
then cycles cellular connectivity. Depending on Android/version this may need
Tasker/MacroDroid plus permitted ADB/root/device-owner actions; a normal app
cannot universally toggle airplane mode. There is no universal Android automation
export in this repository. Configure and validate that piece on your own device.
A bot message being sent successfully does not prove the phone executed it.

Use a dedicated bot/chat and restrict who can trigger the phone action. Decide
which chat the phone watches, its exact trigger, how long the mobile connection
is down, and how its Tailscale connection returns. Keep ordinary notifications
separate: Wraient also has `~/.codex/telegram.env` for blocker notifications;
that is **not** the helper's reset configuration and is not included.

Install [the bundled sender and private config template](../../contrib/telegram-ip-reset/README.md).
The compatibility helper expects exactly:

- executable `~/.config/opencode/skills/telegram-ip-reset/bin/telegram-ip-reset.sh`;
- config `~/.config/telegram-ip-reset.env`, mode 0600;
- `TELEGRAM_IP_RESET_BOT_TOKEN`, `TELEGRAM_IP_RESET_CHAT_ID`, and
  `TELEGRAM_IP_RESET_TEXT` matching the phone rule.

Only after the owner authorizes automatic phone resets and a manual reset has
been verified, set `DS_FLASH_AUTO_RESET=1` in the helper override and restart it.
All-route connection/free-limit failures can request a background reset. It is
single-flight and persists a **30-minute minimum interval** in
`~/.local/state/opencode-cc-ds-flash/last-reset.json`. Do not delete the timestamp
to force extra resets. It never intentionally resets WARP/Morph services.

The helper clears inherited proxy variables before invoking the sender; Bot API
control goes direct, while its recovery probe uses phone HTTP proxy :18889.
Sender output is discarded. The helper independently checks HTTPS trace before
and after; where a before-IP exists it requires a changed IP, otherwise it can
only establish restored egress. It polls up to 48 times with 5-second gaps and
8-second probe deadlines. A reset can interrupt every service sharing that
phone exit. Direct requests and WARP requests are unaffected by the phone IP
change. Never treat resets as account-quota renewal or promise they repair 429s.

## 8. Secure local access before connecting clients

Sign in at `http://127.0.0.1:8787` using the panel password. Create a relay client
API key in the Keys page for CPA; use a separate key for direct clients if wanted.
`OPENCODE_CC_REQUIRE_API_KEY=true` protects `/v1/*`; the panel password protects
administration. These are separate controls. Configure budgets/IP restrictions
as appropriate. Confirm requests without a client key are rejected:

```bash
curl --noproxy '*' -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8787/api/config
curl --noproxy '*' -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8787/v1/models
```

Expect 401 without auth on both, while health endpoints can remain public.
Use localhost/private access for installation. To serve friends remotely, add
TLS, explicit authentication and firewall/reverse-proxy rules; do not expose
the dashboard or phone/helper proxy publicly. This guide installs independent
copies on friends' laptops; it does not publish Wraient's running service.

## 9. Acceptance on the friend's actual machine

Unit tests and historical evidence do not establish a new machine's eligibility.
First run a minimal real message against the exact free model, then the full
Claude/CPA file-tool task. No model/account substitution counts as acceptance.
Avoid dashboard quota-bar polling. The acceptance runner reads the first saved
upstream; its config must use HTTPS opencode.ai and the intended test credential.

```bash
go build ./... && go test ./...
go build -o /tmp/opencode-cc-stage .
"$BUN_BIN" test scripts/ds-flash-gateway/gateway.test.mjs
python3 -m venv /tmp/cc-acceptance-venv
/tmp/cc-acceptance-venv/bin/pip install PyYAML
# --cpa, --stage, --claude, --config allow different install locations.
/tmp/cc-acceptance-venv/bin/python scripts/test_ds_flash_chain.py --credential saved --out /tmp/ds-proof.jsonl
/tmp/cc-acceptance-venv/bin/python scripts/test_ds_flash_chain.py --fault-seconds 8 --out /tmp/ds-proof.jsonl
/tmp/cc-acceptance-venv/bin/python scripts/test_ds_flash_chain.py --fault-seconds 195 --out /tmp/ds-proof.jsonl
# Optional, only if the official CLI owns a valid active OAuth connection:
/tmp/cc-acceptance-venv/bin/python scripts/test_ds_flash_chain.py --credential active --out /tmp/ds-proof.jsonl
```

The runner restricts the registered tool catalog to Read/Write, uses an absolute
proof path to avoid ambiguous tool instructions, and captures bounded verbose
transcripts. It isolates CPA/config/Claude HOME, reserves 18789/18319/18794, enables
staging request logs, grants only Read/Write for a generated proof file, verifies
its contents plus successful Read/Write tool results, removes the proof before
each phase, and cleans up its own children. The sustained test keeps the same
CPA process for the post-recovery task. Faults are injected **before** the helper,
so injected 429s do not trigger phone resets. Real helper failure could still
trigger resets if enabled: keep auto-reset off during acceptance unless already
authorized. It uses installed CPA and gateway; it does not build/install them.
It uses the running relay's process environment when available, otherwise the
caller's environment; pass DB overrides explicitly when using active credentials
without a running service. The default saved config is `~/data/config.json`.
Inspect test JSONL privately before sharing; transcripts may contain paths or
other local details even though the configured static key is redacted.

Also test a normal live CPA invocation after staging, with your own credentials,
then check relay/helper logs for real success and tool execution. Health alone
is not acceptance. Keep any monitoring hourly to conserve usage; this repo
installs no infinite watcher or free-allowance probes.

## 10. Updates, staging, rollback and recovery

Never start a second relay on production :8787. Build/test first, check listeners,
then stage on :18789 with a fresh private data directory and relevant saved
upstream settings. Use the acceptance runner instead of manually copying secrets
into shell commands. Don't reuse :18787 (Morph) or :18790 (production helper).

Before changing a live helper, install a separate release and stage it on a free
loopback port, e.g. 18791, using `DS_FLASH_PORT=18791`, `DS_FLASH_ROUTES=direct`,
`DS_FLASH_AUTO_RESET=0`, and an isolated `STATE_DIRECTORY`. Pass
`--gateway http://127.0.0.1:18791` to acceptance. Check that port first. Test the
exact new runtime/prelude; don't infer it from an old production gateway's pass.

Back up current binary, helper symlink target, private config and units locally
with restricted permissions. After staging passes:

```bash
systemctl --user stop opencode-cc
install -m 755 /tmp/opencode-cc-stage "$HOME/.local/bin/opencode-cc.new"
mv -f "$HOME/.local/bin/opencode-cc.new" "$HOME/.local/bin/opencode-cc"
systemctl --user start opencode-cc
curl --noproxy '*' -fsS http://127.0.0.1:8787/api/health
```

Atomic replacement is mandatory: Morph also executes that same binary on the
reference machine; overwriting its running inode fails with `Text file busy`.
Do not stop Morph. Select new helper releases with a temporary symlink + atomic
rename and restart only the helper. Smoke a real live `/v1/messages` request and
CPA file-tool task after deployment. Stop only your staging PID and remove its
private temp data. Do not use broad process-name kills that can match production.

To disable helper routing remove only the relay's `ds-flash.conf`, reload systemd
and restart relay, then stop/disable the helper. This returns to Go transport;
it does not guarantee the exact free model remains compatible. To roll back
fully atomically restore the previous relay binary and helper release/config.
Disable only this optional WARP consumer with `warp-cli disconnect`; stop only
the isolated phone-egress unit to remove that proxy. Keep ordinary Tailscale
and other services intact.

Recovery backups need source + patch bundle + pinned versions/download checksums,
service units, private config and encrypted irreplaceable credentials/state.
Re-download runtimes and reinstall dependencies; don't publish secrets, node
identities, databases, caches or machine-specific binary backups.

## 11. Diagnose the layer that failed

| Symptom | Check / meaning |
|---|---|
| Connection refused | Exact listener, unit status, port conflict, helper drop-in URL |
| Helper healthy, inference fails | Health proves listener only; test real exact-model request |
| 401 / expired OAuth | Correct credential identity; official CLI refresh; don't copy legacy tokens |
| 403 FreeTierError | Client/model eligibility; key health alone is insufficient |
| 429 FreeUsageLimitError | Could be eligibility/network/account constraints; not proof of zero account allowance |
| Immediate repeated CPA cooldown | Correct patched CPA + hold enabled for this group; never clear all providers' cooldowns |
| 429 after ~3 minutes | The opted-in hold exhausted; report genuine upstream failure |
| WARP connected, HTTPS timeout | Proxied trace, tunnel logs and connectivity; no claim of healthy inference |
| Phone proxy responds but Internet fails | Verify HTTPS through :18889, phone data, tailnet/exit permissions |
| Telegram sends but no reset | Exact authorized sender/chat/text, phone automation and OS permissions |
| HTTP 200 then truncated stream | Read stream to completion; explicit error envelope is a failed generation |
| Wrong upstream despite env edit | Enabled saved upstream pool takes precedence |
| Missing UI | Build Vite and copy web/dist to internal/assets/dist **before** go build |
| Tool/context failures | Real Claude tool task, model launch context, prelude/runtime version; not just curl |

Use `journalctl --user -u opencode-cc -u opencode-cc-ds-flash --since '10 min ago'`
locally; sanitize before sharing. Diagnostic probes under `scripts/probe_zen_*`
are historical opt-in investigations with local path assumptions. Some replay
private fixtures, consume model allowance or require the legacy auth file;
read their `--help` and source first. They are not the installation procedure.
Never feed the whole historical investigation into configuration as if every
old hypothesis were current: the deployed summary supersedes older failures.
