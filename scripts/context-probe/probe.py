#!/usr/bin/env python3
"""Needle-recall probe through the opencode-cc relay (Anthropic /v1/messages, streamed).

Builds a deterministic document of about --chars characters of filler log lines,
hides five random "ACCESS CODE" lines at fixed depths (1%, 25%, 50%, 75%, 99%),
asks for them back, and writes one JSON record per probe under results/.
Stdlib only. Bypasses inherited proxy env so 127.0.0.1 is hit directly.
"""
import argparse
import json
import os
import random
import string
import time
import urllib.error
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
WORDS = (
    "system record queue cache region service latency buffer token window "
    "session request response signal storage index marker summary archive "
    "frame vector agent planner worker branch commit review deploy rollback "
    "metric alert sample trace span packet socket route gateway policy quota "
    "budget limit reset stream chunk prefix suffix header footer body table "
    "column row schema field value key lock timer clock epoch north south "
    "east west harbor river forest meadow canyon desert island copper silver "
    "iron glass paper cable ladder bridge tunnel station engine"
).split()
DEPTHS = [0.01, 0.25, 0.50, 0.75, 0.99]
QUESTION = (
    "\n\nThe document above contains five lines that begin with 'ACCESS CODE'. "
    "Reply with exactly those five codes, one per line, in the order they appear "
    "in the document. Output nothing else."
)
ALPHABET = string.ascii_uppercase + string.digits
# Bypass HTTP(S)_PROXY from the environment: the relay is on 127.0.0.1.
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def build(seed: int, target_chars: int):
    rng = random.Random(seed)
    codes = ["-".join("".join(rng.choice(ALPHABET) for _ in range(4)) for _ in range(3))
             for _ in DEPTHS]
    lines, total, n = [], 0, 0
    while total < target_chars:
        n += 1
        note = " ".join(rng.choice(WORDS) for _ in range(rng.randint(8, 14)))
        line = f"svc=svc-{rng.randint(1, 40):02d} op={rng.choice(WORDS)} seq={n:07d} note={note}"
        lines.append(line)
        total += len(line) + 1
    # Insert from the deepest position first so earlier indexes stay valid.
    for i in reversed(range(len(DEPTHS))):
        pos = int(DEPTHS[i] * len(lines))
        lines.insert(pos, f"ACCESS CODE {i + 1}: {codes[i]}")
    return "\n".join(lines) + QUESTION, codes


def stream_events(resp):
    event, data = None, []
    for raw in resp:
        line = raw.decode("utf-8", "replace").rstrip("\r\n")
        if line == "":
            if data:
                yield event, "\n".join(data)
            event, data = None, []
        elif line.startswith("event:"):
            event = line[6:].strip()
        elif line.startswith("data:"):
            data.append(line[5:].lstrip())


