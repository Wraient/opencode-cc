# Optional phone reset sender

This sends a fixed trigger to **your own** Telegram bot/chat. It does not install
phone automation. First follow the [phone/control-channel guide](../../docs/setup/FRIENDS.md#7-phone-egress-and-telegram-recovery).
No bot credential, chat identity, phone enrollment or Wraient private skill is
required or included. The bundled sender implements the same path/env contract
as the reference private script, with sanitized status output.

```bash
install -d -m 700 "$HOME/.config/opencode/skills/telegram-ip-reset/bin"
install -m 755 contrib/telegram-ip-reset/telegram-ip-reset.sh "$HOME/.config/opencode/skills/telegram-ip-reset/bin/telegram-ip-reset.sh"
# New file only; do not overwrite an existing private config.
install -m 600 contrib/telegram-ip-reset/telegram-ip-reset.env.example "$HOME/.config/telegram-ip-reset.env"
# Edit the private config, then configure the matching authorized phone rule.
```

Requirements: Bash, Python 3 (stdlib only), direct HTTPS access to Bot API and
HTTP CONNECT phone egress. Config is sourced as shell code; only load your own
0600 file. Names: `TELEGRAM_IP_RESET_BOT_TOKEN`, `TELEGRAM_IP_RESET_CHAT_ID`,
`TELEGRAM_IP_RESET_TEXT` (default `network-reset`),
`TELEGRAM_IP_RESET_PROXY`, `TELEGRAM_IP_RESET_TIMEOUT` (default 180),
`TELEGRAM_IP_RESET_DELETE_AFTER` (optional). Legacy bot/chat fallback env names
remain supported for the reference integration.

After the device owner authorizes a manual phone flip:

```bash
~/.config/opencode/skills/telegram-ip-reset/bin/telegram-ip-reset.sh 'manual setup verification'
```

Expect a temporary network interruption. Exit 0 means trigger accepted and
phone proxy HTTPS egress available again, **not proof of an IP change or model
success**. Compare private before/after traces separately. The helper does that
independently. Without `TELEGRAM_IP_RESET_PROXY`, this bundled sender refuses to
send; it never reports success from a blind sleep. The sender itself has no
persistent reset throttle: the helper enforces its 30-minute throttle, and
manual invocations must be deliberate. Do not loop this script.

The helper invokes this executable with sourced env exported,
`TELEGRAM_IP_RESET_CONFIG=/dev/null`, phone proxy :18889 and a 60-second sender
probe timeout; it kills the sender after 90 seconds. It then independently
checks recovery. Direct invocation defaults to 180 seconds. Never run with
shell tracing or log env values; API tokens also appear in Telegram API URL
paths, so do not enable verbose HTTP logging.
