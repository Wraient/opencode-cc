# DeepSeek V4.1 Flash Free: repairs, egress and reproducibility

This explains the reference stack and the October 14–28, 2026 investigation.
The configuration and journal snapshot below were inspected on October 28;
this document was prepared for publication on October 29. It is a historical
snapshot, not a live status page. For installation commands, use
[FRIENDS.md](FRIENDS.md); for optional clients, see
[OPTIONAL_CLIENTS.md](OPTIONAL_CLIENTS.md).

**Result:** real Claude Code sessions work through the exact free-model alias,
with automatic recovery from many temporary network and free-limit failures.
The evidence does **not** establish unlimited allowance, zero upstream 429s,
or uninterrupted availability. Recovery can hide a failed upstream attempt
from the client; it does not remove the provider's limits.

## 1. The working request chain

```text
T3 Code (optional UI)
  -> Claude Code
  -> patched CLIProxyAPI :8317
       client alias: deepseek-v4.1-flash-oc
  -> opencode-cc :8787
       upstream model: deepseek-v4.1-flash-free
  -> Bun compatibility helper :18790
  -> direct laptop Internet OR phone HTTP CONNECT :18889
  -> OpenCode Zen

Phone path:
  :18889 -> isolated userspace Tailscale -> Android exit node -> cellular Internet

Separate optional WARP path:
  explicitly configured application -> SOCKS5 :40000 -> Cloudflare -> Internet

Reset control path:
  local sender -> Telegram Bot API -> phone automation -> cellular reconnect
```

WARP and the phone are separate egress paths, not consecutive VPN layers.
Neither T3 nor a Cloudflare Worker is required to run the local relay stack.
Loopback addresses refer to the machine running the agent and services.

## 2. What was broken and what repaired it

### Credentials and initial network eligibility (October 18)

The old OpenCode JSON auth file contained a different, expired credential.
Reading that file did not reproduce the identity used by the native v2 CLI.
The relay gained the explicit `opencode://active` credential reference: each
selection reads the active v2 credential database read-only, restricted to the
trusted HTTPS OpenCode origin. OpenCode owns login and refresh; the relay never
uses its refresh token or writes its database. A separately supplied static Zen
key was also accepted in later tests.

In the October 18 controlled tests, the active credential succeeded through
WARP while direct inference returned 403. This established an observed network
acceptance difference, not the private gateway's complete eligibility rule.
See [the credential/WARP repair](../diagnostics/2026-10-18-free-tier-rejection.md).

### Real Claude tool turns and unavailable endpoints (October 20–21)

The relay now preserves available DeepSeek `reasoning_content` and includes an
explicit empty field when required for historical assistant turns without it.
This repairs tool follow-ups exposed by compaction or reasoning-cache expiry.
Separate bounded affinity recovery handles the specific upstream
`503 Endpoint is unavailable` response and retains a successful route.
These changes affect the connection and protocol translation, not DeepSeek's
model code. The exact Claude/CPA/DeepSeek acceptance run completed compaction
and large native tool calls. See [reasoning replay](../diagnostics/2026-10-20-deepseek-reasoning-replay.md)
and [full-chain acceptance](../diagnostics/2026-10-21-claude-deepseek-acceptance.md).

### WARP connection errors amplified by CPA cooldowns (October 22)

A SOCKS `host unreachable` failure was followed by CPA's immediate
`auth_unavailable`. That did not mean the credential had expired: temporary
network failure had made the local route unavailable.

The relay retries known temporary proxy failures within a bounded attempt
budget, then returns `503` with `Retry-After: 2`. CPA was patched to preserve
and interpret service retry hints without applying Anthropic quota-window
padding to ordinary 408/5xx responses. Auth and quota failures retain their
own behavior. Fault injection verified brief failure, bounded sustained failure
and subsequent recovery using the same alias without manual auth resets.
See [network recovery](../diagnostics/2026-10-22-warp-proxy-recovery.md).

### A transient Zen 429 became a nearly day-long local block (October 22)

Zen returned `Retry-After: 81927`, counting toward midnight UTC. CPA stored a
roughly 22h45m block and rejected later requests locally, even though actual
upstream rejection cleared in under an hour in that incident.

