#!/usr/bin/env python3
"""Run the unmodified native CLI on the requested model in isolated state.

No provider URL, toolset, TLS transport, or retry policy overrides. Stop after
the bounded observation window and report persisted retry errors, rather than
calling a sleeping retry a transport timeout. Never copies OAuth refresh data.
"""
import argparse
import json
import os
from pathlib import Path
import signal
import shutil
import sqlite3
import subprocess
import tempfile
import time

from probe_zen_free_access import ROOT, ROUTES, MODEL, credentials, identity, fingerprint, timestamp, trace


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--credential", choices=["saved", "active", "none"], default="saved")
    ap.add_argument("--route", choices=ROUTES, default="phone")
    ap.add_argument("--seconds", type=int, default=25)
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--observe", action="store_true", help="Read-only HTTP hooks; preserve native transport and endpoint")
    ap.add_argument("--title-mode", choices=["auto", "explicit"], default="auto")
    ap.add_argument("--prompt", default="Reply exactly OK. Do not use tools or inspect files.")
    ap.add_argument("--replay-native-fetch", action="store_true", help="One controlled replay through the CLI's own fetch runtime")
    args = ap.parse_args()
    saved = json.loads((ROOT / "data/config.json").read_text())
    key = "" if args.credential == "none" else credentials(saved)[args.credential]
    ident = {"name": "none", "kind": "anonymous"} if not key else identity(args.credential, key)
    if args.credential == "active":
        with sqlite3.connect(f"file:{ROOT}/.local/share/opencode/opencode.db?mode=ro", uri=True) as db:
            data = json.loads(db.execute("SELECT value FROM credential WHERE integration_id='opencode' AND active=1").fetchone()[0])
        key = data.get("access") or data.get("key")
    row = {"event": "native_cli", "time": timestamp(), "route": args.route, "observe": args.observe,
           "title_mode": args.title_mode,
           "prompt_sha256": fingerprint(args.prompt),
           "credential": ident, "model": MODEL, "egress_before": trace(args.route)}
    with tempfile.TemporaryDirectory(prefix="cc-native-proof-") as directory:
        env = os.environ.copy()
        for name in ("HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"):
            env[name] = directory + "/" + name
        for name in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "OPENCODE_API_KEY"):
            env.pop(name, None)
        env.update(PWD=directory, OPENCODE_CONFIG_PROJECT_DISABLE="1", OPENCODE_CONFIG_CONTENT='{"snapshot":false}',
                   NO_PROXY="127.0.0.1,localhost,::1", no_proxy="127.0.0.1,localhost,::1")
        if args.observe:
            private = ROOT / ".cache/opencode-cc/zen-proof-fixture"
            private.mkdir(mode=0o700, parents=True, exist_ok=True)
            plugin = Path(directory) / "observer"
            plugin.mkdir()
            shutil.copyfile(Path(__file__).resolve().with_name("observe_zen_native.mjs"), plugin / "index.mjs")
            (plugin / "package.json").write_text(json.dumps({"name": "zen-access-readonly-observer", "version": "0.0.0",
                                                             "type": "module", "main": "index.mjs"}))
            env.update(ZEN_PROOF_CAPTURE=str(private / ("actual-native-" + args.credential + ".json")),
                       ZEN_PROOF_OBSERVATIONS=directory + "/observations.jsonl",
                       OPENCODE_CONFIG_CONTENT=json.dumps({"snapshot": False,
                           "plugins": [{"package": str(plugin),
                                        "options": {"capture": str(private / ("actual-native-" + args.credential + ".json")),
                                                    "observations": directory + "/observations.jsonl",
                                                    "replay": args.replay_native_fetch}}]}))
        if key:
            env["OPENCODE_API_KEY"] = key
        if ROUTES[args.route]:
            env.update(HTTP_PROXY=ROUTES[args.route], HTTPS_PROXY=ROUTES[args.route])
        else:
            env.update(NO_PROXY="*", no_proxy="*")
        with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
            started = time.monotonic()
            command = [str(ROOT / ".opencode/bin/opencode"), "run", "--standalone", "--format", "json", "--print-logs"]
            if args.title_mode == "explicit":
                command += ["--title", "DeepSeek access probe"]
            command += ["-m", "opencode/" + MODEL, args.prompt]
            proc = subprocess.Popen(command,
                cwd=directory, env=env, stdout=stdout, stderr=stderr, start_new_session=True)
            try:
                proc.wait(timeout=args.seconds)
                row["completed"] = True
            except subprocess.TimeoutExpired:
                row["completed"] = False
                os.killpg(proc.pid, signal.SIGTERM)
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait()
            row.update(exit=proc.returncode, seconds=round(time.monotonic() - started, 3))
            stdout.seek(0)
            row["events"] = []
            for line in stdout:
                if len(row["events"]) >= 25:
                    break
                try:
                    event = json.loads(line)
                    summary = {"type": event.get("type")}
                    if event.get("error"):
                        summary["error"] = event["error"]
                    if event.get("part", {}).get("text"):
                        summary["text"] = event["part"]["text"][:1000]
                    row["events"].append(summary)
                except ValueError:
                    pass
            stderr.seek(0)
            row["error_log_evidence"] = []
            for raw in stderr:
                line = raw.decode(errors="replace")
                if any(s in line for s in ("FreeUsageLimitError", "Free usage limit", "provider.rate-limit", "retry scheduled", "FreeTierError", "zen-access", "observer", "Failed to load plugin")) or ("plugin" in line.lower() and any(s in line for s in ("WARN", "ERROR"))):
                    row["error_log_evidence"].append(line[:2500])
                    if len(row["error_log_evidence"]) == 8:
                        break
        row["database_error_evidence"] = []
        observations = Path(directory) / "observations.jsonl"
        if observations.exists():
            row["http_observations"] = [json.loads(line) for line in observations.read_text().splitlines()[:8]]
        for file in Path(directory).rglob("opencode.db"):
            with sqlite3.connect(f"file:{file}?mode=ro", uri=True) as db:
                row["database_tables"] = [x[0] for x in db.execute("SELECT name FROM sqlite_master WHERE type='table'")]
                tables = db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name LIKE '%event%'").fetchall()
                for (table,) in tables:
                    columns = db.execute('PRAGMA table_info("' + table.replace('"', '""') + '")').fetchall()
                    for column in columns:
                        if column[2].upper() not in ("TEXT", "JSON", "BLOB"):
                            continue
                        quoted_table = '"' + table.replace('"', '""') + '"'
                        quoted_column = '"' + column[1].replace('"', '""') + '"'
                        query = (f"SELECT substr(CAST({quoted_column} AS TEXT),1,3000) FROM {quoted_table} "
                                 f"WHERE CAST({quoted_column} AS TEXT) LIKE '%Free usage limit%' LIMIT 2")
                        for (value,) in db.execute(query):
                            row["database_error_evidence"].append({"table": table, "column": column[1], "value": value})
    row["egress_after"] = trace(args.route)
    line = json.dumps(row, sort_keys=True)
    if key:
        line = line.replace(key, "<redacted>")
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("a") as out:
        out.write(line + "\n")
    print(line, flush=True)


if __name__ == "__main__":
    main()
