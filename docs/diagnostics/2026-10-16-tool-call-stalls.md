# Muse Spark premature tool-less completions

Affected T3 thread: `9e1da80f-af1f-4790-b6d5-c03f7af95c10`.

The upstream emits a complete response with message items and no function_call
items, ending at an action introduction such as “Now the commit:”. The bridge
faithfully emits end_turn, so Claude Code ends the agent run. This is not a
colon parser failure: the colon is ordinary upstream text. The existing single
identical resample sometimes rescues the turn, but can return another complete
text-only answer. Frame census below confirms the missing calls originate
upstream rather than being dropped by the converter. It does not establish why
the model itself samples that premature completion.

Fix: when the existing opt-in tool-less recovery is enabled, a short colon-ended
completion is continued with the assistant introduction and a user continuation
instruction appended to the upstream conversation. Up to three recovery samples
are tried; only usable declared calls are replayed, with one downstream terminal
message. Other tool-less answers retain the existing one-resample behavior.
Unresolved stream stalls are persisted with stop_reason=tool_call_stall and an
error description, while the actual wire stop remains end_turn. Recovered stalls
also retain a diagnostic response snapshot instead of only [streamed].

Historical journal evidence (local time, Asia/Kolkata):

```text
Oct 16 07:16:23 deez opencode-cc[925209]: 2026/10/16 07:16:23 opencode-cc: toolless resample unusable status="completed" failed=false tools=false frames=response.completed:1,response.content_part.added:1,response.content_part.done:1,response.created:1,response.in_progress:1,response.output_item.added:1,response.output_item.done:1,response.output_text.delta:6,items:added:message:1,items:done:message:1,status:completed model=muse-spark-1.3-contributor-free target=muse-spark-1.3-contributor-free
Oct 16 07:16:23 deez opencode-cc[925209]: 2026/10/16 07:16:23 opencode-cc: short end_turn out=26 in=161015 empty_text=false text="Dedup applied. Now the detail drawer field plus the filter fix (crash on rows with null error):" frames=response.completed:1,response.content_part.added:1,response.content_part.done:1,response.created:1,response.in_progress:1,response.output_item.added:1,response.output_item.done:1,response.output_text.delta:10,items:added:message:1,items:done:message:1,status:completed model=muse-spark-1.3-contributor-free target=muse-spark-1.3-contributor-free
Oct 16 07:47:17 deez opencode-cc[1005208]: 2026/10/16 07:47:17 opencode-cc: toolless resample unusable status="completed" failed=false tools=false frames=response.completed:1,response.content_part.added:1,response.content_part.done:1,response.created:1,response.in_progress:1,response.output_item.added:1,response.output_item.done:1,response.output_text.delta:3,items:added:message:1,items:done:message:1,status:completed model=muse-spark-1.3-contributor-free target=muse-spark-1.3-contributor-free
Oct 16 07:47:17 deez opencode-cc[1005208]: 2026/10/16 07:47:17 opencode-cc: short end_turn out=10 in=116309 empty_text=false text="README updated. Now the commit:" frames=response.completed:1,response.content_part.added:1,response.content_part.done:1,response.created:1,response.in_progress:1,response.output_item.added:1,response.output_item.done:1,response.output_text.delta:3,items:added:message:1,items:done:message:1,status:completed model=muse-spark-1.3-contributor-free target=muse-spark-1.3-contributor-free
Oct 16 07:48:22 deez opencode-cc[1005208]: 2026/10/16 07:48:22 opencode-cc: toolless resample unusable status="completed" failed=false tools=false frames=response.completed:1,response.content_part.added:1,response.content_part.done:1,response.created:1,response.in_progress:1,response.output_item.added:1,response.output_item.done:1,response.output_text.delta:6,items:added:message:1,items:done:message:1,status:completed model=muse-spark-1.3-contributor-free target=muse-spark-1.3-contributor-free
Oct 16 07:48:22 deez opencode-cc[1005208]: 2026/10/16 07:48:22 opencode-cc: short end_turn out=31 in=117651 empty_text=false text="The `scripts/bench_mixed20.py` is untracked and not mine — leaving it alone. Committing only the tracked changes:" frames=response.completed:1,response.content_part.added:1,response.content_part.done:1,response.created:1,response.in_progress:1,response.output_item.added:1,response.output_item.done:1,response.output_text.delta:6,items:added:message:1,items:done:message:1,status:completed model=muse-spark-1.3-contributor-free target=muse-spark-1.3-contributor-free
```

Validation: full `go build ./...` and `go test ./...` passed. Isolated staging
used port 18789 because Morph owns 18787. Real DeepSeek chat returned OK.
Real Muse Spark produced “Checking the time:” with no call; continuation
recovered get_time and emitted exactly one message_stop with tool_use.
The same real Messages probe passed after deploying to port 8787.
