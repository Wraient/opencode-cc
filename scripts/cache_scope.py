#!/usr/bin/env python3
"""Does the Zen prompt cache SURVIVE a session-id rotation?

This is the decisive test for the user's requirement: long Claude Code /
OpenCode sessions rely on cached tokens, and they need to know whether
spinning up a new session id silently throws that cache away.

Method:
  1. Send an identical large prefix repeatedly under session A until the
     cache is warm (cached_tokens climbs).
  2. Repeat the SAME requests under a brand-new session B.
  3. Compare cached_tokens in A vs B.

If B shows cached_tokens near zero, the cache is session-scoped and rotating
the id is very expensive. If B stays warm, the cache is account/prefix-scoped
and rotation is safe.
"""
import json, sys, time, urllib.request, urllib.error

BASE = "http://127.0.0.1:8787"
MODEL = sys.argv[1] if len(sys.argv) > 1 else "nemotron-3.5-lightning-free"
ROUNDS = int(sys.argv[2]) if len(sys.argv) > 2 else 6

FILLER = ("You are a helpful assistant. Follow these rules carefully. " * 400)
TOOLS = [{"type": "function", "function": {
    "name": "bash", "description": "Run a shell command",
    "parameters": {"type": "object",
                   "properties": {"command": {"type": "string"}},
                   "required": ["command"]}}}]


def send(user, session):
    body = {"model": MODEL, "stream": False, "max_tokens": 8,
            "messages": [{"role": "system", "content": FILLER},
                         {"role": "user", "content": user}],
            "tools": TOOLS}
    req = urllib.request.Request(
        BASE + "/v1/chat/completions", data=json.dumps(body).encode(),
        headers={"content-type": "application/json",
                 "Authorization": "Bearer tset",
                 "x-opencode-session": session},
        method="POST")
    try:
        with urllib.request.urlopen(req, timeout=90) as r:
            u = (json.loads(r.read().decode()).get("usage") or {})
            det = u.get("prompt_tokens_details") or {}
            return r.status, u.get("prompt_tokens"), det.get("cached_tokens")
    except urllib.error.HTTPError as e:
        return e.code, None, None
    except Exception:
        return 0, None, None


sessA = "ses_cachewarmtest000000000A"
sessB = "ses_cachewarmtest000000000B"

print(f"model={MODEL}")
print("\n--- session A (warming the cache, identical prefix) ---")
a_cached = []
for i in range(1, ROUNDS + 1):
    st, pt, ct = send("reply with the number 1", sessA)
    a_cached.append(ct)
    print(f"  A#{i}: HTTP {st} prompt={pt} cached={ct}"
          + (f" ({100*ct/pt:.0f}%)" if pt and ct else ""))
    time.sleep(0.4)

print("\n--- session B (NEW id, identical prefix) ---")
b_cached = []
for i in range(1, ROUNDS + 1):
    st, pt, ct = send("reply with the number 1", sessB)
    b_cached.append(ct)
    print(f"  B#{i}: HTTP {st} prompt={pt} cached={ct}"
          + (f" ({100*ct/pt:.0f}%)" if pt and ct else ""))
    time.sleep(0.4)


def warm(vals):
    v = [x for x in vals if x]
    return max(v) if v else 0


print("\n" + "=" * 58)
print(f"session A peak cached_tokens : {warm(a_cached)}")
print(f"session B peak cached_tokens : {warm(b_cached)}")
if warm(b_cached) == 0 and warm(a_cached) > 0:
    print("\nVERDICT: cache is SESSION-SCOPED.")
    print("Rotating the session id throws the cache away -> every request")
    print("becomes fully uncached -> burns the quota much faster.")
elif warm(b_cached) > 0:
    print("\nVERDICT: cache SURVIVES a session-id rotation (prefix/account scoped).")
    print("Rotating is cheap; cached tokens are retained.")
else:
    print("\nVERDICT: inconclusive (no cache observed anywhere).")