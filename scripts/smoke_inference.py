#!/usr/bin/env python3
"""Opt-in live inference smoke test. Never executes returned tool calls.

Use an isolated staging listener first. Optional client authentication comes from
OPENCODE_CC_TEST_API_KEY, not command-line arguments. Reports contain no keys.
"""
import argparse
import json
import os
from pathlib import Path
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', default='http://127.0.0.1:18789')
    parser.add_argument('--models', nargs='+', default=[
        'deepseek-v4.1-flash-free', 'space-bunny-free', 'cat-jam-free'])
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    report = {'base': args.base, 'tests': [], 'result': 'FAIL'}
    headers = {'Content-Type': 'application/json', 'anthropic-version': '2023-06-01'}
    if key := os.environ.get('OPENCODE_CC_TEST_API_KEY'):
        headers['Authorization'] = 'Bearer ' + key
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def call(route, body, label):
        row = {'case': label, 'route': route, 'model': body['model']}
        report['tests'].append(row)
        started = time.monotonic()
        request = urllib.request.Request(args.base.rstrip('/') + route,
                                         data=json.dumps(body).encode(), headers=headers)
        try:
            with opener.open(request, timeout=60) as response:
                row['status'] = response.status
                if body.get('stream'):
                    text, done, first, count = '', False, None, 0
                    for raw in response:
                        count += len(raw)
                        if count > 1024 * 1024 or time.monotonic() - started > 60:
                            raise AssertionError('Stream exceeded smoke-test limits')
                        if not raw.startswith(b'data:'):
                            continue
                        raw = raw[5:].strip()
                        if raw == b'[DONE]':
                            done = True
                            continue
                        try:
                            event = json.loads(raw)
                        except ValueError:
                            continue
                        piece = ''.join(c.get('delta', {}).get('content') or ''
                                        for c in event.get('choices', []))
                        if event.get('type') == 'response.output_text.delta':
                            piece += event.get('delta', '')
                        if event.get('type') == 'response.completed':
                            done = True
                        if piece and first is None:
                            first = time.monotonic() - started
                        text += piece
                    row.update(text=text, completed=done, visible_ttft_s=first)
                    # Check a real acknowledgement and protocol completion;
                    # punctuation is a model choice, not a transport failure.
                    assert done and text.strip() in ('OK', 'OK.'), 'Incomplete or unexpected streaming output'
                else:
                    data = json.loads(response.read(1024 * 1024))
                    if label == 'forced_client_tool':
                        function = data['choices'][0]['message']['tool_calls'][0]['function']
                        assert function['name'] == 'get_weather'
                        assert json.loads(function['arguments'])['city'] == 'Paris'
                        row['tool_call_verified'] = True
                    else:
                        text = ''.join(c.get('message', {}).get('content') or ''
                                       for c in data.get('choices', [])) or ''.join(
                            c.get('text', '') for c in data.get('content', []))
                        row['text'] = text
                        assert text.strip() in ('OK', 'OK.'), 'Unexpected nonstreaming output'
                    row['usage'] = data.get('usage')
        except urllib.error.HTTPError as error:
            row['status'] = error.code
            try:
                detail = json.loads(error.read(4096)).get('error', {})
                row['error_type'] = detail.get('type')
                row['error_code'] = detail.get('code')
            except (ValueError, AttributeError):
                pass
            raise AssertionError(f'{label}: HTTP {error.code}') from None
        finally:
            row['elapsed_s'] = round(time.monotonic() - started, 3)
            print(json.dumps(row), flush=True)

    def chat(model, stream=False):
        return {'model': model, 'messages': [{'role': 'user', 'content': 'Reply exactly OK.'}],
                'max_tokens': 256, 'stream': stream}

    try:
        for model in args.models:
            call('/v1/chat/completions', chat(model), 'chat')
        call('/v1/messages', chat(args.models[0]), 'messages')
        call('/v1/chat/completions', chat(args.models[-1], True), 'chat_stream')
        call('/v1/responses', {'model': args.models[0], 'input': 'Reply exactly OK.',
                              'max_output_tokens': 256, 'stream': True}, 'responses_stream')
        body = chat(args.models[0])
        body['messages'][0]['content'] = 'Call get_weather for Paris.'
        body['tools'] = [{'type': 'function', 'function': {'name': 'get_weather',
            'description': 'Look up weather by city.', 'parameters': {'type': 'object',
            'properties': {'city': {'type': 'string'}}, 'required': ['city'],
            'additionalProperties': False}}}]
        body['tool_choice'] = {'type': 'function', 'function': {'name': 'get_weather'}}
        call('/v1/chat/completions', body, 'forced_client_tool')
        report['result'] = 'PASS'
    finally:
        Path(args.out).write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    main()
