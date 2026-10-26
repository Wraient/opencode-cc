# Optional clients: Claude Code and T3 Code

Neither client is required to run the relay. You can use an OpenAI-compatible
client against `http://127.0.0.1:8787/v1` with its own relay client key instead.
Claude Code is needed for the documented Claude file-tool acceptance tests;
T3 Code is optional even when using Claude Code. Install only what you want.

First finish [the laptop/server setup](FRIENDS.md), including panel/client auth.
The exact recovery-tested route below uses **patched CPA**:

```text
Terminal Claude OR T3 Code's Claude provider
  -> CPA :8317, alias deepseek-v4.1-flash-oc
  -> relay :8787, model deepseek-v4.1-flash-free
  -> Bun :18790 -> configured phone/direct egress -> Zen
```

All `127.0.0.1` addresses refer to the machine **running the agent process**.
When T3 connects to a remote server or WSL, install the CLI and reachable relay
stack there, or deliberately configure a secured remote connection. A browser
or phone displaying T3 does not run the agent or supply its laptop loopback.

## A. Optional Claude Code terminal setup

### Install the CLI

Use [Anthropic's official setup guide](https://code.claude.com/docs/en/setup).
For Linux, the native installer supports a specific version:

```bash
curl -fsSL https://claude.ai/install.sh -o /tmp/claude-install.sh
# Review the installer before running it.
bash /tmp/claude-install.sh 2.1.310
command -v claude
claude --version
```

`2.1.310` is the version used for this repository's recorded acceptance, not a
claim about the latest version. Newer versions need their own connection/tool
check. Native installers may enable automatic updates; follow the official
version-management instructions if exact-version reproduction is required.
Do not replace an existing working installation merely to follow this example.

### Configure this terminal session for CPA

Create a private `~/.cli-proxy-api/api-key.txt` (0600) containing your own
**client-to-CPA** key from CPA's `access.api-keys`. This is not the upstream Zen
key and not the CPA-to-relay client key. Enter it using your private editor;
do not paste it into a shared terminal command, project file or chat.

Use a subshell so settings do not leak into future Claude sessions:

```bash
(
  export CLAUDE_CONFIG_DIR="$HOME/.claude-cpa"
  install -d -m 700 "$CLAUDE_CONFIG_DIR"
  export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
  export ANTHROPIC_AUTH_TOKEN="$(cat "$HOME/.cli-proxy-api/api-key.txt")"
  export ANTHROPIC_API_KEY=''
  export ANTHROPIC_MODEL=deepseek-v4.1-flash-oc
  export ANTHROPIC_CUSTOM_MODEL_OPTION=deepseek-v4.1-flash-oc
  export CLAUDE_CODE_MAX_CONTEXT_TOKENS=260000
  export CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000
  export NO_PROXY=127.0.0.1,localhost,::1
  export no_proxy="$NO_PROXY"
  claude --model deepseek-v4.1-flash-oc --effort max
)
```

Use an empty dedicated config directory initially. It prevents unrelated saved
Claude settings or a cached subscription login from confusing this gateway
connection; do not log out of or overwrite your ordinary account. Complete any
first-run local onboarding in that directory. Do not change `HOME` to isolate a
normal installation; `CLAUDE_CONFIG_DIR` is the intended separation. The test
runner's temporary HOME is specifically for disposable automated acceptance.
Do not enable fast mode or add a fallback model for this acceptance route.
The CPA alias supports the configured effort choices `low`, `high`, `max`;
selecting one does not grant additional upstream capability or allowance.

The base URL has **no `/v1` suffix** for Claude's Anthropic client. The explicit
model is the CPA alias with `-oc`; the exact upstream model without `-oc` belongs
in CPA's mapping. A Zen key sent to CPA will not authenticate as a CPA client.
Managed settings, existing `apiKeyHelper`, project overrides and shell exports
can still affect Claude; inspect them locally if requests go to the wrong place.

### Verify real tools before using your project

In the configured subshell above, first use an empty scratch directory, then
ask Claude to create and read a proof file:

```bash
proof_dir="$(mktemp -d /tmp/claude-cpa-proof.XXXXXX)"
claude -p "Use Write to create $proof_dir/proof.txt containing exactly CLIENT_SETUP_OK. Then use Read on that exact file and report its contents." \
  --model deepseek-v4.1-flash-oc --effort max \
  --tools Read,Write --allowedTools Read Write --permission-mode acceptEdits \
  --no-session-persistence --output-format json --verbose
# Check the actual artifact; a model's claim or exit code alone is insufficient.
test "$(cat "$proof_dir/proof.txt")" = CLIENT_SETUP_OK
```

Read the verbose transcript for successful **Write** and **Read** results, and
check relay records for the exact free model. Do not share raw transcripts
without review. Remove the scratch directory after inspection. For brief and
sustained outage acceptance use the runner in [section 9 of the main guide](FRIENDS.md#9-acceptance-on-the-friends-actual-machine).
No T3 install is necessary for that runner.

You can point Claude directly at `http://127.0.0.1:8787` with a **relay client
key** and `--model deepseek-v4.1-flash-free`, but that bypasses CPA's hold policy.
Do not describe direct relay success as proof of the CPA alias/recovery chain.

### Optional convenience launcher

`scripts/claude-cpa` is also available. Install a personal copy as
`~/.local/bin/claude-cpa`, and set `~/.config/claude-cpa/models.env` (0600):

```dotenv
DEFAULT_SLOT=opus
DEFAULT_UPSTREAM=http://127.0.0.1:8317
DEFAULT_EFFORT=max
SLOT_OPUS_MODEL=deepseek-v4.1-flash-oc
```

Then `claude-cpa --slot opus` forwards to the real `claude` found in PATH.
Here `opus` is only the launcher's slot name; the request still selects DeepSeek.
Set the DeepSeek context variables and dedicated `CLAUDE_CONFIG_DIR` as above;
this slot launcher does not apply model-specific context limits. Its default
key file is `~/.cli-proxy-api/api-key.txt`, overridable with
`CLAUDE_CPA_KEY_FILE`. It can auto-start `cliproxyapi.service`; set
`CLAUDE_CPA_NO_AUTOSTART=1` if you manage CPA differently. An explicit
`--settings` changes the wrapper's forwarding path; inspect its source before
combining it with custom settings. Do not use this slot launcher as T3's SDK
executable; the model-aware launcher below is designed for that purpose.

## B. Optional T3 Code setup

### Install and open T3

Use a desktop release from [T3's official releases](https://github.com/pingdotgg/t3code/releases)
or follow its [installation guide](https://github.com/pingdotgg/t3code/blob/main/docs/user/install.md).
The documented Linux server installer is:

```bash
curl -fsSL https://t3.codes/install.sh -o /tmp/t3-install.sh
# Review before running; skip if an existing T3 installation already works.
sh /tmp/t3-install.sh
t3 --help
t3
```

Connect to an already running T3 server instead of launching another copy on
its port. A new server can use a separate base directory if you deliberately
need one. This repository does not install, update or restart your T3 app.

### Add one Claude CPA provider

In **Settings → Providers**, add a Claude provider instance named **Claude CPA**,
or edit your existing CPA instance. Leave your ordinary Claude subscription
provider and unrelated providers alone. See [T3's Claude provider guide](https://github.com/pingdotgg/t3code/blob/main/docs/user/providers-claude.md).

For a **DeepSeek-only** instance, use these fields:

| Field | Value |
|---|---|
| Provider type | Claude / Claude Code (`claudeAgent`), not OpenCode or Codex |
| Binary path | Absolute real Claude executable returned by `command -v claude` |
| CLAUDE_CONFIG_DIR path | Absolute path to your dedicated `~/.claude-cpa` directory |
| Custom model / Add custom model | `deepseek-v4.1-flash-oc` |
| Auto-compact after | `200000`, if using this UI setting for a DeepSeek-only instance |
| Launch arguments | Leave empty for the basic setup |

Put these in that provider's **Environment variables**, not Launch arguments:

| Variable | Value |
|---|---|
| `ANTHROPIC_BASE_URL` | `http://127.0.0.1:8317` |
| `ANTHROPIC_AUTH_TOKEN` | Your client-to-CPA key; mark **Sensitive** |
| `ANTHROPIC_API_KEY` | Explicitly empty |
| `CLAUDE_CODE_MAX_CONTEXT_TOKENS` | `260000` |
| `CLAUDE_CODE_AUTO_COMPACT_WINDOW` | `200000` |
| `NO_PROXY` and `no_proxy` | `127.0.0.1,localhost,::1` |

Enter the token privately in your own T3 UI. Do not put `$(cat ...)` in a T3 env
value: these are literal values, not shell commands. When configuring with an
agent, use that environment's private secret mechanism. Do not copy Wraient's
settings.json or assume Sensitive values can be recovered from a redacted export.
Desktop-launched T3 may not inherit your terminal PATH or exports, so use absolute
binary/config paths and provider-specific env. In a remote T3 connection those
paths and env belong to the agent server, not the viewing desktop.

Select **Claude CPA → deepseek-v4.1-flash-oc** in a new scratch thread. A custom
model picker entry does not change CPA's routing: the alias must exist there
as well. Do not select an unrelated built-in Claude model or add fallback routes
as a substitute. Keep fast mode off. If you expose an effort control for this
custom model, use the CPA-configured `low` / `high` / `max` choices; controls
alone do not establish provider support.

### Optional: share one CPA instance across DeepSeek and other models

Wraient uses one CPA provider (`claudeAgent_2` is its **local instance ID**, not a
portable ID to copy). Its other-model defaults are 460000 maximum context and
400000 auto-compact; only DeepSeek launches at 260000 / 200000. Keep your own
other models' appropriate limits rather than assuming they support 460K.

For that split, install a personal copy of `scripts/claude-cpa-context` after
replacing its real Claude path. From this repository root:

```bash
python3 - <<'PY'
from pathlib import Path
import re
import shutil
real = shutil.which('claude')
if not real:
    raise SystemExit('Install Claude Code first')
source = Path('scripts/claude-cpa-context').read_text()
source, count = re.subn(r'^CLAUDE = .+$', lambda _: 'CLAUDE = ' + repr(real), source, count=1, flags=re.M)
assert count == 1
out = Path.home() / '.local/bin/claude-cpa-context'
assert Path(real).resolve() != out.resolve(), 'Wrapper must not execute itself'
out.parent.mkdir(parents=True, exist_ok=True)
out.write_text(source)
out.chmod(0o755)
PY
~/.local/bin/claude-cpa-context --version
```

Change the T3 CPA instance's **Binary path** to that wrapper's absolute path.
Keep shared non-DeepSeek context defaults in provider env; leave the separate
**Auto-compact after** UI field empty when provider env plus wrapper own the
policy. The wrapper overrides both context variables only when the launch args
contain `--model deepseek-v4.1-flash-oc` (also accepts `--model=...`), and forwards
all other args/env to the real CLI. Other model aliases still need their own CPA
routes; the minimal example config in this repo only defines DeepSeek.

**Stop/relaunch the thread when changing between DeepSeek and another model.**
Maximum context is fixed when Claude starts; switching the model in an already
running process does not re-execute the wrapper. A context display or compaction
setting does not increase upstream capacity. Recheck the wrapper's real binary
path if Claude is reinstalled or moved. Do not install the hard-coded reference
path from the source file verbatim on a friend's laptop.

### Verify from T3 itself

1. Open an empty scratch project and a fresh thread with the intended provider/model.
2. Ask it to create `proof.txt` with a unique value using Write, then use Read on it.
3. Inspect T3's actual tool events and the real file. Check relay records for
   `deepseek-v4.1-flash-free` and the correct CPA alias path.
4. Confirm the launched process uses the expected context variables without
   dumping its full environment or credentials. Restart it after model-limit changes.

Terminal Claude success does not prove that T3 selected the right provider,
executable, credentials or model. Provider discovery can differ by T3/Claude
version. If a check fails, report that layer; do not switch harness/model or
claim success based on a health response. This documentation update checks the
setup snippets and launcher locally; it does not create a new T3 thread, change
your providers, or claim a fresh T3 UI acceptance run.