The pinned CPA patch adds `rate-limit-hold-seconds: 180` to the dedicated `oc`
relay group. It holds streaming or non-streaming requests and retries with
2, 4, 8, then 15-second delays, for up to 180 seconds. It caps/restores stale
local 429 cooldowns under that opted-in policy. It does not change the
provider's quota or disable cooling globally. If rejection persists, the real
error reaches Claude, whose own retry behavior can extend total elapsed time.
Client disconnection cancels the hold.

Stock CPA is not equivalent. The reproducible build is
[v8.0.4 plus the bundled local3 source patch](../../contrib/cliproxyapi/README.md),
with the [dedicated-group example config](../../contrib/cliproxyapi/config.example.yaml).

### Claude request compatibility and egress recovery (October 24)

Native OpenCode could answer while real translated Claude requests received
the free-limit error. Valid credentials, healthy ports and a changed exit IP
were insufficient to reproduce native success.

The deployed helper combines:

- A pinned, audited OpenCode system prelude, followed by retained client instructions.
- Consolidated system messages and native-style project/session metadata.
- Bun fetch, validated with Bun 1.4.2.
- Retained client history, tool schemas, limits and reasoning settings.
- Session affinity scoped by authorization and incoming session, with protocol
  timestamps refreshed every four minutes independently of response status.

The prelude adds about 2–3K context tokens. Its public tool catalog does not
register or execute those tools. Parameter-only, token-field-only, tool-renaming
and shorter-prelude trials did not establish a repair. The **combined path**
passed real harness acceptance; the exact private discriminator remains unknown.
Do not describe Bun, a header, an IP reset or the prelude alone as a proven
universal fix. See the deployed summary at the top of
[the investigation](../diagnostics/2026-10-24-warp-api-check.md); its older
sections preserve superseded hypotheses and failed tests.

Only the exact free model's Chat requests on trusted Zen origins use this
opt-in helper. Other models/origins retain the existing transport. The helper
stores no provider credentials and never replaces the model or account.

It tries the preferred configured egress, then the other for connection failure
or the recognized `FreeUsageLimitError`. Success selects that route for later
requests. 401/403 and other 429s pass through. All-route free-limit rejection
preserves the actual response and `Retry-After` for CPA. Each route has a
30-second header deadline; healthy streaming can continue longer. There are
no idle model probes consuming allowance.

### Failures after HTTP 200 headers (October 24, runtime v3)

Some upstream streams disconnected after successful headers. The v3 helper
turns late disconnects into explicit API errors, which the Go scanner rejects
even after partial output. The next attempt prefers the alternate route.
It never silently replays a partly delivered generation or tool call internally.
The versioned installed helper was `releases/20261024-v3` at review time.

Cache accounting was also corrected: fresh plus cached input now equals total
input instead of double-counting cached tokens in Anthropic usage. That fixes
premature client context inflation; it does not raise upstream capacity.

## 3. WARP setup and its actual role

The reference Arch installation uses the reviewed `cloudflare-warp-bin` AUR
recipe wrapping the checksum-verified official binary. It began at
2026.8.2110.0 and was updated to 2026.8.2115.0. The system service is
`warp-svc.service`, consumer registration, **local proxy mode**, SOCKS5 on
`127.0.0.1:40000`. This installed release uses MASQUE for proxy mode; automatic
HTTP/3/HTTP/2 fallback was restored after diagnostic protocol experiments.

