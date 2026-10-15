#!/usr/bin/env python3
"""Compare an actual released OpenCode CLI with this proxy using its exact payload.

The CLI uses an isolated HOME and a loopback measuring relay to the configured
upstream. Nothing executes client tools: prompts explicitly request prose.
Proxy runs replay the CLI's exact primary-model body. Credentials and request
bodies are never written to the report. Concurrency is a bounded observation,
not a quota-exhaustion test. Supply the key via ZEN_API_KEY (never an argument).
"""
import argparse
import concurrent.futures
import http.server
import json
import os
from pathlib import Path
import statistics
import subprocess
import tempfile
import threading
import time

import requests

PROMPT = ("Explain how a token bucket rate limiter works in about 120 words. "
          "Use plain prose. Do not use any tools or inspect files. Respond directly.")


def stream_measure(url, headers, body, relay=None):
    start = time.perf_counter()
    first = visible = last = None
    usage = {}
    chunks = 0
    result = {}
    try:
        with requests.post(url, headers=headers, json=body, stream=True, timeout=(15, 90)) as response:
            result['status'] = response.status_code
            result['rate_headers'] = {k.lower(): v for k, v in response.headers.items()
                                      if 'ratelimit' in k.lower() or k.lower() == 'retry-after'}
            if relay:
                relay.send_response(response.status_code)
                relay.send_header('Content-Type', response.headers.get('Content-Type', 'application/json'))
                relay.send_header('Connection', 'close')
                relay.end_headers()
                relay.close_connection = True
            if response.status_code != 200:
                raw = response.content
                result['error'] = raw.decode(errors='replace')[:400]
                if relay:
                    relay.wfile.write(raw)
                return result
            for raw in response.iter_lines(chunk_size=1):
                now = time.perf_counter()
                if now - start > 90:
                    raise TimeoutError('stream exceeded 90s absolute benchmark deadline')
                if relay:
                    relay.wfile.write(raw + b'\n')
                    relay.wfile.flush()
                if not raw.startswith(b'data:'):
                    continue
                if raw[5:].strip() == b'[DONE]':
                    result['terminal'] = 'done'
                    continue
                try:
                    obj = json.loads(raw[5:].strip())
                except (ValueError, TypeError):
                    continue
                piece = text = ''
                for choice in obj.get('choices', []):
                    delta = choice.get('delta') or {}
                    text += delta.get('content') or ''
                    piece += delta.get('content') or delta.get('reasoning_content') or delta.get('reasoning') or ''
                kind = obj.get('type', '')
                if obj.get('error'):
                    result['error'] = json.dumps(obj['error'])[:400]
                if kind == 'response.output_text.delta':
                    text = piece = obj.get('delta', '')
                elif kind in ('response.reasoning_text.delta', 'response.reasoning_summary_text.delta'):
                    piece = obj.get('delta', '')
                if text:
                    visible = visible or now
                if piece:
                    first = first or now
                    last = now
                    chunks += 1
                if obj.get('usage'):
                    usage = obj['usage']
                if kind in ('response.completed', 'response.incomplete', 'response.failed'):
                    usage = obj.get('response', {}).get('usage') or usage
                    result['terminal'] = kind
                    if kind != 'response.completed':
                        result['error'] = kind
    except Exception as exc:
        result['error'] = str(exc)[:400]
    finally:
        end = time.perf_counter()
        tokens = usage.get('completion_tokens', usage.get('output_tokens'))
        window = (last - first) if first and last else 0
        result.update(ttft_s=first-start if first else None,
                      visible_ttft_s=visible-start if visible else None,
                      total_s=end-start, output_tokens=tokens,
                      input_tokens=usage.get('prompt_tokens', usage.get('input_tokens')),
                      tok_s=tokens/window if tokens and window > 0.05 and chunks > 1 else None,
                      tok_s_e2e=tokens/(end-start) if tokens else None,
                      content_chunks=chunks)
        if result.get('status') == 200 and not result.get('terminal'):
            result.setdefault('error', 'stream ended without completion event')
    return result


