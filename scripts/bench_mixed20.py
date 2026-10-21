#!/usr/bin/env python3
"""Mixed long/short TTFT + tok/s benchmark against opencode-cc (Anthropic path).

Runs N_SHORT short requests + N_LONG long requests (interleaved, sequential),
streaming, and reports per-set and combined TTFT / decode tok/s / e2e tok/s.

- TTFT        : request send -> first content_block_delta of any kind (text OR thinking)
- TTFT_text   : request send -> first *text* delta (visual answer start)
- tok/s       : output_tokens / (last delta - first delta)   [decode-only]
- tok/s e2e   : output_tokens / total wall time
- out tokens  : usage.output_tokens from message_delta (includes thinking tokens)

Usage:
  python3 scripts/bench_mixed20.py --base http://127.0.0.1:8787 --key local \
      --model muse-spark-1.3-contributor-free --n-short 10 --n-long 10
"""
import argparse
import json
import statistics
import time

import requests

FILLER = (
    "The quick brown fox jumps over the lazy dog while the streaming proxy "
    "measures latency distributions across many concurrent turns and "
    "aggregates token counts for each response. "
)

SHORT_PROMPT = (
    "In two sentences, name the main trade-off between latency and throughput "
    "in a streaming LLM proxy. Be direct."
)

LONG_TASK = (
    "\n\nTask: write a detailed essay explaining how a fixed-window rate limiter "
    "with a token bucket works, why the refill rate matters, how burst capacity "
    "interacts with sustained load, and what happens under overload. "
    "Use clear flowing prose, no bullet lists, end with a short conclusion."
)


