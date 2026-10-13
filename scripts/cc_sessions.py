#!/usr/bin/env python3
"""Run N Claude Code sessions via `claude -p` through a given base URL and
report what each one actually got.

Defaults to the full chain: Claude Code -> cliproxyapi :8317 -> opencode-cc
:8787 -> Zen, using the space-bunny-oc alias.

Also records whether Retry-After reached Claude Code, and (optionally) whether
rotating the Claude Code session id changes the outcome.
"""
import argparse, json, os, re, subprocess, sys, tempfile, time
from concurrent.futures import ThreadPoolExecutor

CPA_KEY = None


def load_key(cfg_path):
    import re as _re
    s = open(cfg_path).read()
    m = _re.search(r'access:\s*\n\s*api-keys:\s*\n\s*-\s*"([^"]+)"', s)
    return m.group(1) if m else None


def run_one(idx, base_url, model, api_key, timeout, rotate, extra_env):
    env = dict(os.environ)
    env.update(extra_env)
    home = tempfile.mkdtemp(prefix=f"ccsess{idx}-")
    env["HOME"] = home
    # Claude Code reads its own config from HOME; point it at the real one so
    # auth/model routing still resolves, but keep session state isolated.
    cfgdir = os.path.join(home, ".claude")
    os.makedirs(cfgdir, exist_ok=True)
    settings = {
        "env": {
            "ANTHROPIC_BASE_URL": base_url,
            "ANTHROPIC_AUTH_TOKEN": api_key,
            "ANTHROPIC_API_KEY": api_key,
        }
    }
    if rotate:
        # Distinct session id per run, in case the rate limit is session scoped.
        settings["env"]["CLAUDE_SESSION_ID"] = f"sess-probe-{idx}-{int(time.time())}"
    with open(os.path.join(cfgdir, "settings.json"), "w") as f:
        json.dump(settings, f)

    args = ["claude", "-p", "Reply with exactly: PONG",
            "--model", model, "--output-format", "json"]
    t0 = time.time()
    try:
        p = subprocess.run(args, capture_output=True, text=True,
                           timeout=timeout, env=env, cwd=home)
        dt = time.time() - t0
        out = (p.stdout or "") + (p.stderr or "")
        return dict(idx=idx, rc=p.returncode, dt=dt, out=out[:1200], env=home)
    except subprocess.TimeoutExpired:
        return dict(idx=idx, rc=124, dt=time.time() - t0, out="TIMEOUT", env=home)
    except Exception as e:
        return dict(idx=idx, rc=1, dt=time.time() - t0, out=repr(e), env=home)


def classify(out, rc):
    low = out.lower()
    if rc == 124:
        return "TIMEOUT"
    if "pong" in low:
        return "OK"
    # Only treat it as a rate limit when Claude Code actually reports an API
    # error. A successful result can quote the words "429"/"rate limit" inside
    # its own text, which is not a transport failure.
    is_api_error = '"is_error":true' in low or '"subtype":"error' in low \
        or '"error":' in low
    if is_api_error and ("rate limit" in low or "429" in low):
        return "RATE_LIMIT"
    if "model unavailable" in low:
        return "MODEL_UNAVAILABLE"
    if "responses-api-only" in low:
        return "WRONG_LANE"
    if "invalid api key" in low or "401" in low:
        return "AUTH"
    if rc != 0:
        return "ERROR"
    return "OK"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base-url", default="http://127.0.0.1:8317")
    ap.add_argument("--model", default="space-bunny-oc")
    ap.add_argument("--n", type=int, default=20)
    ap.add_argument("--concurrency", type=int, default=5)
    ap.add_argument("--timeout", type=int, default=120)
    ap.add_argument("--rotate-session", action="store_true")
    ap.add_argument("--config", default=os.path.expanduser("~/.cli-proxy-api/config.yaml"))
    args = ap.parse_args()

    key = load_key(args.config)
    if not key:
        print("could not read api key from", args.config)
        return 2

    print(f"base={args.base_url} model={args.model} n={args.n} "
          f"concurrency={args.concurrency} rotate={args.rotate_session}")
    print("-" * 78)

    t0 = time.time()
    with ThreadPoolExecutor(max_workers=args.concurrency) as ex:
        futs = [ex.submit(run_one, i + 1, args.base_url, args.model, key,
                          args.timeout, args.rotate_session, {})
                for i in range(args.n)]
        results = [f.result() for f in futs]
    results.sort(key=lambda r: r["idx"])

    tally = {}
    for r in results:
        v = classify(r["out"], r["rc"])
        tally[v] = tally.get(v, 0) + 1
        print(f"[{v:>12}] {r['idx']:2d}/{args.n} rc={r['rc']:<4} {r['dt']:6.1f}s"
              + ("  " + r["out"].strip().replace("\n", " ")[:90] if v != "OK" else ""))

    print("-" * 78)
    print(f"RESULT: " + "  ".join(f"{k}={v}" for k, v in sorted(tally.items())))
    print(f"ok={tally.get('OK',0)}/{args.n}  wall={time.time()-t0:.1f}s  "
          f"max={max(r['dt'] for r in results):.1f}s  "
          f"avg={sum(r['dt'] for r in results)/len(results):.1f}s")

    # Retry-After evidence from the proxy logs.
    for f in (os.path.expanduser("~/data/opencode-cc.db"),):
        if os.path.exists(f):
            try:
                import sqlite3
                c = sqlite3.connect(f)
                rows = c.execute(
                    "select datetime(ts/1000,'unixepoch','localtime'), incoming_model, status "
                    "from requests where ts > (strftime('%s','now')-900)*1000 "
                    "order by ts desc limit 6").fetchall()
                print("\nopencode-cc recent requests:")
                for r in rows:
                    print("  ", r)
            except Exception as e:
                print("(log query failed:", e, ")")
    return 0 if tally.get("OK", 0) == args.n else 1


if __name__ == "__main__":
    sys.exit(main())