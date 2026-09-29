# 500-turn tool-loop benchmark (2026-09-29/30)

Stress test for the tool-call stall fix ("says it, doesn't do it, needs
continue"): one short text line + one `Bash echo N` tool call per turn,
history growing like a real agent loop, through staging (`:18787`, resample
flags ON). Loop stops at the first turn with no tool call.

Method: `REPRO_MODEL=<id> REPRO_TURNS=500 python3 /tmp/repro_loop.py`
(single declared `Bash` tool, `stream:true`, 60s backoff on 429/5xx).
Prompt framing is open-ended ("keep going indefinitely") after we learned
a "100 turns" prompt makes models correctly stop at 101.

## Matrix results

| Model | Lane | Result |
|---|---|---|
| `space-bunny-free` | chat | **500/500 clean** (also 100/100 twice before) |
| `longcat-2.5-preview-free` | chat | **500/500 clean** |
| `nemotron-3-ultra-free` | chat | 51 clean turns (was 5 pre-fix, 19 with bridge-only resample); stall at 52 is length-degeneracy (4096 newlines, correctly NOT resampled) |
| `nemotron-3.5-lightning-free` | chat | 20 clean turns, stall at 11/21 with gibberish meltdown (out=536, over resample cap — correctly untouched); turn-21 stall was `stream_error` (mid-stream abort, new resample path covers it) |
| `big-pickle`, `mimo-v2.5/2.6-flash-free`, `muse-spark-1.2/1.3-free` | — | 429-skipped (free-lane quota exhausted by probing; rerun pending) |
| `jev-1.13-free` | — | HTTP 400, protocol-incompatible (upstream rejects) |

Input context grew 685 → 64K tokens over 500 turns with zero drops,
zero corrupt args, exact `toolu_` IDs throughout on the clean models.

## Live rescue receipts (staging log)

- `toolless resample [chat] RESCUED model=nemotron-3-ultra-free` ×5
  (empty upstream streams re-POSTed, tools landed, loops continued)
- `toolless resample [chat] RESCUED model=nemotron-3.5-lightning-free` ×1
- Zero `still toolless` on rescued runs; zero dropped-call lines during
  the entire matrix.

## Unit/e2e coverage (`go test ./...` green)

- `TestStallRepro*` (12): nameless delta-only recovery (both lanes),
  orphan-call diagnosis, namespaced rename, reasoning-done noise,
  parallel-call separation, index-collision split, truncation→max_tokens,
  done-text fallback + no-double, frame/item census.
- `TestShouldResampleToolless` table (9 verdicts incl. cap boundary).
- `TestScanCountsMalformedLines`: corrupt upstream lines counted, not silent.
- `TestBridgeToollessResample{RescuesStream,Disabled,GivesUpWhenStillToolless,RescuesAggregated}`,
  `TestChatToollessResample{RescuesEmptyStream,Disabled}`,
  `TestChatAbortedStreamResample` (hijacked-connection e2e).
- Full suite: `go build ./... && go test ./...` green (2026-09-30).