Only explicitly configured applications use WARP. The rest of the laptop keeps
normal routing. WARP is neither OpenCode authentication nor the Android exit
node, and it is not an inbound Cloudflare Tunnel. See
[Cloudflare's mode documentation](https://developers.cloudflare.com/warp-client/warp-modes/).

Historical tests include both WARP success when direct failed and WARP 429s
when direct succeeded. WARP also reported connected while HTTPS timed out.
A listener, daemon status or successful trace is not inference acceptance.

The October 28 read-only snapshot found:

- The Go relay had proxy variables, but `NO_PROXY` included `opencode.ai`,
  bypassing WARP for that destination.
- The DS helper explicitly configured `phone,direct`; it had no WARP route.
- Helper health reported `direct` preferred, with zero requests in flight.
- Relay, CPA, helper and isolated phone Tailscale services were active.
- WARP 2026.8.2115.0 required Terms acceptance before CLI status reporting.
  The review did not accept terms or change routing.

Thus the working DS setup did not depend on WARP. Configure and test it
independently if desired; never infer its use from `HTTPS_PROXY` alone.

## 4. Phone egress and Telegram recovery

An additional userspace `tailscaled` daemon has its own state/socket and exposes
an HTTP CONNECT proxy on `127.0.0.1:18889`. It selects an approved Android
cellular exit node without replacing the laptop's normal Tailscale daemon or
default route. This listener is **HTTP**, not SOCKS. See
[Tailscale exit nodes](https://tailscale.com/docs/features/exit-nodes) and
[userspace networking](https://tailscale.com/docs/concepts/userspace-networking).

On the reference machine `morph-tailscale.service` provides it. Other Morph
Workers, VPC bindings, protocol adapters and tunnels are a separate product;
they are not required to reproduce this local stack. Its restored sidecar
identity must not run concurrently on its original server. A new installation
must enroll a fresh node in its own tailnet, never copy this machine's state.
The older farm proxy on 18888 is separate too.

When all configured helper routes reject with the recognized free-limit error
or cannot connect, authorized auto-reset can start **in the background**:

1. Persist the reset timestamp and obtain a before-IP through the phone proxy.
2. Invoke the local sender with inherited proxy variables cleared, so Telegram
   control goes directly rather than depending on the broken phone route.
3. Send the configured fixed trigger to the owner's bot/chat.
4. Phone-side automation checks the authorized sender/chat/text and cycles
   cellular connectivity; Tailscale subsequently reconnects.
5. Independently probe HTTPS egress through the phone. If a before-IP existed,
   require a different after-IP; otherwise establish only restored connectivity.

The helper allows one reset at a time and persists a **30-minute minimum
interval** across restarts. It discards sender output and stores only the reset
timestamp. A successful Telegram send is not proof of execution, IP change or
model acceptance. Changing the phone IP does not renew account quota. Shared
phone consumers can lose connectivity during a reset; direct/WARP exits are
unaffected by the phone IP change.

The repository includes a [sender and private config template](../../contrib/telegram-ip-reset/README.md),
but no universal Android automation export. The owner must supply a bot/chat
and matching phone automation with suitable Android permissions. Ordinary apps
cannot universally toggle airplane mode; the required automation/ADB/root or
device-owner permissions depend on the device. Keep blocker-notification bot
configuration separate from reset configuration.

## 5. Evidence and the limits of the result

| Acceptance | Recorded outcome |
|---|---|
| Real Claude/CPA saved-key file task | Write and Read succeeded; file independently verified |
| Separate active OAuth credential | Same exact-model file task succeeded |
| Brief controlled 429 outage | Recovered after the 8-second interruption |
| Sustained controlled 429 outage | Recovered after 195 seconds, about 213 seconds total |
| Next task in the same CPA process | Succeeded without a stale day-long block |
| Production October 24 task | Relay IDs 32178–32180 all 200 on the exact free model |
| Later active monitoring segment | About 72 minutes of successful ongoing work recorded |

The [friend-setup evidence](../evidence/2026-10-27-friend-setup.json) reran saved-key,
active-OAuth, brief-outage and sustained-outage/recovery tests using freshly
built patched CPA and a separate **direct-only helper**, with auto-reset off.
Direct and phone HTTPS traces passed; WARP HTTPS timed out. It tested installation
components on the reference laptop, not a physical second laptop. The bundled
Telegram sender was tested offline; those setup tests did not reset a phone.

The October 28 retained-journal audit counted 4,002 upstream header responses
in the available October 26–27 records: 3,979 HTTP 200, 14 HTTP 429 and nine
HTTP 500, plus 92 separately logged transport-failure events. It recorded 11
reset starts and 11 egress-recovery events. These are **route attempts/header
outcomes**, not unique client requests or completed-generation success rates.
Journal retention did not cover the whole two-week investigation.

Saved monitoring also records failures reaching clients, monitoring gaps and
idle periods. One original active-hour target was not achieved; a later target
recorded a roughly 72-minute active segment. Neither is continuous 24-hour
zero-failure proof. A successful response on the next attempt does not turn
the preceding attempt into a success.

Authentication, free-client eligibility, network eligibility and available
capacity are distinct. Controlled direct-200/phone-429/direct-200 comparisons
showed a network contribution, but changing IP was not sufficient in all tests.
Even invalid/anonymous controls could produce the same free-limit error.
The evidence does not identify the complete private limiter or a universal
account/IP/model/session quota formula.

OpenCode's [published model documentation](https://opencode.ai/v2/docs/console/models/)
states that free-tier chat models require OpenCode. The observed compatibility
path is not a provider-guaranteed external-client contract. Provider changes
can invalidate it, and genuine all-route failures must remain visible.

## 6. Reproducing it on another machine

**Ready for guided installation, not a verified turnkey second-laptop release.**
Use [FRIENDS.md](FRIENDS.md) as the installation procedure.

| Component | Required for reproduction |
|---|---|
| Relay | Wraient fork, `patch/opencode-user-agent`; repair/setup shipped in `831afcf` |
| Build | Go 1.24+, Node 20+/npm for dashboard assets |
| Helper | Bundled source, pinned prelude/license and validated Bun 1.4.2 |
| CPA | Pinned upstream v8.0.4 plus bundled local3 patch and opted-in hold config |
| Zen | Owner's own eligible key or active OpenCode v2 login; native fixture was 2.1.2 |
| Claude | Needed for this acceptance chain; recorded version 2.1.310 |
| T3 | Optional UI; client documentation shipped in `aece277` |
| WARP | Optional; owner's own registration and independent HTTPS/inference validation |
| Phone | Optional resilience path; owner's Android data connection, tailnet and exit approval |
| Telegram | Optional authorized recovery; owner's bot/chat, sender config and phone automation |

Start on loopback with panel/client authentication, **direct egress** and
`DS_FLASH_AUTO_RESET=0`. Verify exact-model inference and a real file-tool task.
Then add and verify phone egress. Enable automatic resets only after the owner
has authorized and verified its manual phone action. Never copy another user's
credentials, OAuth database, WARP registration, Tailscale identity or private env.

The reference guide targets Linux/systemd. Other OS service/network setup needs
adaptation. Native tests passed; full Docker sidecar acceptance, new tailnet
enrollment, Android automation setup and physical second-laptop acceptance were
not performed. New accounts/networks still need their own acceptance.

For reliability, test the exact Claude/CPA alias, brief outage, sustained outage
and recovery in the same CPA process using `scripts/test_ds_flash_chain.py`.
Its isolated ports are 18789/18319/18794. Never reuse production 8787/18790 or
Morph's 18787. Build/test, stage, then atomically replace the binary; follow the
rollback instructions rather than overwriting a running executable.

Context controls are a separate issue: the measured free-route input ceiling
was about 261K. The reference user's desired native T3 250K control was not
enforced by the inspected October 26 nightly adapter; adding a descriptor did
not change Claude's effective window. Older launcher instructions describe a
historical workaround, not completion of that native migration or permission
to reinstate it on the reference machine. Verify actual launched settings and
follow the current project's policy rather than assuming a UI label works.

## 7. Review sources

The public evidence is linked above. Private T3 history used for the review
includes these threads (links require the owner's T3 instance):

- [Sync opencode-cc to official release](t3-thread://v1/ade57c76-4be0-4423-ab35-e0e355df5c33)
- [API Error: Request rejected (429) · All credential...](t3-thread://v1/ce29b13f-b963-4864-9c16-928126e3327a)
- [fix opencode-cc and make this t3code thread comple...](t3-thread://v1/cd5eabda-c248-4417-b062-09cc1793aca1)
- [tell me what are the local changes we've not pushe...](t3-thread://v1/9eb3ecf4-1706-4239-9b94-2b176715d570)
- [there is such a simple way to add context window t...](t3-thread://v1/812cff1e-bfcc-4f61-8f52-0bbfbaefd896)

Some older monitored target threads were no longer available through the T3
reader. Their retained local monitoring records supplied the qualified
historical results above; no missing thread contents or monitoring continuity
were inferred. No secrets, private IP addresses or full chat transcripts are
required to install this stack or included in this explanation.