def build_long_prompt(words=1200):
    w = FILLER.split()
    ctx = " ".join((w * (words // len(w) + 2))[:words])
    return (
        "You are a helpful assistant. Answer precisely.\n\n"
        "Background context (partly irrelevant):\n" + ctx + LONG_TASK
    )


def run_once(base, key, model, prompt, max_tokens, think_budget, path="chat"):
    if path == "chat":
        body = {
            "model": model,
            "max_tokens": max_tokens,
            "stream": True,
            "stream_options": {"include_usage": True},
            "messages": [{"role": "user", "content": prompt}],
        }
        url = f"{base}/v1/chat/completions"
        headers = {"Authorization": f"Bearer {key}",
                   "content-type": "application/json"}
    else:
        body = {
            "model": model,
            "max_tokens": max_tokens,
            "stream": True,
            "messages": [{"role": "user", "content": prompt}],
        }
        if think_budget:
            body["thinking"] = {"type": "enabled", "budget_tokens": think_budget}
        url = f"{base}/v1/messages"
        headers = {"x-api-key": key, "anthropic-version": "2023-06-01",
                   "content-type": "application/json"}
    t0 = time.perf_counter()
    first_any = None
    first_text = None
    last_delta = None
    usage = {}
    n_delta = 0
    text = []
    status = None
    err = None
    try:
        with requests.post(url, headers=headers, json=body,
                           stream=True, timeout=300) as r:
            status = r.status_code
            if r.status_code != 200:
                err = r.read()[:300].decode("utf-8", "replace")
            else:
                for raw in r.iter_lines():
                    if not raw:
                        continue
                    line = raw.decode("utf-8", "replace")
                    if not line.startswith("data:"):
                        continue
                    data = line[5:].strip()
                    if data == "[DONE]":
                        break
                    try:
                        obj = json.loads(data)
                    except json.JSONDecodeError:
                        continue
                    typ = obj.get("type")
                    if path == "chat":
                        u = obj.get("usage") or {}
                        if u.get("completion_tokens") is not None:
                            usage["output_tokens"] = u["completion_tokens"]
                            usage["input_tokens"] = u.get("prompt_tokens", 0)
                            usage["cached_tokens"] = (u.get("prompt_tokens_details") or {}).get("cached_tokens", 0)
                        for ch in obj.get("choices") or []:
                            d = ch.get("delta") or {}
                            piece = d.get("content") or d.get("reasoning_content") or d.get("reasoning")
                            if piece:
                                now = time.perf_counter()
                                n_delta += 1
                                if first_any is None:
                                    first_any = now
                                if d.get("content") and first_text is None:
                                    first_text = now
                                last_delta = now
                                if d.get("content"):
                                    text.append(d["content"])
                        continue
                    if typ == "message_start":
                        usage.update(obj.get("message", {}).get("usage") or {})
                    elif typ == "content_block_delta":
                        d = obj.get("delta") or {}
                        piece = d.get("text") or d.get("thinking")
                        if piece:
                            now = time.perf_counter()
                            n_delta += 1
                            if first_any is None:
                                first_any = now
                            if d.get("type") == "text_delta" and first_text is None:
                                first_text = now
                            last_delta = now
                            if d.get("type") == "text_delta":
                                text.append(piece)
                    elif typ == "message_delta":
                        u = obj.get("usage") or {}
                        if u.get("output_tokens"):
                            usage["output_tokens"] = u["output_tokens"]
                if r.status_code == 200:
                    r.close()
    except Exception as e:  # noqa: BLE001
        err = f"{type(e).__name__}: {e}"
    t_end = time.perf_counter()
    out_tok = usage.get("output_tokens") or 0
    in_tok = usage.get("input_tokens") or 0
    win = (last_delta - first_any) if (first_any and last_delta) else 0.0
    return {
        "status": status,
        "error": err,
        "ttft": (first_any - t0) if first_any else None,
        "ttft_text": (first_text - t0) if first_text else None,
        "total": t_end - t0,
        "in_tok": in_tok,
        "out_tok": out_tok,
        "cached_tokens": usage.get("cached_tokens") or 0,
        "deltas": n_delta,
        "tok_s": (out_tok / win) if out_tok and win > 1e-6 else None,
        "tok_s_e2e": (out_tok / (t_end - t0)) if out_tok else None,
        "chars": sum(len(x) for x in text),
    }


def fmt(v, nd=2):
    return "n/a" if v is None else f"{v:.{nd}f}"


def stat(rows, key, nd=2):
    vals = [r[key] for r in rows if r.get(key) is not None]
    if not vals:
        return "n/a"
    return (f"min={min(vals):.{nd}f} p50={statistics.median(vals):.{nd}f} "
            f"mean={statistics.mean(vals):.{nd}f} max={max(vals):.{nd}f}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:8787")
    ap.add_argument("--key", default="local")
    ap.add_argument("--model", default="muse-spark-1.3-contributor-free")
    ap.add_argument("--n-short", type=int, default=10)
    ap.add_argument("--n-long", type=int, default=10)
    ap.add_argument("--long-words", type=int, default=1200)
    ap.add_argument("--short-max", type=int, default=400)
    ap.add_argument("--long-max", type=int, default=2000)
    ap.add_argument("--long-think", type=int, default=2048,
                    help="thinking budget for long reqs (0 = disabled)")
    ap.add_argument("--path", choices=["chat", "messages"], default="chat")
    ap.add_argument("--jsonl", default="/tmp/opencode/bench_mixed20.jsonl")
    args = ap.parse_args()

    long_prompt = build_long_prompt(args.long_words)
    plan = []
    for i in range(max(args.n_short, args.n_long)):
        if i < args.n_short:
            plan.append(("short", SHORT_PROMPT, args.short_max, 0))
        if i < args.n_long:
            plan.append(("long", long_prompt, args.long_max, args.long_think))

    ep = "/v1/chat/completions" if args.path == "chat" else "/v1/messages"
    print(f"endpoint : {args.base}{ep}")
    print(f"model    : {args.model}")
    print(f"requests : {len(plan)} total ({args.n_short} short + {args.n_long} long), "
          f"interleaved, sequential")
    print(f"long     : ~{args.long_words} ctx words, max_tokens={args.long_max}, "
          f"thinking={args.long_think or 'disabled'}")
    print(f"short    : max_tokens={args.short_max}, thinking=disabled\n")

    rows = []
    with open(args.jsonl, "w") as fh:
        for i, (kind, prompt, mx, tb) in enumerate(plan, 1):
            r = run_once(args.base, args.key, args.model, prompt, mx, tb, args.path)
            r["kind"] = kind
            r["i"] = i
            rows.append(r)
            fh.write(json.dumps(r) + "\n")
            fh.flush()
            print(f"{i:>2}/{len(plan)} {kind:<5} status={r['status']} "
                  f"ttft={fmt(r['ttft'])}s ttft_txt={fmt(r['ttft_text'])}s "
                  f"tok/s={fmt(r['tok_s'],1)} e2e={fmt(r['tok_s_e2e'],1)} "
                  f"total={fmt(r['total'])}s in={r['in_tok']} out={r['out_tok']} "
                  f"cached={r.get('cached_tokens', 0)} deltas={r['deltas']}"
                  + (f" ERR={r['error'][:80]}" if r["error"] else ""))

    ok = [r for r in rows if r["status"] == 200 and r["ttft"] is not None]

    def block(title, subset):
        print(f"\n== {title} ({len(subset)} ok of {len(subset) if title.startswith('ALL') else ''}) ==")
        print(f"TTFT      : {stat(subset, 'ttft')} s")
        print(f"TTFT text : {stat(subset, 'ttft_text')} s")
        print(f"tok/s     : {stat(subset, 'tok_s', 1)}   (decode-only)")
        print(f"tok/s e2e : {stat(subset, 'tok_s_e2e', 1)}")
        print(f"total     : {stat(subset, 'total')} s")
        print(f"in tokens : {stat(subset, 'in_tok', 0)}")
        print(f"out tokens: {stat(subset, 'out_tok', 0)}")
        print(f"chars     : {stat(subset, 'chars', 0)}")

    block("ALL", ok)
    block("SHORT", [r for r in ok if r["kind"] == "short"])
    block("LONG", [r for r in ok if r["kind"] == "long"])
    bad = [r for r in rows if r not in ok]
    print(f"\nsummary: {len(ok)}/{len(rows)} ok"
          + (f", {len(bad)} failed: " + "; ".join(f"{r['kind']} {r['status']} {r['error'][:60]}" for r in bad) if bad else ""))
    print(f"raw jsonl: {args.jsonl}")
    return 0 if not bad else 1


if __name__ == "__main__":
    raise SystemExit(main())
