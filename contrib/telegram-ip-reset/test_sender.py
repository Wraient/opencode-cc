"""Offline control-contract checks; never contact Telegram or reset a phone."""
import contextlib
import io
import json
import os
from pathlib import Path
import unittest
from unittest.mock import patch
import urllib.parse

SOURCE = (Path(__file__).with_name('telegram-ip-reset.sh').read_text()
          .split("<<'PY'\n", 1)[1].rsplit('\nPY', 1)[0])


class SenderTests(unittest.TestCase):
    def run_sender(self, env, direct=None, phone=None):
        output = io.StringIO()
        openers = [direct, phone]
        with patch.dict(os.environ, env, clear=True), patch('urllib.request.build_opener', side_effect=openers) as build, contextlib.redirect_stderr(output):
            try:
                exec(compile(SOURCE, 'telegram-ip-reset.sh', 'exec'), {})
            except SystemExit as result:
                return result.code, output.getvalue(), build.call_count
        self.fail('Sender did not exit')

    def test_missing_private_config_refuses_without_network(self):
        code, output, opened = self.run_sender({})
        self.assertEqual(code, 2)
        self.assertEqual(opened, 0)
        self.assertIn('missing private', output)

    def test_fixed_trigger_and_separate_phone_probe(self):
        from unittest.mock import Mock
        direct, phone = Mock(), Mock()
        direct.open.return_value = io.BytesIO(json.dumps({'ok': True, 'result': {'message_id': 1}}).encode())
        phone.open.return_value = io.BytesIO(b'ip=192.0.2.10\nwarp=off\n')
        env = {'TELEGRAM_IP_RESET_BOT_TOKEN': 'dummy-private-value', 'TELEGRAM_IP_RESET_CHAT_ID': 'dummy-chat',
               'TELEGRAM_IP_RESET_PROXY': 'http://127.0.0.1:18889', 'TELEGRAM_IP_RESET_TEXT': 'exact-private-trigger'}
        code, output, _ = self.run_sender(env, direct, phone)
        self.assertEqual(code, 0)
        request = direct.open.call_args.args[0]
        self.assertEqual(urllib.parse.parse_qs(request.data.decode())['text'], ['exact-private-trigger'])
        self.assertEqual(phone.open.call_args.args[0], 'https://www.cloudflare.com/cdn-cgi/trace')
        self.assertNotIn('dummy-private-value', output)
        self.assertNotIn('dummy-chat', output)
        self.assertNotIn('exact-private-trigger', output)

    def test_transport_exception_does_not_expose_token_url(self):
        from unittest.mock import Mock
        direct = Mock()
        direct.open.side_effect = RuntimeError('https://api.telegram.org/botdummy-private-value/sendMessage')
        code, output, _ = self.run_sender({'TELEGRAM_IP_RESET_BOT_TOKEN': 'dummy-private-value',
            'TELEGRAM_IP_RESET_CHAT_ID': 'dummy-chat', 'TELEGRAM_IP_RESET_PROXY': 'http://127.0.0.1:18889'}, direct, Mock())
        self.assertEqual(code, 1)
        self.assertNotIn('dummy-private-value', output)
        self.assertIn('request failed', output)


if __name__ == '__main__':
    unittest.main()
