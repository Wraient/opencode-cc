#!/usr/bin/env python3
"""One non-streamed probe on the OpenAI route (/v1/chat/completions), same document builder."""
import json, sys, time, urllib.error, urllib.request
from pathlib import Path
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import probe as P

chars, seed, base = int(sys.argv[1]), int(sys.argv[2]), "http://127.0.0.1:18789"
text, codes = P.build(seed, chars)
body = {"model": "deepseek-v4.1-flash-free", "max_tokens": 4096, "stream": False,
        "messages": [{"role": "user", "content": text}]}
req = urllib.request.Request(base + "/v1/chat/completions", data=json.dumps(body).encode(),
                             headers={"content-type": "application/json"}, method="POST")
t0 = time.monotonic()
try:
    resp = P.OPENER.open(req, timeout=1500)
    out = json.loads(resp.read())
    ans = out["choices"][0]["message"]["content"] or ""
    found = [c for c in codes if c in ans]
    print(json.dumps({"route": "chat/completions", "chars": len(text), "status": resp.status,
                      "prompt_tokens": out["usage"]["prompt_tokens"],
                      "cache_read": (out["usage"].get("prompt_tokens_details") or {}).get("cached_tokens"),
                      "recall": f"{len(found)}/5", "t_total_s": round(time.monotonic() - t0, 2)}, indent=2))
except urllib.error.HTTPError as e:
    print(json.dumps({"route": "chat/completions", "chars": len(text), "status": e.code,
                      "t_s": round(time.monotonic() - t0, 2),
                      "error": e.read()[:300].decode("utf-8", "replace")}, indent=2))
