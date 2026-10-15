# Native rate-limit recovery

The plugin entrypoint is `index.ts`, with `policy.ts` and `store.ts`
in the same directory. It requires OpenCode v2 native session hooks, verified
with released v2.0.24 and v2.0.29 binaries.

## Install

This optional OpenCode client plugin complements the Go proxy. Back up any existing
retry plugin first and disable it to avoid competing retry policies. Copy
`index.ts`, `policy.ts`, and `store.ts` together to a dedicated directory outside
this checkout, such as `~/.local/share/opencode-plugins/rate-limit-retry`.

Add that directory's absolute path to your OpenCode v2 configuration, merging with
existing entries:

```json
{
  "plugins": [
    { "package": "/absolute/path/to/rate-limit-retry", "options": {} }
  ]
}
```

Restart OpenCode after initial installation. The directory must contain all three
modules. Do not register the same plugin twice through both explicit configuration
and an existing auto-loaded plugin directory. This contribution does not install
itself, modify the proxy service, or migrate an older plugin's cooldown state.

## Behavior

- Uses `http.request`, `http.response`, and native `retry` hooks. No event-stream
  listener, synthetic user prompts, child processes, IP resets, or Telegram calls.
- Parses structured status/error fields. `FreeUsageLimitError` and account quota
  differ from transient rate/concurrency limits, transport failures, and 403 auth
  or compatibility failures. It never reclassifies a successful answer containing
  the string "429" as a rate limit.
- Honors numeric/date Retry-After deadlines. Short waits use the native retry
  scheduler (therefore cancellation remains owned by OpenCode). Long deadlines
  stop automatic retry instead of shortening the provider deadline.
- Cooldowns survive new sessions/processes. Scopes hash origin, provider, model,
  and credential/organization identity; credentials and request contents are not
  persisted. Another account/model is not blocked by an unrelated scope.
- Missing quota deadlines use an explicitly estimated 15-minute local cooldown;
  no automatic quota retry is attempted. Missing transient rate-limit deadlines
  begin at 30 seconds with increasing backoff. These are estimates, not promises
  that the upstream quota has reset.
- Corrupt/unavailable state stops retries. Healthy in-flight responses cannot
  erase a newer quota deadline. HTTP error bodies remain available to the client.
- Free OpenCode/opencode-cc models get an input limit of at most 128,000 tokens.
  Native automatic compaction uses that limit, rather than growing to a 300k+
  cold prefill. Smaller models keep their smaller limits; paid/other providers,
  output limits, context windows, session IDs, and stored history are unchanged.
  Compaction is summarization: it does not promise verbatim active-context recall.

## Options

Defaults in `policy.ts`: `maxRetries=3`, `baseDelayMs=2000`, `maxDelayMs=30000`,
`maxAutomaticWaitMs=120000`, `quotaFallbackMs=900000`, `rateFallbackMs=30000`,
`freeInputBudget=128000`. `freeInputBudget=0` disables this input limit.
`enabled=false` disables the plugin. `stateDir` or
`RATE_LIMIT_RETRY_STATE_DIR` overrides the state directory; default is
`$XDG_STATE_HOME/opencode/rate-limit-retry-v2` or `~/.local/state/...`.
The old IP-reset, prompt retry, and claim TTL options are intentionally unused.

## Tests

```
bun test contrib/opencode/rate-limit-retry/tests/retry.test.ts
python3 contrib/opencode/rate-limit-retry/tests/native_recovery.py --cli /absolute/path/to/opencode --out /tmp/native-retry-result.json
```

The integration test uses an isolated HOME/database, a loopback mock provider,
and mock credentials. It verifies real 429/200 statuses, elapsed Retry-After,
identical retry payloads, remembered context, exactly three user messages,
and cross-process blocking during a one-hour quota. It never changes live
credential cooldowns or uses a different live model as supposed target proof.
The CLI harness also checks transient 503 recovery and hard quota without a
deadline. Its provider and credentials are fixtures, not live quota measurements.

## Activation and limits

The OpenCode source watcher reloads the entrypoint without restarting the user's
server. When editing helper modules, update/touch `index.ts` last so the complete
revision is reloaded. Use the model API to verify the active free-model input
limits. This plugin cannot extend upstream quota or cache lifetime, and does not
claim to intercept native WebSocket traffic before its handshake.
