#!/usr/bin/env python3
"""Cached-prefix probe: re-send an already-accepted document with extra filler appended.

The first --prefix-chars of the prompt are byte-identical to probe p2 (seed 31,
864000 chars), so the upstream should serve them from its prompt cache. The extra
filler pushes the TOTAL prompt past the size that p3 was rejected at, while the
uncached portion stays near --extra-chars. Accepted => the limit does not count
cached tokens. Rejected => the limit is on total prompt size.
"""
import argparse
import json
import os
import random
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import probe as P  # noqa: E402  (reuses the document builder and SSE runner)


def extra_filler(seed: int, target_chars: int) -> str:
    rng = random.Random(seed)
    lines, total, n = [], 0, 100000
    while total < target_chars:
        n += 1
        note = " ".join(rng.choice(P.WORDS) for _ in range(rng.randint(8, 14)))
        line = f"svc=svc-{rng.randint(1, 40):02d} op={rng.choice(P.WORDS)} seq={n:07d} note={note}"
        lines.append(line)
        total += len(line) + 1
    return "\n".join(lines)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--id", required=True)
    ap.add_argument("--prefix-seed", type=int, default=31)
    ap.add_argument("--prefix-chars", type=int, default=864000)
    ap.add_argument("--extra-chars", type=int, required=True)
    ap.add_argument("--extra-seed", type=int, default=99)
    ap.add_argument("--base", default="http://127.0.0.1:18789")
    ap.add_argument("--model", default="deepseek-v4.1-flash-free")
    ap.add_argument("--effort", default="low")
    ap.add_argument("--max-tokens", type=int, default=4096)
    ap.add_argument("--timeout", type=int, default=1500)
    args = ap.parse_args()

    text, codes = P.build(args.prefix_seed, args.prefix_chars)
    doc = text[: -len(P.QUESTION)]
    prompt = doc + "\n" + extra_filler(args.extra_seed, args.extra_chars) + P.QUESTION
    P.CODES_CACHE["codes"] = codes
    rec = P.run(args.base, args.model, args.effort, args.max_tokens, prompt, args.timeout)
    rec.update(id=args.id, prefix_seed=args.prefix_seed, prefix_chars=args.prefix_chars,
               extra_chars=args.extra_chars, codes_expected=codes, depths=P.DEPTHS,
               wall_clock_utc=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()))
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
        "recall": rec.get("recall"), "ordered": rec.get("ordered"),
        "error": (rec.get("error_body") or rec.get("exception") or (rec.get("stream_errors") or [""])[0])[:300],
    }, indent=2))


if __name__ == "__main__":
    main()
