#!/usr/bin/env python3
"""Real Claude/CPA/relay acceptance, with optional controlled 429 outage.

Requires staged Go binary and running DS gateway. Uses isolated data, dummy
local keys and exact deepseek-v4.1-flash-oc alias. Never alters production CPA,
credentials, dashboard usage or other models. Long outage tests use the SAME
CPA process for outage and recovery. Raw CLI output is bounded and sanitized.
"""

import argparse
import os
import json
import subprocess
import tempfile
import time
import urllib.request
import socket
import signal
import sqlite3
from pathlib import Path
import http.server
import threading
import sys
import shutil


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--fault-seconds", type=float, default=0)
    ap.add_argument("--credential", choices=["saved", "active"], default="saved")
    ap.add_argument("--challenge", default="CPA_DS_ACCEPTANCE_7281")
    ap.add_argument("--gateway", default="http://127.0.0.1:18790")
    ap.add_argument("--config", type=Path, default=Path.home() / "data/config.json")
    ap.add_argument("--stage", default="/tmp/opencode-cc-stage")
    ap.add_argument("--cpa", default=str(Path.home() / ".local/opt/cliproxyapi/cli-proxy-api"))
    ap.add_argument("--claude", default=shutil.which("claude"))
    args = ap.parse_args()
    if not args.claude or not Path(args.claude).is_file():
        ap.error("Claude Code is required; use --claude with its executable path")
    if args.fault_seconds < 0:
        ap.error("--fault-seconds must be nonnegative")
    for executable in (args.stage, args.cpa):
        if not Path(executable).is_file():
            ap.error("Required executable missing: " + executable)

    os.environ["ZEN_USE_CPA"] = "1"
    os.environ["ZEN_FAULT_SECONDS"] = str(args.fault_seconds)
    os.environ["ZEN_STAGE_ROUTE"] = "ds_flash_gateway"
    os.environ["ZEN_CLAUDE_PROMPT"] = (
        "Create proof.txt containing exactly "
        + args.challenge
        + ", then read it back with the file tool and report that exact value. Work only on proof.txt in the current directory. Do not inspect other files."
    )

    fault = {"until": 0.0, "calls": 0}
    saved = json.loads(args.config.read_text())
    if not saved.get("upstreams") or not saved["upstreams"][0].get("api_key"):
        ap.error("Config needs the test credential in upstreams[0].api_key")
    pid = subprocess.run(
        [
            "systemctl",
            "--user",
            "show",
            "opencode-cc.service",
            "-p",
            "MainPID",
            "--value",
        ],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        check=False,
    ).stdout.strip()
    base = dict(os.environ)
    if pid and pid != "0" and Path("/proc/" + pid + "/environ").exists():
        base.update(
            dict(
                x.split("=", 1)
                for x in Path("/proc/" + pid + "/environ").read_text().split("\0")
                if "=" in x
            )
        )
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    report = {
        "event": "real_claude_stage",
        "time": __import__("datetime")
        .datetime.now(__import__("datetime").timezone.utc)
        .isoformat(),
        "model": "deepseek-v4.1-flash-free",
        "route": os.environ.get("ZEN_STAGE_ROUTE", "direct_bun"),
        "headers": "adapter_owned_stable_session",
        "prelude": os.environ.get("ZEN_DS_PRELUDE", "on"),
    }
    for port in (18789, 18319, 18794):
        with socket.socket() as s:
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            s.bind(("127.0.0.1", port))
    with (
        tempfile.TemporaryDirectory(prefix="zen-claude-stage-") as d,
        tempfile.TemporaryFile() as logs,
        tempfile.TemporaryFile() as out,
        tempfile.TemporaryFile() as err,
    ):

        class FaultHandler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                if time.monotonic() < fault["until"]:
                    fault["calls"] += 1
                    data = json.dumps(
                        {
                            "type": "error",
                            "error": {
                                "type": "FreeUsageLimitError",
                                "message": "Controlled acceptance-test outage",
                            },
                        }
                    ).encode()
                    self.send_response(429)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Retry-After", "86400")
                    self.send_header("Content-Length", str(len(data)))
                    self.end_headers()
                    self.wfile.write(data)
                    return
                req = urllib.request.Request(
                    args.gateway + "/v1/chat/completions",
                    data=body,
                    headers={
                        k: v
                        for k, v in self.headers.items()
                        if k.lower() not in ["host", "content-length"]
                    },
                )
                try:
                    r = opener.open(req, timeout=90)
                except urllib.error.HTTPError as e:
                    r = e
                try:
                    self.send_response(r.status)
                    for k, v in r.headers.items():
                        if k.lower() not in [
                            "content-length",
                            "transfer-encoding",
                            "connection",
                        ]:
                            self.send_header(k, v)
                    self.end_headers()
                    while True:
                        part = r.read1(16384)
                        if not part:
                            break
                        self.wfile.write(part)
                        self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass
                finally:
                    r.close()

        faultserver = http.server.ThreadingHTTPServer(
            ("127.0.0.1", 18794), FaultHandler
        )
        threading.Thread(target=faultserver.serve_forever, daemon=True).start()
        cfg = dict(
            saved,
            require_api_key=False,
            log_requests=True,
            panel_token="diagnostic-panel",
        )
        cfg["upstreams"] = [
            dict(
                saved["upstreams"][0],
                api_key=(
                    "opencode://active"
                    if args.credential == "active"
                    else saved["upstreams"][0]["api_key"]
                ),
                base_url=saved["upstreams"][0]["base_url"],
                enabled=True,
            )
        ]
        f = Path(d) / "config.json"
        f.write_text(json.dumps(cfg))
        f.chmod(0o600)
        env = base.copy()
        for k in [
            "HTTP_PROXY",
            "HTTPS_PROXY",
            "ALL_PROXY",
            "http_proxy",
            "https_proxy",
            "all_proxy",
        ]:
            env.pop(k, None)
        env.update(
            OPENCODE_CC_DS_FLASH_FETCH_URL="http://127.0.0.1:18794",
            OPENCODE_CC_LISTEN="127.0.0.1:18789",
            NO_PROXY="*",
            no_proxy="*",
            OPENCODE_CC_REQUIRE_API_KEY="false",
            OPENCODE_CC_PANEL_TOKEN="diagnostic-panel",
            OPENCODE_CC_LOG_REQUESTS="true",
        )
        stage = subprocess.Popen(
            [args.stage, "-data", d], env=env, stdout=logs, stderr=logs
        )
        try:
            for i in range(40):
                try:
                    with opener.open("http://127.0.0.1:18789/api/health", timeout=1):
                        break
                except Exception:
                    time.sleep(0.2)
            cpa = None
            if os.environ.get("ZEN_USE_CPA") == "1":
                import yaml

                cc = {
                    "config-version": 8,
                    "server": {"host": "127.0.0.1", "port": 18319},
                    "access": {"api-keys": ["diagnostic-client"]},
                    "api-keys": {
                        "claude": [
                            {
                                "name": "claude-2",
                                "prefix": "oc",
                                "base-url": "http://127.0.0.1:18789",
                                "proxy-url": "",
                                "rate-limit-hold-seconds": 180,
                                "models": [],
                                "keys": [
                                    {
                                        "api-key": "diagnostic",
                                        "cloak": {"strict-mode": False},
                                        "models": [
                                            {
                                                "name": "deepseek-v4.1-flash-free",
                                                "alias": "deepseek-v4.1-flash-oc",
                                                "thinking": {
                                                    "levels": ["low", "high", "max"]
                                                },
                                            }
                                        ],
                                    }
                                ],
                            }
                        ]
                    },
                }
                cp = Path(d) / "cpa.yaml"
                cp.write_text(yaml.safe_dump(cc))
                cp.chmod(0o600)
                ce = env.copy()
                ce["HOME"] = d + "/cpa-home"
                Path(ce["HOME"]).mkdir()
                cpa = subprocess.Popen(
                    [
                        args.cpa,
                        "-config",
                        str(cp),
                    ],
                    env=ce,
                    cwd=d,
                    stdout=logs,
                    stderr=logs,
                )
                for i in range(40):
                    try:
                        with opener.open(
                            urllib.request.Request(
                                "http://127.0.0.1:18319/v1/models",
                                headers={"Authorization": "Bearer diagnostic-client"},
                            ),
                            timeout=1,
                        ):
                            break
                    except Exception:
                        if cpa.poll() is not None:
                            logs.seek(0)
                            raise RuntimeError(
                                "isolated CPA startup failed: "
                                + logs.read(5000).decode(errors="replace")
                            )
                        time.sleep(0.2)
            cenv = os.environ.copy()
            for k in [
                "HTTP_PROXY",
                "HTTPS_PROXY",
                "ALL_PROXY",
                "http_proxy",
                "https_proxy",
                "all_proxy",
            ]:
                cenv.pop(k, None)
            for k in [
                "HOME",
                "XDG_CONFIG_HOME",
                "XDG_DATA_HOME",
                "XDG_STATE_HOME",
                "XDG_CACHE_HOME",
            ]:
                cenv[k] = d + "/" + k
            cenv.update(
                ANTHROPIC_BASE_URL="http://127.0.0.1:18789",
                ANTHROPIC_AUTH_TOKEN="diagnostic",
                ANTHROPIC_API_KEY="",
                ANTHROPIC_MODEL="deepseek-v4.1-flash-free",
                ANTHROPIC_CUSTOM_MODEL_OPTION="deepseek-v4.1-flash-free",
                CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1",
                NO_PROXY="*",
                no_proxy="*",
                CLAUDE_CODE_MAX_CONTEXT_TOKENS="260000",
            )
            cenv.pop("ANTHROPIC_CUSTOM_HEADERS", None)
            if cpa:
                cenv.update(
                    ANTHROPIC_BASE_URL="http://127.0.0.1:18319",
                    ANTHROPIC_AUTH_TOKEN="diagnostic-client",
                    ANTHROPIC_MODEL="deepseek-v4.1-flash-oc",
                    ANTHROPIC_CUSTOM_MODEL_OPTION="deepseek-v4.1-flash-oc",
                )
                report["chain"] = (
                    "Claude Code -> CPA -> opencode-cc -> DS gateway -> Zen"
                )
                report["model"] = "deepseek-v4.1-flash-oc"
            workspace = Path(d) / "workspace"
            workspace.mkdir()
            report["phases"] = []

            def run_phase(label):
                # Recovery must create a fresh artifact, not inherit the prior phase's file.
                (workspace / "proof.txt").unlink(missing_ok=True)
                out.seek(0)
                out.truncate()
                err.seek(0)
                err.truncate()
                started = time.monotonic()
                cli = subprocess.Popen(
                    [
                        args.claude,
                        "-p",
                        os.environ.get(
                            "ZEN_CLAUDE_PROMPT",
                            "Reply exactly CLAUDE_STAGE_4682. Do not use tools or inspect files.",
                        ) + " The absolute proof path is " + str(workspace / "proof.txt") + ". Use the supplied Read and Write tools on that path; do not simulate tool results.",
                        "--model",
                        (
                            "deepseek-v4.1-flash-oc"
                            if cpa
                            else "deepseek-v4.1-flash-free"
                        ),
                        "--output-format",
                        "json",
                        "--verbose",
                        "--max-turns",
                        "8",
                        "--permission-mode",
                        "acceptEdits",
                        "--tools",
                        "Read,Write",
                        "--allowedTools",
                        "Read",
                        "Write",
                        "--no-session-persistence",
                        "--setting-sources",
                        "",
                    ],
                    env=cenv,
                    cwd=workspace,
                    stdout=out,
                    stderr=err,
                    start_new_session=True,
                )
                try:
                    report["exit"] = cli.wait(timeout=240)
                except subprocess.TimeoutExpired:
                    os.killpg(cli.pid, signal.SIGTERM)
                    cli.wait(timeout=5)
                    report["exit"] = cli.returncode
                    report["deadline"] = True
                out.seek(0)
                err.seek(0)
                report["stdout"] = out.read(131072).decode(errors="replace")
                report["stderr"] = err.read(4000).decode(errors="replace")
                tool_calls, tool_results = {}, set()
                try:
                    transcript = json.loads(report["stdout"])
                    for message in transcript if isinstance(transcript, list) else []:
                        for part in message.get("message", {}).get("content", []):
                            if not isinstance(part, dict):
                                continue
                            if part.get("type") == "tool_use" and part.get("input", {}).get("file_path") == str(workspace / "proof.txt"):
                                tool_calls[part["id"]] = part.get("name")
                            if part.get("type") == "tool_result" and not part.get("is_error"):
                                tool_results.add(part.get("tool_use_id"))
                except (ValueError, TypeError):
                    pass
                report["tool_proof"] = sorted({tool_calls[k] for k in tool_results if k in tool_calls})
                proof = workspace / "proof.txt"
                report["file_proof"] = {
                    "exists": proof.exists(),
                    "text": proof.read_text()[:500] if proof.exists() else None,
                }
                report["phases"].append(
                    {
                        k: report.get(k)
                        for k in ["exit", "stdout", "stderr", "file_proof", "tool_proof", "deadline"]
                    }
                )
                report["phases"][-1].update(
                    label=label, seconds=round(time.monotonic() - started, 3)
                )
                print(
                    json.dumps(
                        {
                            "phase": label,
                            "exit": report["exit"],
                            "seconds": report["phases"][-1]["seconds"],
                            "file": report["file_proof"],
                        }
                    ),
                    file=sys.stderr,
                    flush=True,
                )

            duration = float(os.environ.get("ZEN_FAULT_SECONDS", "8"))
            report["fault_seconds"] = duration
            fault["until"] = time.monotonic() + duration
            run_phase("during_outage")
            if duration > 180:
                time.sleep(max(0, fault["until"] - time.monotonic() + 1))
                run_phase("after_recovery_same_cpa_process")
            report["injected_rejections"] = fault["calls"]
            with sqlite3.connect(Path(d) / "opencode-cc.db") as db:
                report["upstream_records"] = [
                    {"status": x[0], "body": x[1][:600]}
                    for x in db.execute(
                        "SELECT status,resp_body FROM requests ORDER BY id DESC LIMIT 3"
                    )
                ]
        finally:
            faultserver.shutdown()
            faultserver.server_close()
            if "cpa" in locals() and cpa:
                cpa.terminate()
                cpa.wait(timeout=5)
            stage.terminate()
            stage.wait(timeout=5)
    s = json.dumps(report)
    for k in [saved["upstreams"][0]["api_key"]]:
        s = s.replace(k, "<redacted>")
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("a") as f:
        f.write(s + "\n")
    print(s)

    if not report.get("phases") or any(
        phase.get("exit") != 0 or phase.get("file_proof", {}).get("text") != args.challenge
        or not {"Read", "Write"}.issubset(phase.get("tool_proof", []))
        for phase in report["phases"]
    ) or (args.fault_seconds > 0 and not report.get("injected_rejections")):
        raise SystemExit("Exact-model file-tool acceptance failed; inspect report")


if __name__ == "__main__":
    main()
