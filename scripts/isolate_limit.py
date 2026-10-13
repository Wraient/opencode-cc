#!/usr/bin/env python3
"""Isolate WHICH variable the Zen free-tier limiter counts.

Three arms, same request volume, same model:
  A. warm cache  - identical prefix every call (cache hit, few uncached tokens)
  B. cold cache  - unique prefix every call (cache miss, ALL tokens uncached)
  C. tiny        - minimal prefix (few tokens total, never cached)

If the limiter counts uncached tokens, B locks out far sooner than A and C.
"""
import json, string, sys, time, urllib.request, urllib.error, random

BASE = "http://127.0.0.1:8787"
MODEL = sys.argv[1] if len(sys.argv) > 1 else "nemotron-3.5-lightning-free"
ROUNDS = int(sys.argv[2]) if len(sys.argv) > 2 else 25

FILLER = ("You are a helpful assistant. Follow these rules carefully. " * 400)
TOOLS = [{"type": "function", "function": {
    "name": "bash", "description": "Run a shell command",
    "parameters": {"type": "object",
                   "properties": {"command": {"type": "string"}},
                   "required": ["command"]}}}]


def send(system, user):
    body = {"model": MODEL, "stream": False, "max_tokens": 8,
            "messages": [{"role": "system", "content": system},
                         {"role": "user", "content": user}],
            "tools": TOOLS}
    req = urllib.request.Request(
        BASE + "/v1/chat/completions", data=json.dumps(body).encode(),
        headers={"content-type": "application/json", "Authorization": "Bearer tset"},
        method="POST")
    try:
        with urllib.request.urlopen(req, timeout=90) as r:
            raw = r.read().decode()
            u = (json.loads(raw).get("usage") or {})
            det = u.get("prompt_tokens_details") or {}
            return r.status, u.get("prompt_tokens"), det.get("cached_tokens")
    except urllib.error.HTTPError as e:
        return e.code, None, None
    except Exception:
        return 0, None, None


def arm(name, gen):
    print(f"\n=== arm {name} ===")
    ok = locked = 0
    tot_uncached = 0
    for i in range(1, ROUNDS + 1):
        system, user = gen(i)
        st, pt, ct = send(system, user)
        uncached = (pt - ct) if (pt and ct is not None) else (pt or 0)
        tot_uncached += uncached
        if st == 200:
            ok += 1
        else:
            locked = i
            print(f"  round {i}: HTTP {st}  <-- LOCKED after {ok} ok "
                  f"(~{tot_uncached} uncached tok so far)")
            break
        time.sleep(0.4)
    if not locked:
        print(f"  {ok}/{ROUNDS} ok, ~{tot_uncached} uncached tokens total "
              f"(avg {tot_uncached//max(ok,1)}/req) - NO LOCKOUT")
    return ok, locked, tot_uncached


def gen_warm(i):
    return FILLER, "reply with the number 1"


def gen_cold(i):
    # Unique filler per call => every token is a cache miss.
    return FILLER + " " + "".join(random.choices(string.ascii_lowercase, k=64)) + str(i), \
           f"reply with the number {i}"


def gen_tiny(i):
    return "You are helpful.", f"reply with the number {i}"


print(f"model={MODEL} rounds/arm={ROUNDS}")
a = arm("A warm cache (identical prefix)", gen_warm)
b = arm("B cold cache (unique prefix every call)", gen_cold)
c = arm("C tiny prompt (few tokens, uncached)", gen_tiny)

print("\n" + "=" * 60)
print(f"A warm : ok={a[0]:<3} uncached_total={a[2]}")
print(f"B cold : ok={b[0]:<3} uncached_total={b[2]}  locked_at={b[1]}")
print(f"C tiny : ok={c[0]:<3} uncached_total={c[2]}  locked_at={c[1]}")
verdict = ("limiter counts UNCACHED tokens (cache keeps you alive)"
           if (b[1] and not a[1]) else
           "limiter does NOT clearly track uncached tokens")
print(f"\nVERDICT: {verdict}")