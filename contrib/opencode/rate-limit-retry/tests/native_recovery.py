#!/usr/bin/env python3
"""Real OpenCode + loopback upstream: structured 429, deadline, context, no injected prompts.
Uses only an isolated HOME/database and mock credentials; never clears live cooldowns.
"""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import threading
import time


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--cli', default='opencode')
    parser.add_argument('--plugin', default=str(Path(__file__).resolve().parents[1]))
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    records = []
    fact = 'REMEMBERED-FACT-74219'
    phase = 'plant'
    failed = False
    mode = 'short'

    class Upstream(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            nonlocal failed
            raw = self.rfile.read(int(self.headers.get('Content-Length', 0)))
            body = json.loads(raw)
            when = time.monotonic()
            status = 200
            if phase in ('retry', 'overload') and (not failed or mode in ('long', 'no-header')):
                failed = True
                status = 503 if phase == 'overload' else 429
            records.append({'phase': phase, 'status': status, 'time': when,
                            'body_hash': hashlib.sha256(raw).hexdigest(), 'body': body})
            if status in (429, 503):
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                if mode != 'no-header':
                    self.send_header('Retry-After', '2' if mode == 'short' else '3600')
                self.end_headers()
                self.wfile.write(json.dumps({'error': {'type': 'overloaded' if status == 503 else 'FreeUsageLimitError', 'message': 'test quota or overload'}}).encode())
                return
            text = 'PLANTED' if phase == 'plant' else 'DURING-429' if phase == 'retry' else 'OVERLOAD-RECOVERED' if phase == 'overload' else fact
            if phase == 'recall':
                assert fact in json.dumps(body), 'Original remembered context missing from upstream request'
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.end_headers()
            for frame in [
                {'id': 'mock-chat', 'object': 'chat.completion.chunk', 'model': 'echo',
                 'choices': [{'index': 0, 'delta': {'role': 'assistant', 'content': text}, 'finish_reason': None}]},
                {'id': 'mock-chat', 'object': 'chat.completion.chunk', 'model': 'echo',
                 'choices': [{'index': 0, 'delta': {}, 'finish_reason': 'stop'}]},
                {'id': 'mock-chat', 'object': 'chat.completion.chunk', 'model': 'echo', 'choices': [],
                 'usage': {'prompt_tokens': 100, 'completion_tokens': 10, 'total_tokens': 110}},
            ]:
                self.wfile.write(('data: '+json.dumps(frame)+'\n\n').encode())
            self.wfile.write(b'data: [DONE]\n\n')
        def log_message(self, *_):
            pass

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Upstream)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    report = {'cli': args.cli, 'cases': []}
    try:
        with tempfile.TemporaryDirectory(prefix='opencode-native-retry-') as scratch:
            root = Path(scratch)
            env = {k: v for k, v in os.environ.items() if not k.startswith('OPENCODE_')}
            for key in ['HOME', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'XDG_CACHE_HOME']:
                env[key] = str(root/key)
            env.update(PWD=scratch, OPENCODE_CONFIG_PROJECT_DISABLE='1')
            config = {
                'enabled_providers': ['retry-test'],
                'plugins': [{'package': str(Path(args.plugin).resolve()), 'options': {
                    'stateDir': str(root/'cooldowns'), 'baseDelayMs': 1,
                    'maxDelayMs': 10, 'maxAutomaticWaitMs': 5_000, 'freeInputBudget': 0}}],
                'providers': {'retry-test': {
                    'name': 'Retry regression fixture', 'package': '@ai-sdk/openai-compatible',
                    'settings': {'baseURL': f'http://127.0.0.1:{server.server_port}/v1', 'apiKey': 'isolated-test-account'},
                    'models': {'echo': {'name': 'Echo', 'limit': {'context': 16000, 'output': 1000},
                                        'capabilities': {'tools': True, 'input': ['text'], 'output': ['text']}}}}},
            }
            env['OPENCODE_CONFIG_CONTENT'] = json.dumps(config)

            def run(message, session=None):
                command = [args.cli, 'run', '--standalone', '--format', 'json', '-m', 'retry-test/echo']
                if session:
                    command += ['--session', session]
                command += [message]
                proc = subprocess.run(command, cwd=root, env=env, stdin=subprocess.DEVNULL,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90)
                events = []
                for line in proc.stdout.decode().splitlines():
                    try:
                        event = json.loads(line)
                        if isinstance(event, dict): events.append(event)
                    except ValueError:
                        pass
                if not events:
                    raise AssertionError(f'No CLI events: exit={proc.returncode}, stdout={proc.stdout.decode()[-1500:]}, stderr={proc.stderr.decode()[-1500:]}')
                return events, proc.returncode

            planted, rc = run('Remember this exact code: '+fact+'. Reply PLANTED without using tools.')
            assert not any(e.get('type') == 'error' for e in planted), planted
            session = next(e['sessionID'] for e in planted if e.get('sessionID'))
            phase = 'retry'
            resumed, rc = run('Reply exactly DURING-429 without tools.', session)
            observed = [r for r in records if r['phase'] == 'retry']
            assert [r['status'] for r in observed] == [429, 200], [(r['status'], r['body'].get('model')) for r in observed]
            assert observed[0]['body_hash'] == observed[1]['body_hash'], 'Retry changed the request payload'
            gap = observed[1]['time'] - observed[0]['time']
            assert gap >= 2, f'Retried before Retry-After expired: {gap}'
            assert not any(e.get('type') == 'error' for e in resumed), resumed
            phase = 'recall'
            recalled, rc = run('What exact code did I ask you to remember? Reply with that code without tools.', session)
            assert any(e.get('type') == 'text' and e.get('part', {}).get('text') == fact for e in recalled), recalled
            database = root/'XDG_DATA_HOME/opencode/opencode.db'
            db = sqlite3.connect(f'file:{database}?mode=ro', uri=True)
            users = [json.loads(row[0]) for row in db.execute(
                "select data from session_message where session_id=? and type='user'", (session,))]
            assert len(users) == 3, f'Expected exactly 3 real user messages, got {len(users)}'
            assert not any('Automatic retry' in row.get('text', '') for row in users)
            report['cases'].append({'name': 'short_retry_after_recovery', 'statuses': [r['status'] for r in observed],
                                    'retry_gap_seconds': gap, 'user_messages': len(users), 'context_preserved': True,
                                    'same_retry_payload': observed[0]['body_hash'] == observed[1]['body_hash']})
            phase = 'overload'; mode = 'short'; failed = False
            overloaded, rc = run('Recover from this transient overload without tools.', session)
            observed = [r for r in records if r['phase'] == 'overload']
            assert [r['status'] for r in observed] == [503, 200], [r['status'] for r in observed]
            assert observed[1]['time'] - observed[0]['time'] >= 2
            assert not any(e.get('type') == 'error' for e in overloaded), overloaded
            report['cases'].append({'name': 'transient_overload_recovery', 'statuses': [503, 200],
                                    'retry_gap_seconds': observed[1]['time'] - observed[0]['time']})
            phase = 'retry'; mode = 'long'; failed = False
            before = len(records)
            blocked, rc = run('Reply after the long quota test, without tools.', session)
            assert any(e.get('type') == 'error' for e in blocked), blocked
            assert len(records) == before + 1 and records[-1]['status'] == 429
            # Explicitly start another turn before the deadline: must be rejected locally.
            blocked_again, rc = run('Check quota again without tools.', session)
            assert any(e.get('type') == 'error' for e in blocked_again), blocked_again
            assert len(records) == before + 1, 'Long quota was bypassed by a new turn/process'
            report['cases'].append({'name': 'long_quota_cross_process_gate', 'upstream_requests': 1,
                                    'second_turn_rejected_locally': True})
            # A separate credential scope must still reach upstream. With a hard
            # quota and no header, it must stop after one request, not hammer it.
            config['providers']['retry-test']['settings']['apiKey'] = 'separate-isolated-test-account'
            env['OPENCODE_CONFIG_CONTENT'] = json.dumps(config)
            phase = 'retry'; mode = 'no-header'; failed = False
            before = len(records)
            exhausted, rc = run('Test hard quota without a Retry-After, without tools.', session)
            assert any(e.get('type') == 'error' for e in exhausted), exhausted
            assert len(records) == before + 1 and records[-1]['status'] == 429
            report['cases'].append({'name': 'hard_quota_without_deadline', 'upstream_requests': 1,
                                    'different_account_scope_unblocked': True, 'automatic_retries': 0})
            db.close()
            report['result'] = 'PASS'
    finally:
        server.shutdown()
        Path(args.out).write_text(json.dumps(report, indent=2)+'\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