def summary(rows):
    good = [r for r in rows if r.get('status') == 200 and r.get('ttft_s') is not None and not r.get('error')]
    return dict(requests=len(rows), successful=len(good),
                **{k: statistics.median([r[k] for r in good if r.get(k) is not None])
                   if any(r.get(k) is not None for r in good) else None
                   for k in ('ttft_s', 'visible_ttft_s', 'tok_s', 'tok_s_e2e', 'total_s')})


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--cli', required=True)
    ap.add_argument('--proxy', default='http://127.0.0.1:18787')
    ap.add_argument('--upstream', default='https://opencode.ai/inference')
    ap.add_argument('--models', default='mimo-v2.6-flash-free,muse-spark-1.3-contributor-free')
    ap.add_argument('--runs', type=int, default=3)
    ap.add_argument('--concurrency', default='2,4')
    ap.add_argument('--out', required=True)
    args = ap.parse_args()
    key = os.environ['ZEN_API_KEY']
    levels = [int(x) for x in args.concurrency.split(',') if x]
    if not 1 <= args.runs <= 10 or any(x < 1 or x > 4 for x in levels):
        ap.error('runs must be 1..10; concurrency must be 1..4')
    records = []
    lock = threading.Lock()

    class Relay(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            headers = {k: v for k, v in self.headers.items()
                       if k.lower() not in ('host', 'content-length', 'connection', 'accept-encoding')}
            headers['Authorization'] = 'Bearer ' + key
            row = stream_measure(args.upstream.rstrip('/') + self.path, headers, body, self)
            with lock:
                records.append((body, headers, self.path, row))
        def log_message(self, *_):
            pass

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Relay)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    report = {'cli_version': subprocess.check_output([args.cli, '--version'], text=True).strip(),
              'upstream': args.upstream, 'proxy': args.proxy, 'models': {},
              'method': 'Actual CLI through loopback measuring relay; exact primary body replayed to proxy. '
                        'TTFT includes first reasoning or text; visible_ttft is text only. '
                        'tok_s uses upstream output tokens over first-to-last text/reasoning delta; '
                        'this can be inflated when hidden reasoning is not streamed. '
                        'tok_s_e2e divides all reported output tokens by the full request duration. '
                        'The measuring relay uses Python requests upstream, so this is request-contract '
                        'parity, not a measurement of the CLI native TLS transport. '
                        'CLI startup is reported separately. No quota exhaustion.'}
    try:
        with tempfile.TemporaryDirectory(prefix='cc-parity-') as scratch:
            env = os.environ.copy()
            for name in ('HOME', 'XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'XDG_CACHE_HOME'):
                env[name] = str(Path(scratch, name))
            env.update(PWD=scratch, OPENCODE_API_KEY=key, OPENCODE_CONFIG_PROJECT_DISABLE='1',
                       OPENCODE_CONFIG_CONTENT=json.dumps({'providers': {'opencode': {'settings': {
                           'baseURL': f'http://127.0.0.1:{server.server_port}/v1'}}}}))
            for model in args.models.split(','):
                cli_rows, proxy_rows = [], []
                template = None
                for i in range(args.runs):
                    with lock:
                        before = len(records)
                    start = time.perf_counter()
                    proc = subprocess.run([args.cli, 'run', '--standalone', '--format', 'json',
                                           '-m', 'opencode/' + model, PROMPT],
                                          cwd=scratch, env=env, stdin=subprocess.DEVNULL,
                                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=150)
                    elapsed = time.perf_counter() - start
                    with lock:
                        # The CLI can also use the same model for its title.
                        # Its primary generation carries the harness toolset.
                        primary = [x for x in records[before:] if x[0].get('model') == model and x[0].get('tools')]
                    if not primary:
                        raise RuntimeError(f'{model}: expected a primary request, got {len(primary)}; '
                                           f'exit={proc.returncode}, stderr={proc.stderr.decode()[-500:]}')
                    body, headers, path, row = primary[0]
                    row = dict(row, cli_wall_s=elapsed, cli_exit=proc.returncode, primary_requests=len(primary))
                    cli_rows.append(row)
                    template = (body, headers, path)
                    proxy_headers = {k: v for k, v in headers.items()
                                     if k.lower().startswith('x-') and k.lower() != 'x-opencode-org-id'}
                    if os.environ.get('OPENCODE_CC_BENCH_KEY'):
                        proxy_headers['Authorization'] = 'Bearer ' + os.environ['OPENCODE_CC_BENCH_KEY']
                    proxy_row = stream_measure(args.proxy.rstrip('/') + path, proxy_headers, body)
                    proxy_rows.append(proxy_row)
                    print(model, i+1, 'cli', summary([row]), 'proxy', summary([proxy_row]), flush=True)
                results = dict(cli=cli_rows, proxy=proxy_rows,
                               cli_summary=summary(cli_rows), proxy_summary=summary(proxy_rows))
                body, headers, path = template
                results['cli_request_contract'] = {
                    'path': path, 'body_fields': sorted(body),
                    'header_names': sorted(k.lower() for k in headers if k.lower() != 'authorization'),
                    'tools': [t.get('name', t.get('function', {}).get('name')) for t in body.get('tools', [])]}
                # Check schema drift against the versioned reference, without saving prompts or secrets.
                defs = json.loads(Path(__file__).resolve().parents[1].joinpath('internal/proxy/opencode/tools.json').read_text())
                captured = {t.get('name', t.get('function', {}).get('name')): t.get('function', t)
                            for t in body.get('tools', [])}
                results['tool_reference_matches'] = {
                    d['name']: all(captured.get(d['name'], {}).get(k) == v for k, v in d.items()) for d in defs}
                results['concurrency'] = {}
                for level in levels:
                    level_rows = {}
                    for lane, base, lane_headers in [('cli_wire', args.upstream, headers),
                                                      ('proxy', args.proxy, proxy_headers)]:
                        with concurrent.futures.ThreadPoolExecutor(max_workers=level) as pool:
                            rows = list(pool.map(lambda _: stream_measure(base.rstrip('/')+path, lane_headers, body), range(level)))
                        level_rows[lane] = dict(summary=summary(rows), rows=rows)
                    results['concurrency'][str(level)] = level_rows
                    print(model, 'concurrency', level, {k:v['summary'] for k,v in level_rows.items()}, flush=True)
                report['models'][model] = results
                Path(args.out).write_text(json.dumps(report, indent=2).replace(key, '<redacted>')+'\n')
    finally:
        server.shutdown()
        server.server_close()
    return 0 if all(x['proxy_summary']['successful'] == args.runs for x in report['models'].values()) else 1


if __name__ == '__main__':
    raise SystemExit(main())
