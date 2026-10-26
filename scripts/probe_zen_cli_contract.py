#!/usr/bin/env python3
"""Capture one real CLI request in memory, then replay it without persisting secrets.

The local capture returns an intentional 401 to stop CLI retries. Replays use
curl's TLS transport, so this measures request-contract parity, not native TLS.
Only the requested free DeepSeek model is used. No model tools are executed.
"""
import argparse
import concurrent.futures
import hashlib
import http.server
import json
import os
from pathlib import Path
import signal
import socket
import sqlite3
import subprocess
import tempfile
import threading
import time

from probe_zen_free_access import BODY, MODEL, ROOT, ROUTES, credentials, fingerprint, identity, safe_headers, timestamp, trace


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--routes", nargs="+", choices=ROUTES, default=["phone", "direct"])
    ap.add_argument("--credentials", nargs="+", choices=["saved", "active", "legacy", "invalid", "none"], default=["saved", "invalid"])
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--label", required=True)
    ap.add_argument("--cli", default=str(ROOT / ".opencode/bin/opencode"))
    ap.add_argument("--fixture", type=Path, help="Private, bearer-free request fixture; reuse unchanged across IP resets")
    ap.add_argument("--family", choices=["auto", "4", "6"], default="auto")
    ap.add_argument("--organization", choices=["fixture", "credential", "none"], default="fixture")
    ap.add_argument("--curl-http", choices=["auto", "1.1", "2"], default="auto")
    ap.add_argument("--bun-wire-headers", action="store_true", help="Control for Bun's automatic encoding and connection headers")
    ap.add_argument("--upstream", choices=["https://opencode.ai/inference", "https://opencode.ai/inference/openai"],
                    default=None)
    args = ap.parse_args()
    saved = json.loads((ROOT / "data/config.json").read_text())
    keys = credentials(saved)
    keys["none"] = ""
    path = ROOT / ".local/share/opencode/opencode.db"
    with sqlite3.connect(f"file:{path}?mode=ro", uri=True) as db:
        item = json.loads(db.execute("SELECT value FROM credential WHERE integration_id='opencode' AND active=1").fetchone()[0])
    active_token = item.get("access") or item.get("key")
    sensitive = [x for x in list(keys.values()) + [active_token] if x and len(x) > 15]
    args.out.parent.mkdir(parents=True, exist_ok=True)

    def emit(row):
        row.update(time=timestamp(), label=args.label)
        line = json.dumps(row, sort_keys=True)
        for value in sensitive:
            line = line.replace(value, "<redacted>")
        with args.out.open("a") as out:
            out.write(line + "\n")
        print(line, flush=True)

    captured = []

    class Capture(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            size = int(self.headers.get("Content-Length", "0"))
            if size > 2 * 1024 * 1024:
                self.send_error(413)
                return
            body = self.rfile.read(size)
            parsed = json.loads(body)
            if parsed.get("model") == MODEL:
                captured.append((self.path, dict(self.headers), body))
            response = json.dumps({"error": {"type": "authentication_error", "message": "Diagnostic capture complete"}}).encode()
            self.send_response(401)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response)))
            self.end_headers()
            self.wfile.write(response)

        def log_message(self, *_):
            pass

    native_url = None
    if args.fixture and args.fixture.exists():
        fixture = json.loads(args.fixture.read_text())
        native_url = fixture.get("url")
        captured.append((fixture["path"], fixture["headers"], fixture["body"].encode()))
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Capture)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix="cc-cli-contract-") as directory:
            env = os.environ.copy()
            for name in ("HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"):
                env[name] = directory + "/" + name
            for name in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"):
                env.pop(name, None)
            env.update(PWD=directory, OPENCODE_API_KEY=keys["saved"], NO_PROXY="*", no_proxy="*",
                       OPENCODE_CONFIG_PROJECT_DISABLE="1", OPENCODE_CONFIG_CONTENT=json.dumps({
                           "snapshot": False, "providers": {"opencode": {"settings": {
                               "baseURL": f"http://127.0.0.1:{server.server_port}/v1"},
                               "models": {MODEL: {"settings": {
                                   "baseURL": f"http://127.0.0.1:{server.server_port}/v1"}}}}}}))
            with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
                if captured:
                    proc = None
                else:
                    proc = subprocess.Popen([args.cli, "run", "--standalone", "--format", "json", "-m", "opencode/" + MODEL,
                                             "Reply exactly OK. Do not use tools or inspect files."], cwd=directory,
                                            env=env, stdout=stdout, stderr=stderr, start_new_session=True)
                try:
                    if proc:
                        proc.wait(timeout=45)
                except subprocess.TimeoutExpired:
                    os.killpg(proc.pid, signal.SIGTERM)
                    proc.wait(timeout=5)
                if not captured:
                    stdout.seek(0)
                    stderr.seek(0)
                    emit({"event": "capture_failed", "cli_exit": proc.returncode,
                          "stdout": stdout.read(2000).decode(errors="replace"),
                          "stderr": stderr.read(2000).decode(errors="replace")})
                    return
    finally:
        server.shutdown()
        server.server_close()

    request_path, headers, body = next((r for r in captured if json.loads(r[2]).get("tools")), captured[0])
    if args.fixture and not args.fixture.exists():
        clean_headers = {k: v for k, v in headers.items() if k.lower() not in ("authorization", "cookie", "proxy-authorization")}
        fixture = json.dumps({"path": request_path, "headers": clean_headers, "body": body.decode()})
        if any(value in fixture for value in sensitive):
            raise RuntimeError("Refusing to persist a fixture containing a credential")
        args.fixture.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with args.fixture.open("x") as out:
            os.chmod(args.fixture, 0o600)
            out.write(fixture)
    parsed = json.loads(body)
    emit({"event": "cli_contract", "cli_version": subprocess.check_output([args.cli, "--version"], text=True).strip(),
          "path": request_path, "body_fields": sorted(parsed), "request_sha256": hashlib.sha256(body).hexdigest(),
          "request_bytes": len(body), "header_names": sorted(k.lower() for k in headers),
          "tools": [t.get("name") or t.get("function", {}).get("name") for t in parsed.get("tools", [])],
          "stream": parsed.get("stream"), "model": parsed.get("model"),
          "user_agent": next((v for k, v in headers.items() if k.lower() == "user-agent"), None),
          "organization_header_present": any(k.lower() == "x-opencode-org-id" and bool(v) for k, v in headers.items())})
    with tempfile.TemporaryDirectory(prefix="cc-contract-replay-") as directory:
        request_file = Path(directory) / "request.json"
        request_file.write_bytes(body)
        request_file.chmod(0o600)
        for route in args.routes:
            emit({"event": "egress_before", "route": route, "result": trace(route)})
            for name in args.credentials:
                token = active_token if name == "active" else keys[name]
                request_headers = {k: v for k, v in headers.items() if k.lower() not in (
                    "host", "content-length", "authorization", "connection", "accept-encoding")}
                if args.bun_wire_headers:
                    request_headers = {(k.title() if k.lower() in ("content-type", "user-agent") else k): v
                                       for k, v in request_headers.items()}
                    request_headers.update({"Accept-Encoding": "gzip, deflate, br, zstd", "Connection": "keep-alive"})
                if args.organization == "none" or (args.organization == "credential" and name in ("active", "invalid", "none")):
                    request_headers = {k: v for k, v in request_headers.items() if k.lower() not in ("x-opencode-org-id", "x-org-id")}
                if args.organization == "credential" and name == "active":
                    org = item.get("metadata", {}).get("orgID")
                    if org:
                        request_headers["x-opencode-org-id"] = org
                if token:
                    request_headers["Authorization"] = "Bearer " + token
                config = "\n".join("header = " + json.dumps(k + ": " + v) for k, v in request_headers.items()) + "\n"
                response_file = Path(directory) / "response"
                header_file = Path(directory) / "headers"
                endpoint = native_url or "https://opencode.ai/inference" + request_path
                if args.upstream:
                    relative = request_path[request_path.index("/v1/"):]
                    endpoint = args.upstream + relative
                command = ["curl", "--config", "-", "-sS", "--max-time", "40", "--noproxy", "" if ROUTES[route] else "*",
                           "--max-filesize", "1048576", "--data-binary", "@" + str(request_file),
                           "-o", str(response_file), "-D", str(header_file), "-w", "%{http_code}",
                           endpoint]
                if ROUTES[route]:
                    command += ["--proxy", ROUTES[route]]
                if args.curl_http != "auto":
                    command += ["--http" + args.curl_http]
                if args.bun_wire_headers:
                    command += ["--compressed"]
                target_address = None
                if args.family != "auto":
                    family = socket.AF_INET if args.family == "4" else socket.AF_INET6
                    target_address = socket.getaddrinfo("opencode.ai", 443, family, socket.SOCK_STREAM)[0][4][0]
                    literal = "[" + target_address + "]" if args.family == "6" else target_address
                    command += ["--connect-to", "opencode.ai:443:" + literal + ":443"]
                start = time.monotonic()
                call = subprocess.run(command, input=config, capture_output=True, text=True)
                response = response_file.read_bytes()[:16384] if response_file.exists() else b""
                raw_headers = header_file.read_text() if header_file.exists() else ""
                response_headers = {}
                for line in raw_headers.splitlines():
                    if ":" in line:
                        k, v = line.split(":", 1)
                        response_headers[k] = v.strip()
                emit({"event": "cli_contract_replay", "route": route,
                      "credential": {"name": "none", "kind": "anonymous"} if name == "none" else identity(name, keys[name]),
                      "family": args.family, "target_address": target_address,
                      "curl_http": args.curl_http,
                      "bun_wire_headers": args.bun_wire_headers,
                      "upstream": endpoint,
                      "organization_mode": args.organization,
                      "organization_hash": fingerprint(next((v for k, v in request_headers.items()
                                                             if k.lower() == "x-opencode-org-id"), "")),
                      "status": call.stdout, "curl_exit": call.returncode, "seconds": round(time.monotonic() - start, 3),
                      "headers": safe_headers(response_headers), "response": response.decode(errors="replace"),
                      "error": call.stderr[:500]})
            emit({"event": "egress_after", "route": route, "result": trace(route)})


if __name__ == "__main__":
    main()
