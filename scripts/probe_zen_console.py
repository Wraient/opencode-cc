#!/usr/bin/env python3
"""Read authenticated Console request logs without exporting account data or keys.

Endpoints and response schema are published in the Console's own browser bundle.
This is read-only. Raw results stay private; repository evidence contains only
request metadata, fingerprints, statuses and attempts, never prompts or tokens.
"""
import argparse
import datetime
import json
import os
from pathlib import Path
import sqlite3
import subprocess

from probe_zen_free_access import ROOT, fingerprint, timestamp


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--credentials", nargs="+", choices=["saved", "active"], default=["saved", "active"])
    ap.add_argument("--limit", type=int, default=100)
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()
    if not 1 <= args.limit <= 100:
        ap.error("limit must be between 1 and 100")
    saved = json.loads((ROOT / "data/config.json").read_text())["upstreams"][0]["api_key"]
    with sqlite3.connect(f"file:{ROOT}/.local/share/opencode/opencode.db?mode=ro", uri=True) as db:
        active = json.loads(db.execute(
            "SELECT value FROM credential WHERE integration_id='opencode' AND active=1").fetchone()[0])
    credentials = {"saved": (saved, None),
                   "active": (active["access"], active.get("metadata", {}).get("orgID"))}
    private = ROOT / ".cache/opencode-cc/zen-console-private"
    private.mkdir(mode=0o700, parents=True, exist_ok=True)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    for name in args.credentials:
        key, org = credentials[name]
        if not key or key.startswith("opencode://"):
            raise ValueError("Expected an actual credential for the read-only Console call")
        destination = private / (name + "-request-logs.json")
        fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        os.fchmod(fd, 0o600)
        os.close(fd)
        config = "header = " + json.dumps("Authorization: Bearer " + key) + "\n"
        if org:
            config += "header = " + json.dumps("x-org-id: " + org) + "\n"
        call = subprocess.run([
            "curl", "-sS", "--max-time", "25", "--proxy", "http://127.0.0.1:18889",
            "--config", "-", "https://opencode.ai/console/api/request-logs?limit=" + str(args.limit),
            "-o", str(destination), "-w", "%{http_code}"],
            input=config, capture_output=True, text=True, timeout=30)
        row = {"event": "console_request_logs", "time": timestamp(), "credential": name,
               "credential_fingerprint": fingerprint(key), "http_status": call.stdout,
               "curl_exit": call.returncode, "items": []}
        if call.returncode == 0 and call.stdout == "200":
            data = json.loads(destination.read_text())
            for item in data["items"]:
                if item.get("requestedModel") != "deepseek-v4.1-flash-free":
                    continue
                headers = item.get("requestHeaders") or {}
                public_headers = {k: v for k, v in headers.items() if k.lower() in (
                    "accept", "content-type", "content-length", "user-agent", "x-opencode-client")}
                safe = {k: item.get(k) for k in (
                    "outcome", "category", "protocol", "product", "path", "app", "statusCode",
                    "errorCode", "errorMessage", "requestedModel", "attemptCount")}
                safe.update(time=datetime.datetime.fromtimestamp(
                    item["startedAt"] / 1000, datetime.timezone.utc).isoformat(),
                    request_id_fingerprint=fingerprint(item["requestID"]),
                    session_fingerprint=fingerprint(item.get("sessionID") or ""),
                    request_headers=public_headers,
                    attempts=[{k: attempt.get(k) for k in (
                        "provider", "model", "statusCode", "durationMs", "errorCode", "errorMessage")}
                        for attempt in item.get("attempts", [])])
                row["items"].append(safe)
        with args.out.open("a") as report:
            report.write(json.dumps(row, sort_keys=True) + "\n")
        print(json.dumps({"credential": name, "http_status": row["http_status"],
                          "free_model_rows": len(row["items"]),
                          "successes": sum(i["statusCode"] == 200 for i in row["items"]),
                          "pre_attempt_rejections": sum(i["statusCode"] == 429 and i["attemptCount"] == 0
                                                        for i in row["items"])}), flush=True)


if __name__ == "__main__":
    main()
