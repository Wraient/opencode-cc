#!/usr/bin/env bash
# Optional local control adapter; credentials never appear in argv or status.
set -eu
config="${TELEGRAM_IP_RESET_CONFIG:-$HOME/.config/telegram-ip-reset.env}"
if [[ -f "$config" ]]; then
  set -a
  source "$config"
  set +a
fi
exec python3 - <<'PY'
import json
import os
import sys
import time
import urllib.parse
import urllib.request


def main():
    env = os.environ
    # NO_PROXY=* must not silently turn a phone recovery probe into direct egress.
    env.pop('NO_PROXY', None)
    env.pop('no_proxy', None)
    token = next((env.get(k) for k in (
        'TELEGRAM_IP_RESET_BOT_TOKEN', 'ZCODE_NETWORK_RESET_TELEGRAM_BOT_TOKEN',
        'NETWORK_RESET_BOT_TOKEN', 'TELEGRAM_BOT_TOKEN') if env.get(k)), '')
    chat = next((env.get(k) for k in (
        'TELEGRAM_IP_RESET_CHAT_ID', 'ZCODE_NETWORK_RESET_TELEGRAM_CHAT_ID',
        'NETWORK_RESET_CHAT_ID', 'TELEGRAM_CHAT_ID') if env.get(k)), '')
    proxy = env.get('TELEGRAM_IP_RESET_PROXY', '')
    if not token or not chat or 'REPLACE_WITH' in token or 'REPLACE_WITH' in chat:
        print('telegram-ip-reset: missing private bot/chat configuration', file=sys.stderr)
        return 2
    if not proxy.startswith('http://127.0.0.1:'):
        print('telegram-ip-reset: configure a loopback HTTP phone proxy first', file=sys.stderr)
        return 2
    timeout = float(env.get('TELEGRAM_IP_RESET_TIMEOUT', '180'))
    delete_after = float(env.get('TELEGRAM_IP_RESET_DELETE_AFTER', '0'))
    direct = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    phone = urllib.request.build_opener(urllib.request.ProxyHandler({'http': proxy, 'https': proxy}))

    def telegram(method, fields):
        req = urllib.request.Request('https://api.telegram.org/bot' + token + '/' + method,
                                     data=urllib.parse.urlencode(fields).encode())
        with direct.open(req, timeout=15) as response:
            result = json.load(response)
        if not result.get('ok'):
            raise RuntimeError('Bot API rejected request')
        return result['result']

    message = telegram('sendMessage', {'chat_id': chat, 'text': env.get('TELEGRAM_IP_RESET_TEXT', 'network-reset')})
    print('telegram-ip-reset: trigger accepted; checking phone HTTPS egress', file=sys.stderr)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with phone.open('https://www.cloudflare.com/cdn-cgi/trace', timeout=8) as response:
                trace = response.read(8192).decode()
            if '\nip=' not in '\n' + trace:
                raise RuntimeError('No egress address')
            if delete_after > 0:
                time.sleep(delete_after)
                try:
                    telegram('deleteMessage', {'chat_id': chat, 'message_id': message['message_id']})
                except Exception:
                    print('telegram-ip-reset: optional message deletion failed', file=sys.stderr)
            print('telegram-ip-reset: phone HTTPS egress available; verify IP change separately', file=sys.stderr)
            return 0
        except Exception:
            time.sleep(5)
    print('telegram-ip-reset: phone egress recovery timed out', file=sys.stderr)
    return 1


try:
    sys.exit(main())
except Exception:
    # Exception URLs can contain the bot token. Never print their text.
    print('telegram-ip-reset: request failed; check private configuration/connectivity', file=sys.stderr)
    sys.exit(1)
PY
