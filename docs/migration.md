# Installing, upgrading and rolling back MCParcel

Version `0.1.0`. MCParcel is installed with Homebrew or from a GitHub release
archive; it is not published to npm. On macOS it runs on Apple Silicon only;
on Linux it is a static binary for amd64 and arm64 (see
[runtime.md](runtime.md#linux-desktop-mode) and [headless.md](headless.md)). Moving from mcporter
happens one connection at a time: see [Agent instructions during the mixed period](#agent-instructions-during-the-mixed-period).

## Install

With Homebrew, on macOS (Apple Silicon) or Linux:

```sh
brew install dedene/tap/mcparcel
mcparcel doctor
```

Without Homebrew, download `mcparcel_<version>_<os>_<arch>.tar.gz` from
[GitHub Releases](https://github.com/dedene/mcparcel/releases), check it
against `checksums.txt` and put `mcparcel` on your PATH.

From a checkout you can also build an npm tarball (needs Go and Node.js 20 or
newer): `make npm-pack` writes `dist/mcparcel-<version>.tgz`, which
`npm install -g` installs.

`mcparcel doctor` is offline. It does not start the runtime, read the Keychain or
1Password, or write anything. It exits 0 when no check failed and 8 when at least
one did; every failed row has a next action. `mcparcel version` prints
`mcparcel 0.1.0 (<commit>, <date>)` for a release; an npm tarball built from a
checkout prints `<version>+<commit>`, and adds `.dirty.<UTC seconds>` when the
checkout has uncommitted changes.

The macOS binary is ad-hoc signed. It is not Developer ID signed or notarized.

## Upgrade

1. Install the new version: `brew upgrade mcparcel`, or replace the binary
   with the one from the new release archive.
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
cache, or `brew cleanup`, does not affect a running daemon. A new version's copy replaces all older
copies except the most recent one, which stays for a rollback.

If `add` or `sync` reports `catalog_requires_upgrade`, the team catalog needs a
newer MCParcel than the one you run: upgrade first. Your saved snapshot of that
catalog stays active in the meantime.

## Roll back to a previous MCParcel

Put the older binary back (from its GitHub release archive), then run
`mcparcel runtime restart` when no calls are active. Your configuration, selections and stored sign-ins are kept.

Rolling back to a build from before the first release candidate (`0.1.0-rc.1`)
is not supported: those binaries do not know the catalog `minVersion` field and reject any catalog that
has it. If a saved catalog asks for a newer MCParcel than the one you rolled back
to, it keeps working, and `doctor` reports it under `version.catalog`.

The `service-account` credential profile mode is in `0.1.0`. It was added
after the `0.1.0-rc.1` release candidate, so a `0.1.0-rc.1+<commit>` build
(`mcparcel --version`) may or may not know the mode. If `config.json` has a `service-account` profile, an MCParcel without
the mode rejects the whole file: every command that reads the configuration fails with
`invalid_config`. Before you install an older version, remove those profiles
from `config.json` (and bind their connections to another profile), or keep a
copy of `config.json` without them to switch to.

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
When every connection has switched, the instruction becomes "Use mcparcel for
all MCP calls."

## Syntax changes from mcporter

| What | mcporter | mcparcel |
| --- | --- | --- |
| Tool schemas | `mcporter list <server> --schema` | `mcparcel tools <mcp>` (live; `--json` gives `data.items[]` with `name`, `description`, `inputSchema`) |
| Enabled servers | `mcporter list` | `mcparcel list` (offline, enabled only); `mcparcel catalog` for everything |
| Call | `mcporter call <server>.<tool> key=value` | unchanged; `key:value` also works, values are typed by the tool schema, `key:=json` forces JSON |
| Whole-object arguments | `--args '<json>'` | unchanged; also `--args-file <path>` or `--args-file -` |
| JSON output | `--output json` | `--json`: one envelope `{schemaVersion, ok, data, error}`, the MCP result in `data.result` |
| Errors | exit status and text | exit codes 0–8 and 130, branch on `error.code` ([cli.md](cli.md#output-and-errors)) |
| Sign-in | handled by mcporter | `mcparcel auth <mcp>`; a call never opens a browser, it fails `auth_required` (exit 3) |
| Images | — | `--output-dir <dir>` saves image and audio blocks |
| Function-call syntax, positional arguments | `server.tool(a: 1)` | not supported: name every argument |
| Ad-hoc servers | `--stdio`, `--http-url` | not supported: `mcparcel local add --file <definition.json>` |
| Import | — | `mcparcel import mcporter --file <path>` previews; `--apply` writes personal definitions, never the source file |

Sources: [cli.md](cli.md) "Arguments and identity" and "Deferred surface".

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
