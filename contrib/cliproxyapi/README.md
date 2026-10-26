# Reproduce the reference CPA build

The exact acceptance chain uses CLIProxyAPI **v8.0.4 + local3**, not a stock
release. `patches/v8.0.4-local3.patch` is a plain source diff from upstream base
`d33f63f8` (tag v8.0.4) to local commit `2864425`. It includes the three reference
changes: xAI refresh/rate-limit handling (`a57bd68`), service Retry-After parsing
(`4639715`), and opt-in Claude 429 hold (`2864425`). The xAI changes are included
to reproduce the exact binary's source; they are not necessary configuration
for a friend using only the dedicated relay route. Preserve the MIT notice in
`LICENSE`. No CPA auth pools, keys, binary, or private configuration are included.

Build in a new directory, never over an existing CPA checkout:

```bash
# Run from opencode-cc repo root; remember the absolute patch location.
relay_source="$PWD"
git clone --branch v8.0.4 --depth 1 https://github.com/router-for-me/CLIProxyAPI.git /tmp/cpa-friend-source
cd /tmp/cpa-friend-source
git rev-parse HEAD  # must start with d33f63f8
git apply --check "$relay_source/contrib/cliproxyapi/patches/v8.0.4-local3.patch"
git apply "$relay_source/contrib/cliproxyapi/patches/v8.0.4-local3.patch"
# Its go.mod determines the toolchain, independently of the relay's Go 1.24.
go test ./sdk/cliproxy/auth ./internal/runtime/executor/helps ./internal/runtime/executor ./internal/watcher/synthesizer
mkdir -p dist-local
# If /tmp has a quota, set BOTH TMPDIR and GOTMPDIR to a private directory
# with sufficient space; the linker can also allocate temporary files.
go build -ldflags='-s -w -X main.Version=8.0.4+local3 -X main.Commit=2864425' -o dist-local/cli-proxy-api ./cmd/server
install -d "$HOME/.local/opt/cliproxyapi" "$HOME/.cli-proxy-api"
# New installation only. Existing CPA needs its own staged/atomic deployment.
install -m 755 dist-local/cli-proxy-api "$HOME/.local/opt/cliproxyapi/cli-proxy-api"
chmod 700 "$HOME/.cli-proxy-api"
```

Install `config.example.yaml` as `~/.cli-proxy-api/config.yaml` (0600), replacing
both placeholders privately. Create the CPA→relay client key through the relay
panel with `require_api_key=true`. The relay's upstream Zen key stays in the
relay; neither CPA key is a Zen credential.

Example `~/.config/systemd/user/cliproxyapi.service`:

```ini
[Unit]
Description=Local CPA with relay hold policy
After=network-online.target opencode-cc.service
Wants=opencode-cc.service

[Service]
ExecStart=%h/.local/opt/cliproxyapi/cli-proxy-api -config %h/.cli-proxy-api/config.yaml
WorkingDirectory=%h/.cli-proxy-api
Environment=NO_PROXY=127.0.0.1,localhost,::1
UMask=0077
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
```

Run `systemctl --user daemon-reload` and
`systemctl --user enable --now cliproxyapi`. Keep management loopback/private.
To use `scripts/claude-cpa`, also save the client→CPA key in
`~/.cli-proxy-api/api-key.txt` (0600); configure its model slots separately.
Direct harness configuration does not need that extra file.

The isolated runner generates its own dummy CPA keys and runs the installed
binary on :18319; it never needs production CPA's key pool. A 195-second outage
can exceed the 180-second hold; success depends on real Claude retrying, then
completing, and a second invocation recovering in the same CPA process. Test
that chain with the actual installed CLI rather than promising recovery from
unit tests or patch presence alone.

The patch is pinned to this base. Do not apply it to an arbitrary latest release
with forced/rejected hunks. New CPA versions need a deliberate port and fresh
acceptance. Do not set global `disable-cooling` or remove genuine cooldowns for
other providers. To roll back, restore a matched binary and config: stock CPA
may reject the custom `rate-limit-hold-seconds` field.
