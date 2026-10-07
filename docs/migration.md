# Installing, upgrading and rolling back MCParcel

Release candidate `0.1.0-rc.1`, 7 October 2026. MCParcel is installed from a local
tarball; it is not published to npm. The npm package runs on macOS with Apple
Silicon only; for Linux, see [headless.md](headless.md). Moving from mcporter
happens one connection at a time: see [Per-connection status](#per-connection-status)
and [Agent instructions during the mixed period](#agent-instructions-during-the-mixed-period).

## Install

You need macOS on Apple Silicon and Node.js 20 or newer; running MCParcel needs
neither Go nor Homebrew. Building the tarball in a checkout needs Go:

```sh
make npm-pack
# writes dist/mcparcel-0.1.0-rc.1.tgz
```

Run it without installing:

```sh
npx --yes --package ./dist/mcparcel-0.1.0-rc.1.tgz mcparcel doctor
```

Or install it so `mcparcel` is on your PATH:

```sh
npm install -g ./dist/mcparcel-0.1.0-rc.1.tgz
mcparcel doctor
```

`mcparcel doctor` is offline. It does not start the runtime, read the Keychain or
1Password, or write anything. It exits 0 when no check failed and 8 when at least
one did; every failed row has a next action. `mcparcel version` prints
`mcparcel 0.1.0-rc.1+<commit> (<commit>, <date>)`; a tarball built from a
checkout with uncommitted changes adds `.dirty.<UTC seconds>` after the commit.

The macOS binary is ad-hoc signed. It is not Developer ID signed or notarized.

## Upgrade

1. Install the new tarball the same way (`npm install -g <new tgz>`, or `npx`
   with the new tarball).
2. Run `mcparcel doctor`. If the runtime of the old version is still running,
   `runtime.version` fails and names both versions and the daemon's PID. Until
   you restart it, `tools` and `call` fail with `runtime_version_mismatch`.
   MCParcel never restarts the runtime on its own.
3. When no calls are active, run `mcparcel runtime restart`. Under
   `runtime serve`, restart it through its supervisor instead.
4. Run `mcparcel doctor` again. `runtime.version` and `runtime.binary` should be
   `ok`.

Stored sign-ins and Keychain values stay readable. Their Keychain identity is a
fixed service name plus the connection ID or variable name; the binary's path,
version and signature are not part of it. The 1Password desktop app may ask once
to approve the new binary.

The runtime runs from a copy of the binary in
`~/.local/share/mcparcel/runtime/<version>/mcparcel`, so cleaning the npm or npx
cache does not affect a running daemon. A new version's copy replaces all older
copies except the most recent one, which stays for a rollback.

If `add` or `sync` reports `catalog_requires_upgrade`, the team catalog needs a
newer MCParcel than the one you run: upgrade first. Your saved snapshot of that
catalog stays active in the meantime.

## Roll back to a previous MCParcel

Install the older tarball again, then run `mcparcel runtime restart` when no
calls are active. Your configuration, selections and stored sign-ins are kept.

Rolling back to a build from before the first release candidate (`0.1.0-rc.1`)
is not supported: those binaries do not know the catalog `minVersion` field and reject any catalog that
has it. If a saved catalog asks for a newer MCParcel than the one you rolled back
to, it keeps working, and `doctor` reports it under `version.catalog`.

## Per-connection status

Statuses come from the compatibility runner in `tests/compatibility` and are
copied in by hand after review; the runner never edits this page or the
manifest. The manifest (`tests/compatibility/manifest.json`) holds the workflow
each connection is tested with: one read-only tool call and its assertion. A
connection switches to MCParcel only when it is `passed` (or `waived`) **and**
you say so. `Automated` is `yes` when the runner runs the row by default and
`no` when it needs a prerequisite in place first. The status legend is in
[acceptance.md](acceptance.md#stage-11-per-connection-acceptance).

| Connection | Transport | Status | Automated | Evidence or blocker |
| --- | --- | --- | --- | --- |
| `local:blender` | stdio-uvx | passed | no | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 51, hmac 43826a442e7b |
| `local:browserstack` | stdio-npx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 1, hmac 478f01f3866a |
| `local:bugsnag` | stdio-npx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 3, hmac 609b4614c5ce |
| `local:chrome-devtools` | stdio-npx | not_run | no | Needs Chrome running and accepting `--autoConnect`. |
| `local:codex-cu` | stdio-node | not_run | no | Needs the Codex Computer Use plugin; run from a terminal. |
| `local:context-mode` | stdio-npx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 5, hmac 75bb411346fc |
| `local:context7` | stdio-npx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 7, hmac ee947ad5b31f |
| `local:exa` | stdio-npx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 9, hmac 04e2be2bde8d |
| `local:excalidraw` | stdio-docker | blocked | no | The host Excalidraw canvas server was not running on 2026-10-07 and the container has auto-start disabled (tool_error at call, same under mcporter): start the canvas server on the host, then rerun. |
| `local:figma` | https-oauth | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 11, hmac c963425c362b |
| `local:firecrawl` | stdio-npx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 13, hmac 26dd50c2f1ca |
| `local:front-mcp` | https-oauth | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 15, hmac 3ad334eb6f8e |
| `local:glitchtip` | https-oauth | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 17, hmac 02a6276a9d01 |
| `local:higgsfield` | https-oauth | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 19, hmac 3f95a175a155 |
| `local:home-assistant` | http-internal | passed | no | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 53, hmac f98674eb9e81 |
| `local:lighthouse` | stdio-npx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 21, hmac 1fdd2d503040 |
| `local:maestro` | stdio-binary | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 23, hmac 8779fbde8f4e |
| `local:mobbin` | https-oauth | not_run | no | Not signed in on 2026-10-07, so no workflow tool yet: sign in, then pick a read-only search tool. |
| `local:neuronwriter` | https-oauth | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 25, hmac 2ddba5127fdf |
| `local:notion` | https-oauth | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 27, hmac 9864404bedff |
| `local:octocode` | stdio-npx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 29, hmac d0c7aa746b83 |
| `local:ovh-logs` | stdio-uvx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 31, hmac 23d98dd27941 |
| `local:paper` | http-loopback | passed | no | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 55, hmac b0529c308ccc |
| `local:perplexity` | stdio-npx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 33, hmac 279d9e68c4d4 |
| `local:proxmox-mcp-plus` | stdio-uvx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 35, hmac 9e67c9fd40b0 |
| `local:rails-blocks` | stdio-binary | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 37, hmac 3aac1238d86d |
| `local:rubrikit-staging` | https-oauth | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 39, hmac c01667e355dd |
| `local:se-ranking` | https-header | not_run | no | The server rejected the Authorization header on 2026-10-07: fix the key, then pick a read-only tool. |
| `local:sequential-thinking` | stdio-npx | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 41, hmac 0d3480d3c862 |
| `local:slack` | https-oauth | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 43, hmac 3c14b70a0891 |
| `local:treg` | https-header | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 45, hmac ea4dfb9dc027 |
| `local:typefully` | https-oauth | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 47, hmac cb9af94acf21 |
| `local:uptimerobot` | https-oauth | passed | yes | 2026-10-07, 0.1.0-rc.1+6476830.dirty, rev 49, hmac e8acf1d616d3 |

Replacement ready means the 32 original connections are `passed` or `waived`;
`codex-cu` is tracked separately (stage 8b).

## Running the compatibility runner

The runner briefly enables, calls and disables each connection with your real
configuration, then puts its selection back as it found it. Before a run:

1. `mcparcel doctor` shows a running runtime (`runtime.version` ok) of the same
   version as the binary you pass in `MCPARCEL_COMPAT_BIN`. Start it first with
   `mcparcel runtime restart`, so the runner never auto-starts one. A version
   mismatch blocks every row with `runtime_version_mismatch`.
2. Run one interactive `mcparcel tools <mcp>` on a connection with `op://`
   references, so the 1Password session is open. The runner passes `--no-input`,
   so an approval it would need becomes `auth_required` and the row `blocked`.
3. Check `mcparcel auth status` for the OAuth rows; sign in where it says
   `sign-in required`.
4. While it runs: no `setup`, `sync`, `import --apply` or other configuration
   write, and no agent calling these connections through MCParcel. Each row is
   briefly disabled, and a concurrent `sync` could set a review that the
   runner's `enable` would then accept.

Then, from the repository root:

```sh
MCPARCEL_COMPAT_LIVE=1 MCPARCEL_COMPAT_BIN=<abs path to mcparcel> \
MCPARCEL_COMPAT_OUT="$PWD/.scratch/stage11/compat-$(date -u +%Y%m%dT%H%M%SZ).json" \
go test ./tests/compatibility -run TestCompatibilityLive -count=1 -timeout 3h -v
```

By default it runs every row marked `yes`. Three of those spend paid API
credits: `perplexity` (one Sonar request), `exa` (one search) and `firecrawl`
(a few search credits), each called twice per run. Rows marked `no` run only when you
name them in `MCPARCEL_COMPAT_IDS` (for example `MCPARCEL_COMPAT_IDS=paper,local:blender`)
once their prerequisite is in place; naming rows runs only those.
`MCPARCEL_COMPAT_TIMEOUT` sets the per-row budget (default `5m`).

The report goes under `.scratch/` and is never committed; the runner refuses an
output path elsewhere in the repository. It holds codes, counts, revisions,
versions and keyed digests, never result content (fields in
[acceptance.md](acceptance.md#stage-11-per-connection-acceptance)). If a run is
killed, the row it was on is left as `"status": "running"`, `"restored": false`
with `initialEnabled`: put that one connection back by hand with
`mcparcel enable <mcp>` or `mcparcel disable <mcp>`.

After reviewing the report, copy back per row, into the manifest row (`status`,
`evidence`, `blocker`) and into the table above (Status, Evidence or blocker),
which `go test ./tests/compatibility` keeps in agreement:

- `passed`: the evidence line `<date>, <binary version>, rev <configRevision>, hmac <first 12 hex of calls[0].resultHmac>`,
  for example `2026-10-08, 0.1.0-rc.1+abc123def456, rev 41, hmac 1a2b3c4d5e6f`.
  A recorded hand run ends in `manual` instead of the `hmac` part. The manifest
  accepts nothing else in `evidence`.
- `failed` or `blocked`: one sentence in `blocker` saying what is missing or
  wrong, without values, URLs or account names.
- Add a line to the Runs table in acceptance.md.

## Agent instructions during the mixed period

You decide when each connection switches; MCParcel never edits agent instructions (the
global `AGENTS.md`, `CLAUDE.md` or installed skills). While some connections
still run through mcporter, the instruction names the ones that switched, for
example:

```markdown
MCP servers: use mcparcel for context7, exa and perplexity
(`mcparcel tools <mcp>` for schemas, `mcparcel call <mcp>.<tool> key=value`,
add `--json` for machine output). Use mcporter for every other MCP server.
Never call the same server through both.
```

Short names work because import keeps the mcporter server names as aliases.
When every connection has switched, the instruction becomes the README's
"Gebruik mcparcel voor alle MCP-aanroepen." ("Use mcparcel for all MCP calls.").

## Syntax changes from mcporter

| What | mcporter | mcparcel |
| --- | --- | --- |
| Tool schemas | `mcporter list <server> --schema` | `mcparcel tools <mcp>` (live; `--json` gives `data.items[]` with `name`, `description`, `inputSchema`) |
| Enabled servers | `mcporter list` | `mcparcel list` (offline, enabled only); `mcparcel catalog` for everything |
| Call | `mcporter call <server>.<tool> key=value` | unchanged; `key:value` also works, values are typed by the tool schema, `key:=json` forces JSON |
| Whole-object arguments | `--args '<json>'` | unchanged; also `--args-file <path>` or `--args-file -` |
| JSON output | `--output json` | `--json`: one envelope `{schemaVersion, ok, data, error}`, the MCP result in `data.result` |
| Errors | exit status and text | exit codes 0–8 and 130, branch on `error.code` ([cli.md](cli.md#output-and-errors)) |
| Sign-in | handled by mcporter | `mcparcel auth login <mcp>`; a call never opens a browser, it fails `auth_required` (exit 3) |
| Images | — | `--output-dir <dir>` saves image and audio blocks |
| Function-call syntax, positional arguments | `server.tool(a: 1)` | not supported: name every argument |
| Ad-hoc servers | `--stdio`, `--http-url` | not supported: `mcparcel local add --file <definition.json>` |
| Import | — | `mcparcel import mcporter --file <path>` previews; `--apply` writes personal definitions, never the source file |

Sources: [cli.md](cli.md) "Arguments and identity" and "Deferred surface", and
the call forms found in agent history ([feasibility.md](feasibility.md)
"Call forms").

## Roll back to mcporter

MCParcel never writes mcporter's configuration or its token cache, so mcporter
works as it did before. No re-import or sign-in is needed on the mcporter side.

### One connection

1. Remove the connection from the "use mcparcel for" list in the agent
   instruction, so it falls under "mcporter for the rest".
2. Optional: `mcparcel disable <mcp>`.
3. Check it with `mcporter list <server> --schema`.

### All connections

1. Run `mcparcel runtime stop`.
2. Point your agent instructions (for example the global `AGENTS.md`) back at
   mcporter.
3. Optional: run `mcparcel auth logout <mcp>` for each OAuth connection to remove
   MCParcel's Keychain item. It does not revoke the token at the provider yet.
4. Optional: `npm uninstall -g mcparcel`.
