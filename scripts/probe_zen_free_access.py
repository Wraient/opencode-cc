#!/usr/bin/env python3
"""Bounded, opt-in DeepSeek Free diagnostics; never prints or exports credentials.

Uses an isolated relay on 18789, never production 8787. Build the stage binary
with the project's required build/test procedure before running. The phone
route is HTTP CONNECT, not SOCKS. Reports include raw upstream error metadata
from the isolated relay database; no request bodies, authorization, or cookies.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path.home()
ROUTES = {"direct": None, "warp": "socks5h://127.0.0.1:40000",
          "phone": "http://127.0.0.1:18889"}
MODEL = "deepseek-v4.1-flash-free"
BODY = {"model": MODEL, "messages": [{"role": "user", "content": "Reply exactly OK."}],
        "max_tokens": 256, "stream": False}


def timestamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def fingerprint(value):
    return hashlib.sha256(value.encode()).hexdigest()[:12]


def credentials(saved):
    result = {"saved": saved["upstreams"][0]["api_key"],
              "active": "opencode://active",
              "invalid": "diagnostic-invalid-not-a-real-api-key"}
    old = json.loads((ROOT / ".local/share/opencode/auth.json").read_text())["opencode"]
    result["legacy"] = old.get("key") or old.get("access")
    return result


def identity(name, value):
    if name != "active":
        return {"name": name, "kind": "invalid-control" if name == "invalid" else "api-key",
                "fingerprint": fingerprint(value)}
    path = ROOT / ".local/share/opencode/opencode.db"
    with sqlite3.connect(f"file:{path}?mode=ro", uri=True) as db:
        rows = db.execute("SELECT value FROM credential WHERE integration_id='opencode' AND active=1").fetchall()
    if len(rows) != 1:
        raise RuntimeError("Expected exactly one active OpenCode credential")
    item = json.loads(rows[0][0])
    token = item.get("access") or item.get("key")
    return {"name": name, "kind": item["type"], "fingerprint": fingerprint(token),
            "expires": item.get("expires")}


def safe_headers(headers):
    return {k.lower(): v for k, v in headers.items()
            if k.lower() in ("date", "retry-after", "cf-ray", "x-request-id", "request-id", "content-type")
            or k.lower().startswith(("x-ratelimit-", "ratelimit-"))}


def trace(route):
    result = {}
    for name, url in [("ipv4", "https://api.ipify.org"),
                      ("trace", "https://www.cloudflare.com/cdn-cgi/trace")]:
        command = ["curl", "-sS", "--max-time", "10", "--noproxy", "" if ROUTES[route] else "*"]
        if ROUTES[route]:
            command += ["--proxy", ROUTES[route]]
        call = subprocess.run(command + [url], capture_output=True, text=True)
        result[name] = {"exit": call.returncode, "body": call.stdout[:1500], "error": call.stderr[:500]}
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--routes", nargs="+", choices=ROUTES, default=["phone", "direct"])
    parser.add_argument("--credentials", nargs="+", choices=["saved", "active", "legacy", "invalid"],
                        default=["saved", "active"])
    parser.add_argument("--stage", default="/tmp/opencode-cc-stage")
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--label", required=True)
    parser.add_argument("--openai-fixture", type=Path,
                        help="Replay a private bearer-free native fixture through staging's OpenAI endpoint")
    parser.add_argument("--upstream", choices=["https://opencode.ai/inference", "https://opencode.ai/inference/openai",
                                               "http://127.0.0.1:18790"])
    args = parser.parse_args()
    saved = json.loads((ROOT / "data/config.json").read_text())
    body, client_headers, client_path = BODY, {"anthropic-version": "2023-06-01"}, "/v1/messages"
    if args.openai_fixture:
        fixture = json.loads(args.openai_fixture.read_text())
        body = json.loads(fixture["body"])
        if body.get("model") != MODEL:
            raise ValueError("Fixture must target the exact diagnostic free model")
        body["stream"] = False
        client_headers = {k: v for k, v in fixture["headers"].items()
                          if k.lower() not in ("authorization", "cookie", "host", "content-length")}
        client_path = "/v1/chat/completions"
    keys = credentials(saved)
    pid = subprocess.check_output(["systemctl", "--user", "show", "opencode-cc.service",
                                   "-p", "MainPID", "--value"], text=True).strip()
    base_env = dict(x.split("=", 1) for x in Path(f"/proc/{pid}/environ").read_text().split("\0") if "=" in x)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    args.out.parent.mkdir(parents=True, exist_ok=True)

    def emit(row):
        row.update(time=timestamp(), label=args.label)
        line = json.dumps(row, sort_keys=True)
        with args.out.open("a") as report:
            report.write(line + "\n")
        print(line, flush=True)

    for route in args.routes:
        emit({"event": "egress_before", "route": route, "result": trace(route)})
        with socket.socket() as sock:
            sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            sock.bind(("127.0.0.1", 18789))
        with tempfile.TemporaryDirectory(prefix="cc-zen-proof-") as directory, tempfile.TemporaryFile() as log:
            cfg = dict(saved, require_api_key=False, panel_token="diagnostic-panel", log_requests=True)
            cfg["upstreams"] = [dict(saved["upstreams"][0], api_key=keys[args.credentials[0]], enabled=True)]
            if args.upstream:
                cfg["upstreams"][0]["base_url"] = args.upstream
            config_path = Path(directory) / "config.json"
            config_path.write_text(json.dumps(cfg))
            config_path.chmod(0o600)
            env = base_env.copy()
            for key in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"):
                env.pop(key, None)
            env.update(OPENCODE_CC_LISTEN="127.0.0.1:18789", NO_PROXY="127.0.0.1,localhost,::1",
                       no_proxy="127.0.0.1,localhost,::1", OPENCODE_CC_PANEL_TOKEN="diagnostic-panel",
                       OPENCODE_CC_REQUIRE_API_KEY="false", OPENCODE_CC_LOG_REQUESTS="true")
            if ROUTES[route]:
                env.update(HTTP_PROXY=ROUTES[route], HTTPS_PROXY=ROUTES[route])
            else:
                env.update(NO_PROXY="*", no_proxy="*")
            proc = subprocess.Popen([args.stage, "-data", directory], env=env, stdout=log, stderr=log)
            try:
                ready = False
                for _ in range(40):
                    if proc.poll() is not None:
                        raise RuntimeError("Staging process exited")
                    try:
                        with opener.open("http://127.0.0.1:18789/api/health", timeout=1):
                            ready = True
                            break
                    except Exception:
                        time.sleep(.2)
                if not ready:
                    raise RuntimeError("Staging did not become healthy")
                previous_id = 0
                for name in args.credentials:
                    upstream = dict(cfg["upstreams"][0], api_key=keys[name])
                    update = urllib.request.Request("http://127.0.0.1:18789/api/config", method="PUT",
                        data=json.dumps({"upstreams": [upstream]}).encode(),
                        headers={"Content-Type": "application/json", "Authorization": "Bearer diagnostic-panel"})
                    with opener.open(update, timeout=5) as response:
                        response.read()
                    row = {"event": "inference", "route": route, "credential": identity(name, keys[name]),
                           "model": MODEL, "client_path": client_path,
                           "request_sha256": fingerprint(json.dumps(body, sort_keys=True))}
                    request = urllib.request.Request("http://127.0.0.1:18789" + client_path,
                        data=json.dumps(body).encode(), headers={**client_headers, "Content-Type": "application/json"})
                    started = time.monotonic()
                    try:
                        with opener.open(request, timeout=65) as response:
                            row.update(status=response.status, headers=safe_headers(response.headers),
                                       response=json.loads(response.read(1024 * 1024)))
                    except urllib.error.HTTPError as error:
                        row.update(status=error.code, headers=safe_headers(error.headers),
                                   response=json.loads(error.read(16384)))
                    except Exception as error:
                        row["transport_error"] = str(error)
                    row["seconds"] = round(time.monotonic() - started, 3)
                    for _ in range(25):
                        with sqlite3.connect(Path(directory) / "opencode-cc.db") as db:
                            record = db.execute("SELECT id,status,resp_body,resp_headers FROM requests WHERE id>? ORDER BY id LIMIT 1",
                                                (previous_id,)).fetchone()
                        if record:
                            previous_id = record[0]
                            row["upstream_status"] = record[1]
                            row["upstream_body"] = record[2][:16384]
                            row["upstream_headers"] = safe_headers(json.loads(record[3] or "{}"))
                            break
                        time.sleep(.1)
                    emit(row)
            finally:
                proc.terminate()
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
        emit({"event": "egress_after", "route": route, "result": trace(route)})


if __name__ == "__main__":
    main()
