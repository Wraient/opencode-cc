#!/usr/bin/env python3
"""Opt-in live same-model recovery test with isolated CPA/relay and a SOCKS fault shim.

Requires WARP's local proxy, a staging relay binary, and CLIProxyAPI v8. Does not
disconnect WARP, restart production, execute model tools, or alter live cooldowns.
"""
import argparse
import json
import os
from pathlib import Path
import select
import socket
import socketserver
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request


def receive(sock, size):
    data = b''
    while len(data) < size:
        part = sock.recv(size - len(data))
        if not part:
            raise ConnectionError('Closed SOCKS handshake')
        data += part
    return data


def address(sock, kind):
    if kind == 1:
        return receive(sock, 6)
    if kind == 4:
        return receive(sock, 18)
    size = receive(sock, 1)
    return size + receive(sock, size[0] + 2)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--relay', default='/tmp/opencode-cc-stage')
    parser.add_argument('--cpa', default='/tmp/cli-proxy-api-stage')
    parser.add_argument('--data', default=str(Path.home()/'data'))
    parser.add_argument('--out', required=True)
    args = parser.parse_args()
    report = {'model': 'deepseek-v4.1-flash-oc', 'cases': [], 'result': 'FAIL'}
    fixture_key = 'isolated-recovery-test-only'
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request():
        started = time.monotonic()
        body = {'model': report['model'], 'messages': [{'role': 'user', 'content': 'Reply exactly OK.'}],
                'max_tokens': 256}
        req = urllib.request.Request('http://127.0.0.1:18317/v1/chat/completions',
            data=json.dumps(body).encode(), headers={'Authorization': 'Bearer '+fixture_key,
                                                    'Content-Type': 'application/json'})
        try:
            response = opener.open(req, timeout=90)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = json.loads(response.read(1024*1024))
            result = {'status': response.status, 'elapsed_s': round(time.monotonic()-started, 3),
                      'retry_after': response.headers.get('Retry-After')}
        if data.get('choices'):
            result['text'] = data['choices'][0].get('message', {}).get('content')
            result['returned_model'] = data.get('model')
        else:
            result['error_code'] = data.get('error', {}).get('code')
        return result

    def success(result):
        assert result['status'] == 200 and result.get('text', '').strip() in ('OK', 'OK.'), result
        assert result['returned_model'] in ('deepseek-v4.1-flash-free', report['model']), result

    try:
        for persistent in (False, True):
            lock = threading.Lock()
            state = {'remaining': 1000 if persistent else 3, 'failed': 0, 'forwarded': 0}

            class Socks(socketserver.BaseRequestHandler):
                def handle(self):
                    client = self.request
                    client.settimeout(90)
                    try:
                        hello = receive(client, 2)
                        receive(client, hello[1])
                        client.sendall(b'\x05\x00')
                        head = receive(client, 4)
                        destination = address(client, head[3])
                        with lock:
                            fail = state['remaining'] > 0
                            if fail:
                                state['remaining'] -= 1
                                state['failed'] += 1
                            else:
                                state['forwarded'] += 1
                        if fail:
                            client.sendall(b'\x05\x04\x00\x01'+b'\x00'*6)
                            return
                        with socket.create_connection(('127.0.0.1', 40000), timeout=15) as warp:
                            warp.sendall(b'\x05\x01\x00')
                            assert receive(warp, 2) == b'\x05\x00'
                            warp.sendall(head+destination)
                            reply = receive(warp, 4)
                            client.sendall(reply+address(warp, reply[3]))
                            if reply[1] != 0:
                                return
                            while True:
                                readable, _, _ = select.select([client, warp], [], [], 90)
                                if not readable:
                                    return
                                for source in readable:
                                    data = source.recv(65536)
                                    if not data:
                                        return
                                    (warp if source is client else client).sendall(data)
                    except (OSError, AssertionError):
                        return

            class Server(socketserver.ThreadingTCPServer):
                allow_reuse_address = True
                daemon_threads = True

            with tempfile.TemporaryDirectory(prefix='cc-chain-recovery-') as td, Server(('127.0.0.1', 0), Socks) as shim:
                root = Path(td)
                threading.Thread(target=shim.serve_forever, daemon=True).start()
                cfg = json.loads((Path(args.data)/'config.json').read_text())
                cfg.update(listen_addr='127.0.0.1:18789', require_api_key=False, panel_token='',
                           log_requests=False, max_body_log_bytes=0)
                cfg['upstreams'] = [u for u in cfg['upstreams'] if u.get('enabled') and
                                    u['base_url'].startswith('https://opencode.ai/')]
                assert cfg['upstreams'], 'No official inference upstream'
                for upstream in cfg['upstreams']:
                    upstream['api_key'] = 'opencode://active'
                (root/'relay').mkdir()
                (root/'relay/config.json').write_text(json.dumps(cfg))
                (root/'relay/config.json').chmod(0o600)
                cpa = {'config-version': 8, 'server': {'host': '127.0.0.1', 'port': 18317},
                    'access': {'api-keys': [fixture_key]}, 'oauth': {'auth-dir': str(root/'auth')},
                    'requests': {'passthrough-headers': True},
                    'routing': {'retry': {'request-retry': 1, 'max-retry-interval': 5},
                                'cooldown': {'transient-error-cooldown-seconds': 60, 'save-cooldown-status': False}},
                    'api-keys': {'claude': [{'name': 'same-model-recovery', 'base-url': 'http://127.0.0.1:18789',
                        'keys': [{'api-key': fixture_key}], 'models': [{'name': 'deepseek-v4.1-flash-free',
                                                                     'alias': report['model']}]}]}}
                (root/'cpa.json').write_text(json.dumps(cpa))
                env = dict(os.environ)
                proxy = f'socks5h://127.0.0.1:{shim.server_address[1]}'
                env.update(OPENCODE_CC_LISTEN='127.0.0.1:18789', HTTP_PROXY=proxy, HTTPS_PROXY=proxy,
                           NO_PROXY='127.0.0.1,localhost,::1')
                processes = []
                with (root/'relay.log').open('w') as relay_log, (root/'cpa.log').open('w') as cpa_log:
                    try:
                        processes.append(subprocess.Popen([args.relay, '-data', str(root/'relay')],
                            env=env, stdout=relay_log, stderr=relay_log))
                        processes.append(subprocess.Popen([args.cpa, '-config', str(root/'cpa.json')],
                            cwd=root, stdout=cpa_log, stderr=cpa_log))
                        for _ in range(100):
                            assert all(p.poll() is None for p in processes), 'Isolated service exited'
                            try:
                                with socket.create_connection(('127.0.0.1', 18317), timeout=.1):
                                    break
                            except OSError:
                                time.sleep(.1)
                        else:
                            raise AssertionError('CPA did not start')
                        result = request()
                        if not persistent:
                            success(result)
                            assert state['failed'] == 3, state
                            report['cases'].append({'name': 'same_request_recovers_after_three_connect_failures',
                                                    **result, 'injected_failures': state['failed']})
                        else:
                            assert result['status'] == 503 and result['retry_after'] == '2', result
                            assert state['failed'] <= 6, state
                            report['cases'].append({'name': 'persistent_outage_is_service_unavailable', **result,
                                                    'injected_failures': state['failed']})
                            with lock:
                                state['remaining'] = 0
                            recovered = request()
                            success(recovered)
                            assert recovered['elapsed_s'] < 20, recovered
                            report['cases'].append({'name': 'same_alias_recovers_without_auth_reset', **recovered})
                        print(json.dumps(report['cases'][-1]), flush=True)
                    finally:
                        for process in reversed(processes):
                            process.terminate()
                            try:
                                process.wait(timeout=5)
                            except subprocess.TimeoutExpired:
                                process.kill()
                                process.wait()
                        shim.shutdown()
            report['result'] = 'PASS'
    finally:
        Path(args.out).write_text(json.dumps(report, indent=2)+'\n')


if __name__ == '__main__':
    main()
