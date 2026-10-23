# DeepSeek V4.1 Flash effort through Claude Code / CPA

The live alias `deepseek-v4.1-flash-oc` mapped to Zen's
`deepseek-v4.1-flash-free` but had no `thinking` capability metadata on its
per-key CPA model entry. Claude Code sent `thinking.type: adaptive` and
`output_config.effort: max`; CPA removed both. Production request 27040
succeeded with an empty effort field. A successful response was not proof
that the client's selected effort reached Zen.

## Repair

Added `thinking.levels: [low, high, max]` to that one per-key alias in
`~/.cli-proxy-api/config.yaml` (`api-keys.claude`, group `claude-2`, prefix
`oc`, `keys[].models[]`). CPA hot-reloaded the change. Backup:
`~/.cli-proxy-api/config.yaml.bak-deepseek-max-20261023`.
The group-level list did not contain this alias; changing a different model's
metadata would not fix this execution path. Muse Spark retains its own ceiling.

The documented `~/.local/bin/claude-cpa` was missing. Restored it from the
tracked source now at `scripts/claude-cpa`, retaining the existing slot file,
CPA key-file lookup, caller credential precedence and default Muse/xhigh slot.
The selected custom model declares effort and max-effort capabilities.
The launcher injects a settings overlay as well as environment values so
saved `~/.claude/settings.json` effort settings cannot override an explicit
`--effort low`. Its credentials stay in environment, outside command arguments.
An explicit caller `--settings` retains control and bypasses injection.

## Verification

- `go build ./...`, `go test ./...`, staging binary build: passed.
- Actual `claude-cpa -p`, exact alias, explicit `--effort max` through an
  isolated CPA and staging relay on 18789: passed.
- Captured each boundary: Claude → CPA `output_config.effort=max`, CPA →
  relay `output_config.effort=max`, relay →
  `https://opencode.ai/inference/v1/chat/completions`
  `model=deepseek-v4.1-flash-free, reasoning_effort=max`.
- Actual Zen returned HTTP 200 with nonempty `reasoning_content` and the
  correct answer, `121877237`, to `12347 * 9871`.
- The upstream capture used a temporary loopback CONNECT interceptor trusted
  only by the staging relay. The upstream URL and `opencode://active` were
  retained; no OAuth tokens or credential database were copied or changed.
  The interceptor forwarded through the existing WARP SOCKS proxy and stored
  only effort/model/status and reasoning/content character counts.
- Live `claude-cpa -p --model deepseek-v4.1-flash-oc --effort max`: passed;
  production row 27094 recorded HTTP 200 and effort `max`.
- Live two-turn Bash computation passed, without permission denials; both
  model turns recorded `max` (27100 and 27102).
- Low control with an explicit overlay passed: production row 27115 recorded
  HTTP 200, adaptive thinking, and `output_config.effort=low`. This verifies
  the launcher respects the requested effort rather than always forcing max.
- Production health remained OK. No relay binary deployment was needed;
  staging stopped and Morph on 18787 was left running.

Sanitized upstream evidence is in
[evidence/2026-10-23-deepseek-max-hops.json](evidence/2026-10-23-deepseek-max-hops.json).
No prompts, credentials, or reasoning text are included in that evidence.

Run the repaired path with:

```sh
claude-cpa -p 'Your task' --model deepseek-v4.1-flash-oc --effort max
```

The provider contract accepts named effort levels; DeepSeek documents the
[thinking controls](https://api-docs.deepseek.com/guides/thinking_mode/), and
OpenCode documents the [Zen model route](https://docs.opencode.ai/docs/zen/).
The trace proves max was transmitted and accepted and that reasoning was
active. It cannot reveal Zen's private routing or prove a specific internal
reasoning budget; reasoning length and the model's own assertions are not
reliable effort-setting evidence. Claude's `thinkingTokens: 0` counter is also
insufficient: this run returned upstream reasoning despite that UI counter.