def run(base, model, effort, max_tokens, text, timeout, headers=None):
    body = {
        "model": model,
        "max_tokens": max_tokens,
        "stream": True,
        "output_config": {"effort": effort},
        "messages": [{"role": "user", "content": [{"type": "text", "text": text}]}],
    }
    hdrs = {"content-type": "application/json", "anthropic-version": "2023-06-01"}
    hdrs.update(headers or {})
    req = urllib.request.Request(
        base + "/v1/messages",
        data=json.dumps(body).encode(),
        headers=hdrs,
        method="POST",
    )
    rec = {"chars": len(text), "model_requested": model, "effort": effort, "max_tokens": max_tokens}
    t0 = time.monotonic()
    try:
        resp = OPENER.open(req, timeout=timeout)
    except urllib.error.HTTPError as e:
        rec.update(status=e.code, t_headers_s=round(time.monotonic() - t0, 2),
                   error_body=e.read()[:800].decode("utf-8", "replace"))
        return rec
    except Exception as e:  # network-level failure, not an HTTP verdict
        rec.update(status=None, exception=f"{type(e).__name__}: {e}"[:400],
                   t_total_s=round(time.monotonic() - t0, 2))
        return rec
    t_headers = time.monotonic() - t0
    rec.update(status=resp.status, t_headers_s=round(t_headers, 2))
    usage = {"input_tokens": 0, "cache_read_input_tokens": 0,
             "cache_creation_input_tokens": 0, "output_tokens": 0}
    answer, thinking_chars, block_types, errors = [], 0, [], []
    t_first_text = None
    stop_reason = None
    for event, payload in stream_events(resp):
        if event == "error":
            errors.append(payload[:400])
            continue
        try:
            obj = json.loads(payload)
        except json.JSONDecodeError:
            continue
        if event == "message_start":
            msg = obj.get("message", {})
            rec["model_returned"] = msg.get("model")
            u = msg.get("usage", {})
        elif event == "message_delta":
            stop_reason = obj.get("delta", {}).get("stop_reason") or stop_reason
            u = obj.get("usage", {})
        elif event == "content_block_start":
            block_types.append(obj.get("content_block", {}).get("type"))
            u = {}
        elif event == "content_block_delta":
            d = obj.get("delta", {})
            if d.get("type") == "text_delta":
                if t_first_text is None:
                    t_first_text = round(time.monotonic() - t0, 2)
                answer.append(d.get("text", ""))
            elif d.get("type") == "thinking_delta":
                thinking_chars += len(d.get("thinking", ""))
            u = {}
        else:
            u = {}
        for k in usage:
            if u.get(k):
                usage[k] = u[k]
    text_out = "".join(answer)
    positions = []
    rec["codes_found"] = []
    for code in CODES_CACHE.get("codes", []):
        pos = text_out.find(code)
        positions.append(pos)
        if pos >= 0:
            rec["codes_found"].append(code)
    rec.update(
        usage=usage,
        stop_reason=stop_reason,
        block_types=block_types,
        thinking_chars=thinking_chars,
        t_first_text_s=t_first_text,
        t_total_s=round(time.monotonic() - t0, 2),
        answer=text_out[:600],
        stream_errors=errors,
        recall=f"{len(rec['codes_found'])}/{len(CODES_CACHE.get('codes', []))}",
        ordered=all(p >= 0 for p in positions) and positions == sorted(positions),
    )
    return rec


CODES_CACHE = {}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--id", required=True)
    ap.add_argument("--chars", type=int, required=True)
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--base", default="http://127.0.0.1:18789")
    ap.add_argument("--model", default="deepseek-v4.1-flash-free")
    ap.add_argument("--effort", default="low")
    ap.add_argument("--max-tokens", type=int, default=4096)
    ap.add_argument("--timeout", type=int, default=1500)
    ap.add_argument("--cc-session", default="", help="send x-claude-code-session-id (lane selector)")
    args = ap.parse_args()

    text, codes = build(args.seed, args.chars)
    CODES_CACHE["codes"] = codes
    extra = {"x-claude-code-session-id": args.cc_session} if args.cc_session else None
    rec = run(args.base, args.model, args.effort, args.max_tokens, text, args.timeout, extra)
    rec["cc_session"] = args.cc_session or None
    rec.update(id=args.id, seed=args.seed, codes_expected=codes,
               depths=DEPTHS, wall_clock_utc=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()))
    out_dir = Path(os.environ.get("CTXPROBE_OUT", Path.cwd() / "ctxprobe-results"))
    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / f"{args.id}.json").write_text(json.dumps(rec, indent=2))
    u = rec.get("usage", {})
    print(json.dumps({
        "id": args.id, "status": rec.get("status"), "chars": rec["chars"],
        "input_tokens": u.get("input_tokens"),
        "total_input_tokens": sum(u.get(k, 0) for k in ("input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens")),
        "cache_read": u.get("cache_read_input_tokens"),
        "output_tokens": u.get("output_tokens"), "stop": rec.get("stop_reason"),
        "t_headers_s": rec.get("t_headers_s"), "t_total_s": rec.get("t_total_s"),
        "model_returned": rec.get("model_returned"), "recall": rec.get("recall"),
        "ordered": rec.get("ordered"), "thinking_chars": rec.get("thinking_chars"),
        "error": (rec.get("error_body") or rec.get("exception") or (rec.get("stream_errors") or [""])[0])[:300],
    }, indent=2))


if __name__ == "__main__":
    main()
