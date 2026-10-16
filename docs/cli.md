# MCParcel CLI contract

Review draft, 4 October 2026. Commands below are proposed, not implemented.
This revision specifies the earlier CLI sketch; [catalog.md](catalog.md) and
[runtime.md](runtime.md) define configuration and execution behaviour.
All commands, flags, help, messages, errors and terminal UI are English.

## Start and daily use

```sh
npx mcparcel join acme/mcp-catalog
npx mcparcel list
npx mcparcel tools paper --json
npx mcparcel call paper.<tool> --args-file request.json --json
```

`join` is the one line for a new teammate: it registers the catalog as `add`
does, then walks through the 1Password account the catalog suggests, its
presets, then the setup list (see "Joining a catalog").
`add` registers a GitHub catalog and validates metadata. It does not enable entries,
start servers or read 1Password. `add` followed by `setup` is the manual path,
and `setup` remains useful whenever selections change.
1Password authentication is implicit on the first call that needs it; there is no
mandatory `unlock` command. OAuth sign-in is explicit in this release: a call
without a stored session returns `auth_required` with next action
`mcparcel auth <mcp>` instead of opening a browser. Private catalog access through `gh` is independent of 1Password.

Catalog README agent prompt:

> Add acme/mcp-catalog as an MCParcel catalog. Show its domains and
> connections, ask which I need, and enable my selection. Preserve my personal
> connections. Use MCParcel for subsequent MCP calls.

The initial add trusts the explicitly named source for later selected execution.
Print repository, file and revision. Changes are applied only by explicit sync.
A team can publish a single shell line, `mcparcel join <owner/repo>` (or
`add ... && setup`); agents can use `catalog` and `enable` without driving a
terminal UI.

## Command surface

The short `mcparcel` form assumes the native executable is available; all commands
also work via `npx mcparcel ...`. No global installation or Homebrew is required.

| Command | Contract |
| --- | --- |
| `add <owner/repo> [--path <file>] [--ref <ref>]` | Validate and register a source, idempotently |
| `join <owner/repo> [--path <file>] [--ref <ref>]` | Interactive only (no `--json`). Register the source exactly as `add` does, or reuse it when it is already registered (no fetch; `sync` updates it), print `Added <owner/repo> (<commit>).` or `<owner/repo> is already added (<commit>). Run mcparcel sync to update it.` on stdout, then open setup on that catalog's join screens (see "Joining a catalog"); prints setup's summary on exit |
| `remove <owner/repo>` | Remove local source registration; retained selections become unavailable; no remote write |
| `catalog [--domain <id>]` | Available metadata including disabled/missing-input entries |
| `list` | Enabled selections; clearly mark unavailable entries; no live connection |
| `inspect <mcp>` | Effective non-secret config, origin, requirements, blockers and revision |
| `enable <mcp>...` / `disable <mcp>...` | Atomically update selection for supplied IDs; `enable` links a credential profile requirement to the local profile with the same ID (see below) |
| `tools <mcp> [--cached]` | Live schema discovery; `--cached` has no cache yet and always returns `schema_cache_miss` |
| `tools enable <mcp> <tool>...` / `tools disable <mcp> <tool>...` | Change personal tool selection; cannot override source policy |
| `call <mcp>.<tool> [key=value ...] [--args <json>] [--meta <json>]` | Invoke an enabled, allowed tool; a server's approval request during the call is shown as a prompt (see Approval prompts). The CLI checks the connection and tool policy offline first: a denied tool (`tool_denied`, exit 4; `deny` wins over `allow`) or a disabled, unreviewed, unknown or ambiguous connection fails before the runtime is contacted, so no daemon starts, no connection opens and no token is requested. The runtime checks again at admission and before dispatch |
| `setup` | Interactive domain and connection editor (see "Domain matrix and terminal sketch"); renders on stderr and writes each change as it is made. On exit it prints one line on stdout: `9 on, all ready.`, or `9 on: 6 ready, GlitchTip needs a sign-in, Maps could not start, Rails Blocks needs rails-blocks-mcp installed.` (each server that is on with a step left, by label, as its Status reads), or `Nothing is on.`; then, indented, what to run for the steps setup could not take: `  mcparcel setup, then s signs you in` once for the sign-ins `s` can do, `  mcparcel auth <mcp>` per sign-in setup cannot do here (headless mode, or no browser sign-in), named as `auth` next actions are, and `  mcparcel doctor` when a server could not start. `The last change could not be confirmed. Run mcparcel list to check it.` comes first after a change that failed part way. Ctrl+C exits 130 after printing an unconfirmed change, without the summary |
| `sync [<owner/repo>] [--apply [--accept <mcp>...]]` | Fetch and display update; only `--apply` changes active snapshot; `--accept` unblocks named connections whose execution or auth changed |
| `import mcporter --file <path> [--bindings <file>] [--only <id>...] [--apply]` | Preview or apply supported imports, with explicit unresolved-field report; unbound `${NAME}` in env/header values becomes `env:NAME` with an `environment_reference` warning |
| `local add --file <definition.json>` | Add a connection object containing `id` plus catalog connection fields. `domains` declares each ID it names; `other` is reserved (omit `domains` instead). An invalid file fails `invalid_config` with `Invalid definition: <field path>: <reason>.`; any other `invalid_config` with a field error reads `Invalid configuration: <field path>: <reason>.` |
| `local update <id> --file <definition.json>` | Replace that personal connection atomically; matching ID required |
| `local remove <id>` | Remove personal definition and turn the connection off (its inputs, profile and tool choices stay for when it comes back); never mutate a team catalog |
| `config validate --file <catalog.json>` | Validate schema and references offline |
| `config input set <mcp> <name> <value>` | Set a declared non-secret local input |
| `config profile set <name> --file <profile.json>` | Save a validated credential profile containing references only, for example `{"mode":"service-account","tokenFile":"/run/secrets/op-token"}` or `{"mode":"desktop-service-account","account":"Example account","bootstrapRef":"op://Private/mcparcel-bootstrap/token"}`; a `service-account` profile names where its token is (`tokenEnv` or `tokenFile`), never the token ([catalog.md](catalog.md#local-state)) |
| `config profile bind <mcp> <profile>` | Bind the connection's declared credential requirement |
| `auth <mcp>` | Make the connection's credentials fresh, through the runtime (it starts it if needed), in this order: (1) a connection with `op://` references (env, headers, OAuth client ID or secret) drops their cached values and reads them again through 1Password now, so a prompt or error shows here and not on the next call; (2) an OAuth authorization-code connection (marked OAuth, or an HTTP connection without a configured credential header) signs in again in the browser; (3) a `client_credentials` connection drops its pooled token and gets a new one now (the token stays in daemon memory only). A connection with both, for example OAuth with an `op://` client secret, reads 1Password first. Sign-in prints the URL on stderr and opens the browser (macOS: `/usr/bin/open`; Linux: `xdg-open`, only when `DISPLAY` or `WAYLAND_DISPLAY` is set; where no browser can open, it prints only the URL to open, and over SSH also the `ssh -N -L <port>:127.0.0.1:<port> <this host>` forward that lets a browser elsewhere reach the loopback callback); before any browser opens, the runtime reads the stored sign-in, waiting up to 2 minutes for a keyring unlock prompt, and a keyring failure other than a missing item refuses it with `keychain_unavailable`. JSON `{connection, secretsRefreshed?, signedIn?, tokenRenewed?}`: a key is present only when its step applies and is `true` when it ran; `signedIn` is `false` when the server never asked for sign-in (nothing stored). Human output: one line per step (`Read the 1Password secrets for <mcp> again.`, `Signed in to <mcp>.` or `<mcp> did not ask for sign-in. Nothing was stored.`, `Got a new access token for <mcp>.`). A re-read holds no connection gate: an in-flight call keeps the values it started with; the next call reconnects only when a value changed, and a failed re-read stops the pooled process once that call ends. A `service-account` profile with an active session reads through that session and does not read its token file or variable again; without one (a new runtime, an expired session, or after `auth lock`), it bootstraps from the token now, as a call would. If the 1Password step succeeds and a later step fails, only the error is returned and the values stay refreshed. Refused offline, without starting the runtime: a connection with only literal values and `env:` references (`invalid_arguments`, "<mcp> has no sign-in, client credentials or 1Password secrets to refresh.", plus "Its env: values are read when the runtime starts." and next action `mcparcel runtime restart` when it uses `env:` references); a sign-in in headless mode (`auth_required`, "This server needs sign-in, which headless mode cannot do."); a sign-in with `--no-input` (`auth_required`, "Signing in to <mcp> opens a browser, which --no-input does not allow."); and, on Linux when no runtime runs and this session has no usable D-Bus session bus, a sign-in (`keychain_unavailable`, "No Secret Service keyring is reachable from this session (no usable D-Bus session bus), so MCParcel cannot store a sign-in."). The keyring checks apply to a sign-in only. `--no-input` re-reads `op://` values for a `desktop-service-account` profile with an active session and for a `service-account` profile (which bootstraps from its token without a prompt, also in headless mode); a `desktop` profile may prompt on any read, so it is refused before anything is dropped (`auth_required`, "Reading <mcp>'s 1Password secrets again may need approval in the 1Password app, which --no-input does not allow."). A `desktop-service-account` profile without a session gives `auth_required` as a call does. On Linux desktop a desktop-app profile gives `config_required`, also under `--no-input`. `client_credentials` works with `--no-input` and in headless mode. A connection named `status`, `logout` or `lock` is shadowed by that subcommand; it is reached by its canonical ID, for example `mcparcel auth local:status`, and MCParcel's next actions use that form (doctor row `credentials.auth-name`) |
| `auth status [<mcp>]` | Offline, never starts the runtime (it asks a running daemon for its 1Password sessions): per HTTP connection `{connection, state, signedIn, refreshToken, lastRefreshAt?, accessTokenExpiresAt?, refreshTokenExpiresAt?, lastRefreshFailure?: {at, code}, keepAlive, cause?: {code, message}, previousCause?: {code, message}, nextAction?}` in `items`, plus `events` (the health log) for `auth status <mcp>`; `state` is `ok`, `expiring` or `sign-in required`; causes are listed in runtime.md "Health log and `auth status` as built". `previousCause` is set for a signed-in session whose last sign-in followed a terminal failure and says why that sign-in was needed. `keepAlive` is `off` when the connection turned it off, else `unavailable` when its client secret is a 1Password reference, else its keep-alive interval (`24h` by default). Human output: `<connection>  <state>  (<cause>)`, and for one connection the cause message, `Last sign-in needed: <message>`, last refresh, expiries, keep-alive and next action. No token, client identifier or redirect. Without `<mcp>`: enabled HTTP connections marked OAuth, holding a sign-in, or whose server asked for sign-in (an unmarked connection whose call or sign-in got `auth_required`; shown as `sign-in required`). `profiles` lists the local credential profiles an enabled connection uses (for `auth status <mcp>`: that connection's profile when it has `op://` references) as `{profile, mode, session, sessionExpiresAt?, tokenEnv?, tokenFile?, connections}`; `session` is `active`, `expired`, `none` (no session, or the runtime is not running) or `unknown` (the daemon could not be asked or runs another version); `tokenEnv` or `tokenFile` is set for a `service-account` profile and says where its token comes from; human output `profile <id>  <mode>  <session>[  until <RFC3339>][  token from <NAME> | token from file <path>]`. Never the account, a reference or a token. When listing, a connection that is not marked OAuth and whose item the keyring cannot read is left out; a marked connection, or `auth status <mcp>`, fails `keychain_unavailable`. A connection with neither sign-in nor `op://` references gives `invalid_arguments` `client_credentials` connections are left out in every mode (their token lives in daemon memory only); naming one is `invalid_arguments`. Headless mode reads no Keychain: `items` is empty (human: "No sign-ins in headless mode.", followed by the profile lines); `profiles` is listed as on desktop and is `[]`, never `null`, when there are none |
| `auth lock` | Through the runtime (starts it if needed): end every 1Password session, cancel protected work (dispatched calls report `outcome_unknown`, queued ones `auth_required`), stop protected processes and bar every OAuth connection from reusing its stored session until its next sign-in with `auth <mcp>` (cause `locked` in `auth status`). Service-account profiles are not barred: their next call, also with `--no-input` and in headless mode, bootstraps again from the token without a prompt. To stop one, remove it, bind its connections to another profile or disable them, or revoke the token in 1Password. Secret-free connections keep running. JSON `{locked}` |
| `auth logout <mcp>` | Remove the stored sign-in (macOS Keychain, Linux Secret Service) through the runtime (starts it if needed); JSON `{connection, removed, providerRevoked}`; `providerRevoked` is always false for now. A `client_credentials` connection is refused offline (`invalid_arguments`, "<mcp> uses client credentials; there is nothing to sign in to.") |
| `doctor [<mcp>] [--live]` | Offline checks of the runtime mode and version, local storage, configuration documents, connections, credential mapping and prerequisites, one row each (see Doctor). With `<mcp>`, connection rows cover that connection only. `--live` needs `<mcp>` and lists its tools through the runtime. Exit 8 when a row fails |
| `runtime status` / `runtime restart [--force]` / `runtime stop [--force]` | Inspect, restart or stop the daemon; status includes `executable` (the file the daemon runs from: on desktop its retained copy `<data>/runtime/<version>/mcparcel`, human `Executable:`), `stayAlive` (human: `Stay-alive: on` or `off`), true when `runtime.keepAlive` is set and an OAuth session may still need refreshing, and `credentialSessions` (`[{profile, mode, state, expiresAt}]`, omitted when there are none; no account or reference); restart and stop refuse active calls unless forced; restart recaptures the login environment and drops pooled sessions, so changed `env:` values apply; stop on a stopped runtime succeeds. Headless mode: status shows `Environment: daemon environment`, and restart starts the new daemon with the forwarded variables of the caller's own environment (no login shell). Against `runtime serve`, restart (also `--force`) is refused with `runtime_supervised` |
| `runtime serve` | Run the runtime in the foreground under a supervisor: takes the daemon lock itself (`runtime_busy`, exit 6, when another runtime holds it), logs to the daemon log and stderr, never exits when idle; SIGTERM or SIGINT ends dispatched calls as `outcome_unknown`, closes sessions, removes the socket and exits 0 (human `Runtime stopped.`, JSON `{"stopped":true}`). It marks the runtime directory supervised: other CLIs wait up to 15 s for it instead of auto-starting a daemon, then fail `runtime_supervised`. `runtime.supervised: true` in `config.json` (headless only) has the same effect before serve first ran, e.g. on a fresh pod. On Linux in desktop mode it is refused like the other runtime commands when there is neither a desktop session nor a `config.json` |
| `version` / `--version` / `--help` | Version (`--version` prints the same line as `version`, before any command runs) and English usage |

All commands provide `--json` except interactive `setup`; use selection/config
commands for equivalent machine actions. `--no-input` never opens UI, browser,
biometric or approval prompts (terminal or dialog). Missing necessary input yields an error with the next action.
Headless mode (`runtime.mode: "headless"`) behaves as if every runtime command
had `--no-input`: no terminal or dialog prompt, no browser, also with a
terminal attached and `runtime.approvalDialog` set; a server's approval request
is declined at once with the `elicitation_declined` warning.
In headless mode every command that writes configuration (`enable`, `disable`,
`tools enable|disable`, `local`, `config input|profile`, `import --apply`,
`add`, `join`, `remove`, `sync` with or without `--apply`) fails `config_read_only`
before it takes a lock or sends a request. On Linux, `tools`, `call`, `auth`
and `runtime` in desktop mode fail `runtime_unsupported` ("No MCParcel
configuration was found and there is no desktop session.") when there is no
display, no usable D-Bus session bus and no `config.json`; with a
`config.json` they run. Offline commands always work. See
[runtime.md](runtime.md#linux-desktop-mode) and [headless.md](headless.md).
`setup --no-input`, `setup --json` and setup without a TTY (stdin and stderr must
be terminals, in the foreground) fail with `terminal_required` (exit 2) before
reading any configuration, and write nothing. `join` checks the same before it
reads the configuration or fetches the catalog ("Join needs an interactive
terminal; it does not run with --json, --no-input or without a terminal.",
next action `Run mcparcel add <owner/repo>, then mcparcel setup in a
terminal, or mcparcel enable <mcp>.`).

`local` file updates can also change personal domain assignments; the setup
server page edits the same fields and input/profile bindings. Setup never shows argument,
env or header values or a URL's path and query; replace them by typing a new
value in the personal form, or use `local update`. Setup does not author team
catalogs or edit protected policy.

`enable` validates all specified entries and local prerequisites before saving;
missing executable/auth availability is a reported runtime prerequisite, not a
reason to resolve secrets. When a connection declares a credential profile
requirement and its selection has no profile, or names one that no longer exists,
enable links it to the local profile with the same ID if there is one. It never
replaces a valid binding and never picks a profile with another ID. Human output
adds `Linked credential profile <profile> to <mcp>.` per linked connection after
`Configuration saved at revision N.` (the profile ID, never its account); JSON
adds `linkedProfiles` (`{"<canonical id>": "<profile>"}`) next to `revision`,
omitted when nothing was linked. Missing declared inputs/profile mappings still
block enable with `config_required`, and then nothing is linked or saved. Bulk
enable is all-or-nothing. Tool names may be configured
before live discovery; unknown names are marked unverified until schemas are loaded.

## Arguments and identity

- Short names resolve through explicit aliases first, otherwise unique matches.
  Ambiguity fails with candidates. Qualified ID: `github:owner/repo#paper` or
  `local:paper`. Aliases preserved by import cannot be hijacked by adding a source.
- Split `call` at the first dot in a short alias, after `local:` in a personal ID,
  or after `#` in a GitHub ID. Remaining text is the exact tool name. Repository
  names and tool names can contain dots; connection IDs themselves cannot.
- `key=value` and `key:value` are equivalent. The key ends at the first `=` or `:`.
  The value is coerced by the tool's input schema, not guessed from its looks:
  a property typed `string` stays a string (`id=007` is `"007"`); `integer`,
  `number`, `boolean`, `array` and `object` properties are parsed as JSON, so
  `limit=5` and `queries='[{"q":"x"}]'` arrive typed. A value that does not parse
  as the declared type fails with exit 2 and names the expected type. The type is
  looked up through local `$ref`s (`#/$defs/...`, `#/definitions/...`, also a
  `$ref` at the schema root), one-item `allOf` wrappers and nullable unions
  (`type: [T, "null"]`, or `anyOf`/`oneOf` of T and `null`): such a property
  coerces as T, and the literal `null` becomes JSON null unless T is `string`. A
  property with no declared type, any other union or no schema entry is sent as
  a string; use `key:=json` for those.
- The coerced arguments are then validated against the tool's input schema
  (draft-07 or 2020-12) before the call is sent. A mismatch fails with
  `invalid_arguments` (exit 2) and nothing is dispatched; the message names the
  schema path and keyword (`... at /properties/limit (minimum).`, or the missing
  required names), never the value. Remote `$ref`s are never fetched. When the
  schema cannot be checked locally (another draft, a remote, dynamic or
  unresolvable ref, a cyclic or oversized schema, or arguments too large or deep
  to check within the validation work budget), the call is sent unvalidated with
  a `schema_unchecked` warning and the server checks the arguments.
- `key:=json` always parses the value as JSON, whatever the schema says.
- `--args <json>` takes a JSON object inline; `--args-file path` reads one from a
  file and `--args-file -` from stdin. The three are mutually exclusive with each
  other and with assignments. Values are payloads, not shell expressions.
- Duplicate keys fail. No dot-path nesting and no function-call syntax in v1.
- Coercion needs the tool's schema. `call` reads the tool's schema live from the
  connection before every call.
- `--timeout 120s` changes the call deadline. Cancellation/timeout never replays it.
  Time spent on an open approval prompt does not count toward it.
- `--meta '<json object>'` sends the object as the `_meta` of that `tools/call`
  (for example Codex's `x-codex-turn-metadata`). At most 64 KiB as sent (compact JSON with `<`, `>` and `&`
  escaped as `<` and so on), one JSON object,
  no duplicate keys. Keys the SDK or the MCP specification reserve are rejected:
  `progressToken` and any prefix whose second label is `modelcontextprotocol` or
  `mcp` (such as `io.modelcontextprotocol/`). Any of these fail with
  `invalid_arguments` (exit 2) before the runtime is contacted. Given once only.
- `--output-dir <path>` saves the result's top-level `image` and `audio` content
  blocks as files in that directory. It must name an existing directory, given
  once; otherwise `call` fails with `invalid_arguments` (exit 2) before reading
  input or contacting the runtime. Each file is new: named
  `mcparcel-<UTC stamp>-<6 hex>-<index>.<ext>` (`index` is the block's position
  in `content`; the extension comes from a fixed `mimeType` table, else `.bin`),
  mode 0600, created exclusively, so an existing file or symlink is never
  followed or overwritten and no server text reaches the name. Resource blobs,
  nested blocks and other types are not saved; more than 256 image/audio blocks
  saves nothing. Every block is base64-decoded before the first file is
  created, so invalid data writes nothing. `data.artifacts` lists what was
  written as `{index,type,mimeType,path,bytes}` with an absolute `path`; it is
  absent when nothing was written. `--json` still carries the complete result,
  base64 included. If saving fails after a successful call, `call` exits 1 with
  `export_failed`, keeping `data.result` and any `artifacts` already written; do
  not call the tool again just to export. When the call itself failed with a
  result (`tool_error`, `input_required`), the blocks are still saved and an
  export failure is a `warnings` entry instead. Without `--output-dir`, nothing
  from a result is ever written to disk.

## Approval prompts

A server can ask its user something in the middle of a call through MCP form
elicitation (Codex computer use: `Allow Computer Use to use "<App>"?`). `call`
shows that request only when stdin and stderr are both terminals, the CLI is in
the terminal's foreground process group, and neither `--json` nor `--no-input` is
given. The prompt goes to stderr and names the connection from the `call` target
as the asker, then the server's message, `Note:` (subtitle), `Risk:` and
`Details:`, each cleaned of control, escape, bidi and blank padding characters and
capped. `Details:` shows the server's parameter list as `Name: value` items
joined by `; ` (`Details: App: TextEdit`); any other shape is shown as text or
compact JSON. Input typed before the prompt appears is discarded. An approval
offers `1) Decline (default)`, `2) Allow once`, and `3) Allow for this session`
only when the server offers it; an absent number is invalid input, and three
invalid entries decline. There is no lasting approval, even when the server lists
`always`: the server does not store an approval it receives (Codex computer use
only uses it for statistics; the Codex app writes lasting approvals). To approve
an app for good, approve it in the server's own app. A form with flat string,
number, integer, boolean or string-enum fields offers `1) Decline (default) 2) Answer`,
asks each field (a required one up to three times, then declines) and ends with
`Send? [y/N]`. Anything else (nested objects, other schemas, URL mode) is declined
without asking. Enter declines; Ctrl-D or the 5-minute prompt timeout cancel the
request only. Ctrl-C or a closed terminal cancels the whole call, as at any other
moment: the request is answered `cancel`, the call ends (exit 130 on Ctrl-C), and
the daemon retires that connection's session, so a stateful server (Codex
`cua_repl`) loses its REPL state. Time spent on an open prompt does not count toward `--timeout`
or `callTimeout`. One prompt is open at a time per call. There is no flag,
variable or setting that accepts for you.

Without a terminal (or with `--json`), `runtime.approvalDialog: true` in
`config.json` (default `false`) shows the approval as a native macOS dialog
instead; `--no-input` never does. The dialog has the same choices as buttons:
Decline (default), Allow once, and Allow for this session when offered. It gives
up after the same 5 minutes (cancel) and shows approvals only; forms are declined.

Residual risk: MCParcel cannot prove that a human answered. An agent whose shell
tool allocates a pseudo-terminal passes the terminal check and can type `2` or
`3` itself, approving its own request; a computer-use or accessibility agent can
click the dialog. The daemon trusts the prompt mode the
CLI declares, so any process running as the same user can speak the socket
protocol and answer its own prompt. That is why the dialog is opt-in.
`--no-input` binds only a cooperating caller; an agent that must not grant
approvals needs OS-level isolation (another user, or a sandbox that denies the
socket).

Headless mode (`runtime.mode: "headless"`) never prompts: no terminal prompt
even with stdin and stderr on a terminal, no dialog even with
`runtime.approvalDialog: true`. The request is declined at once with the "no
prompt was possible" notice.

## Doctor

`mcparcel doctor [<mcp>] [--live] [--json] [--no-input]` checks what the
other commands need and reports one row per check. It is offline by default:
it never starts or waits for the runtime, runs no login shell, reads no
Keychain, 1Password, `op` or browser state, makes no network request and writes
nothing (no lock file, no directory). It may ask a runtime that is already
running for its version and status, as `runtime status` does. It works in
desktop and headless mode, also on Linux in desktop mode without a desktop
session and without `config.json`, where it reports `runtime.mode` as failed.

`<mcp>` resolves like `inspect`: an unknown ID fails `connection_unavailable`
(exit 4), an ambiguous one `ambiguous_id` (exit 2), before any row. When the
configuration cannot be read, doctor still runs the global checks and emits no
connection rows. `--live` without `<mcp>` fails `invalid_arguments` (exit 2)
before any file is read.

`--live` adds one `live.tools` row: it connects to the connection and lists its
tools, exactly like `tools <mcp>` (on desktop it starts the runtime if needed).
It is skipped when `runtime.version` failed or the runtime is still starting,
and fails without contacting the runtime when the connection cannot be called,
with the code `tools <mcp>` would return: `config.connection`'s code, or
`connection_disabled` for a disabled connection (whose `config.connection` row
is a skip without a code). In headless mode it never starts a runtime: with no
runtime running and no supervisor, or when the runtime exits between the offline
probe and the live call, it fails with "doctor --live does not start a runtime
in headless mode: it would run with this shell's environment, not the
wrapper's." Interrupting doctor exits 130 (`canceled`), never 8. With `--no-input` (always in headless mode) it never prompts: a
1Password approval or a missing OAuth sign-in gives `auth_required` on the row.
Without it, the 1Password app may ask for approval as it does for `tools`, so
agents pass `--no-input`. The live row reflects the configuration the runtime
reads at that moment, which can be newer than the offline rows when another
command saved in between.

Rows, in this order: the global rows below, then the connection rows (sorted by
connection ID, then in the order below), then `live.tools`. Row IDs are stable.
Each row has `id`, `subject` (connection ID, document, source or storage name;
omitted for most global rows), `status` (`ok`, `warn`, `fail`, `skip`), `message`
(one English line), and optionally `nextAction` and `code` (the error code the
failing command would give).

| Row | Checks |
| --- | --- |
| `runtime.mode` | Desktop, or headless with its state root (`Supervised.` when a supervisor owns the runtime). Fails on Linux in desktop mode without a desktop session (display or usable D-Bus session bus) and without `config.json` (`runtime_unsupported`), and when `config.json` cannot be read (`invalid_config`, `unsafe_local_path`); the other `runtime.*` and `storage.*` rows are then skipped |
| `runtime.version` | Not running (ok; a warning when supervised), a stale socket (ok, the next start removes it), starting or not answering within 2 s (warn), running (ok, version and PID), another version (fail `runtime_version_mismatch` naming both versions and the daemon PID; next action `runtime restart`, or the supervisor), another configuration directory (fail `runtime_config_mismatch`), or a handshake it cannot read (fail; stop it with the version that started it) |
| `runtime.binary` | Desktop: the running runtime runs from its retained copy `<data>/runtime/<version>/mcparcel` (ok), from a file that is gone (warn: restart it), or from elsewhere because the copy failed (warn). When stopped: whether the copy exists or the next start makes it. Skipped in headless mode, under a supervisor, and when `runtime.version` reports a problem |
| `storage.dir` | `state`, `data`, `runtime`: private (mode 700, owned by you, no symlinks), absent (created on first use) or unsafe (`unsafe_local_path`). The message names the path |
| `config.file` | `config.json`, `personal.json`, `selections.json`, then `catalog <source-id>` per registered source, each decoded on its own: valid, not present, invalid (`<field path>: <reason>`, `invalid_config`), unsafe, or a missing active snapshot. All are read under one shared configuration lock; when another command holds it for 5 s, one row with subject `-` fails `config_conflict` and the configuration rows are skipped |
| `config.state` | The documents agree with each other, or the cross-document error (`invalid_config`) |
| `version.catalog` | Per registered source: the catalog's `minVersion` against this version; fails `catalog_requires_upgrade` when the catalog needs a newer MCParcel; skipped for development builds |
| `config.summary` | Enabled, disabled, review-required and unavailable counts; a warning when none is enabled |
| `prereq.onepassword` | Desktop on macOS, when an enabled connection with an `op://` reference is bound to a `desktop` or `desktop-service-account` profile: whether `1Password.app` exists in `/Applications` or `~/Applications` (it does not check that the integration is on). Service-account profiles need no app |
| `prereq.keyring` | Linux desktop, when an enabled HTTP connection without `client_credentials` exists: whether a usable D-Bus session bus is reachable (ok; it does not check that a Secret Service provider runs or is unlocked). Without one it fails `keychain_unavailable` when a connection is marked OAuth, else warns |
| `config.connection` | One per enabled connection, or the named one: ready; review required (warn); unavailable (warn); missing inputs or profile (fail `config_required`, naming them); not callable by the runtime (fail `runtime_unsupported`, or the headless `op://` error). A named disabled connection is one skipped row |
| `credentials.profile` | The bound credential profile and its mode; never the account. On Linux in desktop mode, a `desktop` or `desktop-service-account` profile fails `config_required` ("Profile ID uses the 1Password desktop app, which MCParcel supports on macOS only.") |
| `credentials.token` | Both modes, for a connection with `op://` references bound to a `service-account` profile. `tokenEnv`: whether this shell has the variable (name only; a miss is a warning, because the runtime reads its login-shell environment on desktop and its own environment in headless mode). `tokenFile`: the file exists, is safe and is not empty (ok); missing, unreadable or empty (fail `config_required`); unsafe (fail `unsafe_local_path`). It opens the file without reading the token |
| `credentials.reference` | Desktop, and headless for a connection bound to a `service-account` profile: whether every `op://` reference follows the character rule ([catalog.md](catalog.md)); a failure names where (`env NAME`, `header NAME`, `auth clientId`, `auth clientSecret`, `profile bootstrapRef`), never the reference |
| `credentials.env` | Headless: whether this shell has every `env:` variable (names only; the runtime reads its own environment, so a miss is a warning) |
| `credentials.oauth` | Headless: an OAuth connection that needs browser sign-in fails `auth_required` |
| `credentials.auth-name` | Both modes, for a connection with something to authenticate (`op://` references, OAuth or an HTTP transport without a credential header; in headless mode, which refuses every sign-in, only `client_credentials` connections and connections with `op://` references and no sign-in) whose name, or an alias of it, is an auth subcommand (`status`, `logout`, `lock`): a warning that `mcparcel auth <name>` runs that command; the next action names `mcparcel auth <canonical ID>` |
| `prereq.command` | Stdio: the command found on the connection's own literal `PATH`, else the running runtime's PATH, else this shell's (headless: this process's). A miss fails, except against this shell's PATH on desktop (warn: the login shell may have more). The next action names what to install. Remote connections are skipped |
| `live.tools` | `--live` only: the number of tools, or the error's code, message and next action |

No row shows a secret, an `op://` reference, a URL or host, an account, an
argument, or an env or header value. Rows show connection, source and document
IDs, field paths, env and header names, commands and local executable paths,
versions, PIDs and local state paths.

JSON `data` is `{version, mode, live, checks, summary: {ok, warn, fail, skip}}`;
`mode` is `desktop`, `headless` or `unknown`. With no failed row the envelope is
`ok:true` and exit 0 (warnings allowed). With one or more, `ok:false`, `data`
keeps the rows, `error` is `doctor_failed` ("N checks failed.", next action from
the first failed row) and the exit code is 8. Exit 2, 4 or 1 mean doctor could
not run. Human output is a plain ASCII table (`Status  Check  Subject  Message`,
a `next:` line under rows that have one, a summary line) on stdout; the
`doctor_failed` message goes to stderr.

## Output and errors

Human output goes to stdout; progress and prompts to stderr. JSON mode emits one
object and no ANSI or diagnostic lines on stdout. Top-level contract:

```json
{"schemaVersion":1,"ok":true,"data":{},"error":null}
```

Failure uses `ok:false`, `data:null` and `{code,message,nextAction?,details?}` as
`error`. MCP `isError` uses `ok:false` but retains the complete result in `data`.
For call output, `data` contains `connection`, `tool`, `result` (unmodified MCP
CallToolResult), optional `artifacts` and optional `warnings`: a list of
`{code,message,nextAction}` notices about the call that leave `result` untouched.
Human mode prints each warning's message and next action on stderr. Human call
output prints text blocks, `[image saved: <path>]` or `[audio saved: <path>]` for
exported blocks and `[<type>]` for any other block (`[content]` when the type is
not a short lowercase name); when there is no text block it prints
`structuredContent` as compact JSON. Server text is stripped of terminal escape
sequences, control characters other than newline and tab, and bidi controls. List output includes `items`, relevant
source revisions and cache age. Secret bindings remain references, never values.

| Exit | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Unexpected internal failure, or `export_failed` (the call succeeded but `--output-dir` could not save its blocks) |
| 2 | Usage, invalid config, missing input or ambiguous ID |
| 3 | Authentication required/denied/expired |
| 4 | Disabled/unavailable connection or denied tool |
| 5 | MCP tool returned an error result |
| 6 | Connection failure, timeout, unknown outcome or runtime mismatch |
| 7 | Concurrent config conflict |
| 8 | `doctor` found a failed check (`doctor_failed`; `data` keeps the rows) |
| 130 | User cancellation |

Error codes distinguish `auth_required`, `auth_expired`, `config_required`,
`config_conflict`, `connection_unavailable`, `review_required`, `tool_denied`,
`runtime_version_mismatch`, `runtime_config_mismatch`, `auth_account_conflict`,
`terminal_required` and `outcome_unknown`. `review_required` uses exit 4;
`auth_rate_limited` (exit 6) means 1Password refused one request; the session and
pooled process stay, and nothing is retried within the call;
`runtime_config_mismatch` exit 6; `auth_account_conflict` exit 3;
`terminal_required` exit 2 (setup or join without an interactive terminal, or
with `--json` or `--no-input`; the next action lists the equivalent commands);
`doctor_failed` exit 8 (doctor ran and at least one row failed; `data` keeps
the rows, as for `isError`).
Headless mode adds `config_read_only` (exit 2, "This configuration is
read-only (headless mode).", next action to change the configuration at its
source and restart the runtime) and `runtime_supervised` (exit 6, a
supervisor runs the runtime: `runtime restart` against `runtime serve`, or a
CLI that waited 15 s for a supervised runtime that did not answer). A missing
`env:` variable in headless mode is `config_required` with
`details.variables` (the missing names, never values). `tool_denied` keeps
exit 4 and `error.code` is the discriminator; `call` returns it before the
runtime is contacted. A `client_credentials` token-endpoint refusal is
`auth_failed` naming the OAuth error code (`invalid_client`); 5xx, 429,
timeouts and network errors are `connection_failed`.
`server_error` (exit 6) means the server answered the call with a JSON-RPC error
instead of a result: the message carries the server's text cleaned to one line of
at most 300 characters (never the error's `data`), and `details` has
`dispatched`, `requestId` and `rpcCode` (the JSON-RPC code; 0 is a legal code) but
no `outcome`, since the server reported the failure. `result_too_large` (exit 6)
means the tool's answer exceeded a size limit (16 MiB per MCP message) and was
not received; it has `dispatched` and `outcome:"unknown"`, because the tool may
have run. `export_failed` (exit 1) means the call finished but `--output-dir`
could not save its blocks; `data` keeps the result. A `protocol_error` after dispatch, such as a result that is not strict
JSON (duplicate keys, invalid UTF-8, nested deeper than 125 levels, a non-boolean
`isError`), also has `outcome:"unknown"`. Do not blindly retry any of these.
`elicitation_declined` (exit 3) means the server asked for input through MCP
elicitation and did not get an accept. The server's text is reduced to one line
of at most 300 characters without control characters. The wording says why:

| Reason | Message | Next action |
| --- | --- | --- |
| No prompt possible | `The server asked for approval and MCParcel declined it because no prompt was possible: <msg>` | `Run the call in a terminal without --no-input or --json, or set runtime.approvalDialog in config.json, or approve it in the server's own app, then retry.` |
| Cannot be shown | `The server asked for input MCParcel cannot show, so MCParcel declined it: <msg>` | `Approve this in the server's own app, then retry.` |
| You declined | `You declined the server's request: <msg>` | `Retry the call if you meant to allow it.` |
| Canceled | `The server's request was canceled without an answer: <msg>` | `Retry the call and answer the prompt.` |

Only the first non-accept of a call is reported; an accept adds nothing. It is
the error when the server then failed the call with a JSON-RPC error; when the
server still returned a result, that result is kept and the same notice is a
`warnings` entry (exit 0, or 5 for `isError`). OAuth adds `auth_failed` (provider refused; only a
sanitized OAuth error code is shown), `keychain_unavailable` (the macOS
Keychain or the Linux Secret Service could not read or store the sign-in, or it
is too large; on Linux also when no session bus is reachable, or "A keyring
prompt from an earlier MCParcel request is still open." while an unanswered
unlock or create dialog blocks the keyring) and `auth_callback_unavailable`
(fixed callback port in use), all exit 3. Catalog commands add `invalid_repository` (exit 2,
malformed `owner/repo` argument, nothing fetched), `invalid_catalog` (exit 2, fetched
content rejected), `catalog_unavailable` (exit 4, repository unreachable or not
registered) and `catalog_requires_upgrade` (exit 4, the fetched catalog's `minVersion`
is above this MCParcel; `add` saves nothing and `sync` keeps the saved snapshot). Human `sync` with no registered catalogs prints a hint to run
`mcparcel add <owner/repo>`. `runtime stop --json` returns
`{"stopped":true,"wasRunning":<bool>}` and never starts a daemon. Errors include request ID when execution began. Upstream
secret-bearing messages are sanitized. No automatic execution of corrective hints.

## Domain matrix and terminal sketch

A connection appearing in multiple tabs has one shared selection. For example,
context7 belongs to Development and Research without creating two processes.

```text
MCParcel  Acme · a54e28f · synced 2m ago                                                            6 on · 2 need a step
 Design 0/1  [Development 6/6]  Monitoring 1/1   Research 1/1   Other 0/0

    MCP                         Status                                      │ GlitchTip
  ● Chrome DevTools             starts on first use                         │ Errors and performance.
  ● Context7                    2 tools                                     │
> ◐ GlitchTip                   sign in                                     │ ◐ needs a sign-in
  ● Lighthouse                  starts on first use                         │
  ● Notion                      sign-in unknown                             │ ✓ On
  ✕ Rails Blocks                rails-blocks-mcp not installed              │ ◆ Sign in with your browser
                                                                            │ ○ Tools load after sign-in
                                                                            │
                                                                            │ Runs  remote · errors.example
                                                                            │ From  Acme · Monitoring, Development

2 servers need a step.  s set up all   → open GlitchTip
Space on/off   Tab domain   / search   u undo   ? help   q quit
```

This is setup at 120x30 in a UTF-8 terminal, with the cursor on GlitchTip;
blank list rows are trimmed. The catalog and its servers are illustrative.

The header shows `MCParcel`, then, when exactly one catalog is registered, its
name (or `owner/repo`), its 7-character commit and `synced Xm ago`; with none it
shows `personal`, with several `N catalogs + personal`. On the right it counts,
across all tabs, the servers that are on and how many of those need a step
(`5 on · 2 need a step`), or `5 on · all ready`. Below it are the domain
tabs, then the search line (blank until you search). The current tab reads
`[Label on/total]` (`[Label (n)]` with the match count while a search is
active); the tab bar keeps it whole, unless it alone is wider than the
screen, when its label is cut. It shows `‹` and `›` (`<` and `>` in ASCII)
when tabs are hidden on either side.

From 100x30 setup uses the wide layout: the list on the left and the details
pane on the right of a rule. Each list row starts with `> ` on the focused
row, then a glyph, MCP, Status and Source; where a row runs, and the command
or runner it starts (`npx`, `uvx`, `docker`, ...), is in the pane. MCP is as
wide as the longest label (14 to 28 columns) while Status keeps room for
`starts on first use`. Source (the catalog's name, else its `owner/repo`, or
`Personal`) is shown only when the connections come from two or more sources
(catalogs and Personal each count, across all tabs); otherwise Status gets
its width. The glyphs (ASCII in brackets):

| Glyph | State |
| --- | --- |
| `●` (`[x]`) | on, nothing to do |
| `◐` (`[~]`) | on, needs a step |
| `✕` (`[-]`) | on, but it cannot run |
| `○` (`[ ]`) | off |

Status is one short phrase: the first of these that applies.

| Status | Means |
| --- | --- |
| `review change` (amber) | on, changed upstream: Space accepts it |
| `unavailable` (red) | its source no longer defines it |
| `needs <input>` (amber) | an input to fill in |
| `link 1Password` (amber) | a credential profile to link that Space cannot link by itself |
| `<command> not installed` (red) | the command it runs is not found on the PATH its connection sets, else on this process's PATH (the runtime uses the login shell's PATH, which only it captures), or not at its absolute path; a PATH that comes from an input is not checked |
| `<reason>` (red) | it could not start this session (`could not start`, `timed out`, `1Password failed`, ...) |
| `run mcparcel auth <mcp>` (amber) | it needs a sign-in setup cannot do here (headless mode, or no browser sign-in) |
| `sign in` (violet) | its stored sign-in is missing or no longer usable, or its server asked for one |
| `N tools` (green) | it started this session and loaded N tools |
| `checking sign-in` (grey) | its stored sign-in is being read |
| `sign-in unknown` (grey) | its stored sign-in cannot be read without a prompt (a locked Linux keyring) |
| `starts on first use` (grey) | nothing is known to be left |

A row that is off shows only what would keep it from running, in grey
(`needs workspace`, `paper not installed`, ...), or nothing. The first four
(and a command not installed) wait on you; `s` skips them. Status reads the
state shown, what this session found, the stored sign-in of each server
that is on and signs in through a browser (as `auth status` explains it,
read once a session without a prompt, again after a sign-in; one still being
read when setup ends is read again for the summary) and whether each command
is found; never the runtime or the network. Tool counts are
from this session only: no tool list is kept on disk. A row that is off and
misses only a credential profile that Space links by itself (see
Exceptional states) shows nothing: one Space turns it on.

The details pane takes three eighths of the width, kept between 40 and 60
columns including the rule, and follows the cursor. It is the server page
in short: the label, the description (up to 3 lines), the state (`● ready ·
2 tools`, `◐ needs a sign-in`, `○ off · needs workspace`, ...), the To do,
then Runs and From (see "The server page"). Below come why a row is
unavailable, or why it could not start (`Could not start Maps.`, with its
next step).
Right or Enter opens the server's page.

`mcparcel setup` opens on the first server, in tab order, that needs a
step, and moves there again once the stored sign-ins are read, unless a key
was pressed. With nothing to do it opens on the first tab.

Under the list come a blank line, the bottom lines and the status line. When
a server that is on needs a step, the first bottom line says so, offers `s
set up all` when `s` takes a step (a sign-in it can do here, or a start that
failed) and `→ open <Label>` (`Right open` in ASCII) when the cursor is on a
server with a step: `2 servers need a step.  s set up all   → open
GlitchTip`. With neither it names the next step: `2 servers need a step.
Next: Rails Blocks, rails-blocks-mcp not installed.` The second line keeps
the main keys. Otherwise they are the two key
lines. While setting up runs they show its progress or the sign-in that
waits (see "Setting up").

Below 100 columns or 30 rows setup uses the compact list: glyph, MCP and
Status. Under the list come one summary line (what the focused row needs,
in a sentence),
the status line and one bottom line when a server needs a step (`2 need a
step. s set up all   → open GlitchTip   ? help`, `1 needs a step. Next: ...`,
as on the wide list). MCP is as wide as the longest label needs, and Status
gets the rest. In both layouts every column ends in two spaces,
so truncated text never runs into the next column. Below 40x10 setup shows
`Terminal too small (need 40x10). Resize, or press q.`

Help lines show each key as a chip followed by a dim label; without color they
read `Space on/off   Enter open`. The last hint of a line (`? help`, `← back` (`Left back` in ASCII), `Esc back`, ...) is always kept; the
hints before it are kept in order while they fit, and one that does not fit
is dropped whole (a shorter one after it may still fit), so the hints near
the end of a line go first; the keys still work.

Keys (`?` in setup shows the list and server page keys, the marks and the
Status phrases; letters match either case):

| Where | Keys |
| --- | --- |
| List | Tab/Shift+Tab: next or previous domain (wrapping). Up/Down: server; PgUp/PgDn, Home/End. Right or Enter: open the server's page. Space: on/off, written at once (on a row that misses something it asks for it first, see Exceptional states). `u`: undo the last change, again and again, back to how setup opened. `s`: set up every server that is on and needs no step from you (see "Setting up"). `n`: new personal connection. `/`: search. Left or Esc: clear an active search, keeping the cursor on the server it was on; otherwise Esc only says how to quit. `q`: quit (while setting up runs, it stops it). `?`: help. Ctrl+C: quit, exit 130. |
| Search | Typing filters label, ID and description in every tab (the help lines show these keys while searching); after each edit, when the current tab has no match it moves to the first tab, in tab order, that has one; with no match anywhere it stays. Backspace, Ctrl+U. Enter or Down: back to the list, filter kept. Esc: clear; the cursor stays on the server it was on. |
| Server page | Up/Down: choose an action; the page opens on the first, the next step. Enter: do it. Right: open Choose tools or Edit only, so a stray arrow never signs in, starts or removes anything. Space: on/off. `s`: set up this server (also one that loaded its tools already). `u`: undo. `i`: show or hide the technical facts. `?`: help. Left or Esc: back. `q`: quit (while setting up runs, it stops it). While an input is edited on the page: typing, Backspace, Ctrl+U, paste; Enter writes it; Esc leaves it as it was. |
| Tools | Opening the page loads the tools of a server that is on (Esc cancels a running load). Up/Down. Space: on/off, written at once. `s`: set up this server right here; its tools show once it starts. `u`: undo. `?`: help. Left or Esc: back (while a sign-in waits, Esc skips it). `q`: quit (while setting up runs, it stops it). |
| Setting up | The list stays usable. Esc skips the sign-in waiting for your browser; `q` stops setting up and stays in setup (`q` again quits). |
| Join | Intro: Enter uses the suggested 1Password account, `e` types another one (Enter uses it, Esc goes back), Esc skips 1Password for now; with nothing to ask, Enter continues. Presets: Up/Down, Space picks or unpicks (on Start empty it clears the picks), Enter turns the picks on and continues to the list (on Start empty, without the picks). `q` (and Esc where it does not skip) quits. |
| Form or field | Typing, Backspace, Ctrl+U, paste (control characters stripped). Tab/Shift+Tab: next or previous field. Enter: apply, written at once (on the field Space opens, the next missing item). Esc: discard the edit. Keys pressed and text pasted while a change is written wait for it, then run in order. |

Every setup action has a non-interactive command, and both run the same code:

| Setup action | Command |
| --- | --- |
| Space: off to on (links the local profile with the requirement's ID, as `enable` does) | `enable <mcp>` |
| Space on a row that misses an input or a profile | `config input set` per missing input and `config profile bind` when needed, then `enable <mcp>` (setup writes them together, as one change) |
| Space: on to off | `disable <mcp>` |
| Space on a row under review | `enable <mcp>` (accepts like `sync --apply --accept <mcp>`; only `enable` also links the local profile with the requirement's ID) |
| Server page: Set or Change an input | `config input set <mcp> <name> <value>` |
| Server page: Link or Change 1Password profile | `config profile bind <mcp> <profile>` |
| Server page: Turn on, Turn off, Accept the change | as Space |
| Server page: Sign in, Start, Start again, Test again; `s` on the tools page | as `s` on the page |
| Toggle a tool | `tools enable <mcp> <tool>` / `tools disable <mcp> <tool>` |
| Open the tools page of a server that is on | `tools <mcp>` |
| Set up (`s`): start a connection and load its tools | `tools <mcp>` |
| Set up (`s`): sign in | `auth <mcp>` (the same offline refusals first; the URL shows on screen, not on stderr) |
| Join: use the suggested or a typed 1Password account | `config profile set <requirement ID> --file <profile.json>` with `{"mode":"desktop","account":"<account>"}` |
| Join: presets | `enable <mcp>` per connection, skipping those already on and those that miss an input or a profile (setup writes them as one change) |
| Add a personal connection | `local add --file <definition.json>` |
| Edit a personal connection | `local update <id> --file <definition.json>` (not made when the definition changed elsewhere since the form opened) |
| Remove a personal connection | `local remove <id>` (turns it off too, as the command does; `u` brings the definition back; not made when the definition changed elsewhere since the page showed it) |
| Browse, search | `catalog [--domain <id>]`, `list`, `inspect <mcp>` |
| `u`: undo the last change | none: setup writes back what the connection had before (its selection and, for a personal one, its definition) |

Not in setup, shown as hints: `add` (no catalog), `config profile set` (no
profile yet), `auth <mcp>` (a sign-in setup cannot do here) and `sync`
(what a review changed).

The server page. Right or Enter on a row opens its page: a to-do list for
that one server.

```text
Development / GlitchTip                                                                                ◐ needs a sign-in
Errors and performance.

> Sign in to GlitchTip  opens your browser
  Choose tools          all, after sign-in
  Turn off

To do
  ✓ On
  ◆ Sign in with your browser
  ○ Tools load after sign-in

Runs  remote · errors.example
From  Acme · Monitoring, Development

Enter do it   Space on/off   s set up   u undo   i technical   ? help   ← back
```

The title is `<Domain> / <Label>` (the domain the list showed, else the
row's first) with the state on the right, then the description. The actions
come next, and the page opens on the first: the next step for the server's
state, in this order:

| First action | When | Enter |
| --- | --- | --- |
| `Turn on` | it is off (`asks for what it misses first` when it misses an input or a profile) | as Space |
| `Accept the change` | it is on and changed upstream (`mcparcel sync shows what changed`) | as Space |
| `Set <input>` | an input has no value | edits it right on the page; Enter writes it and the cursor moves to the next step |
| `Link 1Password profile` | it needs a profile Space cannot link by itself | the profile picker; exactly one local profile is linked without it (`Linked 1Password profile <p>.`) |
| `<command> not installed` | its command is not found on the PATH it runs with | looks again (`Found <command>.` or `<command> is still not installed: it is not found on PATH.`) |
| `Sign in to <Label>` | it needs a browser sign-in (`opens your browser`; `run mcparcel auth <mcp>` where setup cannot sign in) | sets this server up, as `s` does |
| `Start again` | it could not start this session (the reason as its note) | sets it up again |

Then come `Choose tools` (`2 of 2 on` once loaded, else `all` or `N tools
off`, with `, after sign-in` while a sign-in is needed), which opens the tools
page; `Start` (`loads its tools`) or, once its tools loaded, `Test again`
(`last ok 2m ago`) for a server that is on and waits on nothing; `Change
<input>` with its value for each input that has one (a url input, and any
input the HTTP URL comes from, as host only) and `Change 1Password profile`
with the linked profile's name (or `Link 1Password profile` with `<p> links
when on` while Space would link one); `Turn off` when it is on; and for a
personal connection `Edit` (the form) and `Remove` (`u brings it back`). An
unavailable row offers only `Turn off`. Each action that changes something is
written at once, and the cursor goes back to the top, where the next step
then shows. A label longer than its column keeps its note after it; on a
narrow page the note is cut before the label.

Below the actions, the To do lists what the server needs and what this
session knows: `✓` (`[+]` in ASCII) what is done (`On`, `workspace is set`,
`1Password: work`, `1Password: links team when on`, `Signed in`, `2 tools
loaded`), `◆` (`[*]`) what is left for you (`Turn it on`, `Accept the
change`, `Set workspace`, `Link a 1Password profile`, `Install
rails-blocks-mcp`, `Sign in with your browser`, `Could not start: timed
out`) and `○` (`[.]`) what happens later by itself (`Sign in when it first
starts`, `Tools load after sign-in`, `Tools load on first use`). Once its tools
loaded and nothing is left, the page lists the tools instead (a mark, the
name and the first line of the description, as many as fit, then `N more
tools: Choose tools lists them all.`):

```text
Development / Context7                                                                                 ● ready · 2 tools
Up-to-date library documentation.

> Choose tools  2 of 2 on
  Test again    last ok just now
  Turn off

Tools
  ✓ get-library-docs    Fetch docs for a library.
  ✓ resolve-library-id  Package name to library ID.

Runs  this device · npx
From  Acme · Development, Research
```

Last come Runs (`this device · <command>` or `remote · <host>`) and From (the
catalog's name, or Personal, and the server's domains). `i` adds the
technical facts, dim: `ID`, `Source` (`owner/repo, commit a54e28f, synced 2m
ago`, or `Personal`), `Transport` (`stdio: <command> (N arguments)` or `HTTP:
<host>`), `Tool policy` (`all tools` or `N allowed by source`, `N denied by
source`, `N off`) and, when it needs one, `1Password` (`needs profile team,
linked to work`). The page never shows argument, env or header values, a
URL's path or query, or a profile's account. Shared (catalog) definitions
are read-only. An input edit that is invalid shows the field path and reason
under it, never the value. A url input starts empty and shows `Now <host>;
an empty Enter keeps it.` (keys: `Enter set it (empty keeps it)   Esc
cancel`). A page taller than the window ends in `N more lines below.`

The personal form (`n` on the list, `Edit` on the page) edits label,
description, domains (comma-separated, pre-filled with the current tab when
adding), command, and arguments (space-separated) or URL; the transport type is
chosen when adding only. Editing keeps every other field (listed by name as
`Kept: ...`); arguments and URL are replace-only, and arguments that are not
literals are read-only. Form errors show the field path and reason, never the
value.

Every change applies right away, as the matching command does: Space, a
tool toggle, an input, a profile link, the personal form, a removal and the
join presets each write one revisioned update the moment you make them. There is
no draft and nothing to save; `q` quits at once. When the store changed
elsewhere since setup last read it (another terminal, a sync), the change is
applied once more, to the fresh state, which then shows. A change that no
longer applies there is not made (see Exceptional states), so setup never
overwrites a newer CLI edit or enables a definition you have not seen. `u`
undoes the last change as a new update (`Paper is off again.`, `Tool erase
of Excalidraw is on again.`, `Nothing to undo.`), again and again back to
how setup opened; an enable that linked a profile by itself, or the presets
of a join, undo as one step. An undo puts back what the connection had
before, review state included, and is refused when the connection changed
elsewhere since.

Setup never contacts GitHub (join fetches the catalog before setup opens)
and never starts the runtime while you browse or change things. To show each
Status it reads, on this device only, the stored sign-ins of the servers
that are on (as `auth status` does, without a prompt: on Linux it reads only
an unlocked keyring item, so a locked keyring reads `sign-in unknown`) and
looks each command up on the PATH it runs with. Only opening the tools page
of a server that is on and setting up (`s`), each an explicit step, contact
the runtime, and 1Password or a browser may then ask you. The tools page
(`Tool policy: all tools`, then the tools) says what is missing first
instead, with the keys that get there (`Paper is off: ← back, then Space
turns it on.`, `rails-blocks-mcp is not installed. Install it, then press
s.`), and loads nothing. For a server that needs a sign-in it says `Needs a
sign-in first: s signs you in.` (`run mcparcel auth <mcp> in a terminal.`
where setup cannot sign in); `s` there signs in and the tools show on the
page. A load the server answers with a sign-in request counts as a sign-in,
not a failure: the row reads `sign in` (or `run mcparcel auth <mcp>`). Loaded
tools are kept for the session only; opening the page again after a failure
tries again. The daemon hides disabled and source-denied tools, so a disabled
tool that is not loaded shows as `(disabled)`. New tool names are added with
`tools disable`.

Setting up. `s` on the list sets up, in place, every server that is on,
waits on nothing from you (a review, an input, a profile, a missing
command or an unavailable source) and has not loaded its tools this
session; `s` on a server page or its tools page sets up that one. A server
turned off, undone or removed while setting up runs is dropped from it:
nothing starts or signs in for it, and a sign-in of its that runs is
canceled. Before it starts, setup
looks each command up again, so one installed meanwhile counts. No other
screen opens: each row's Status shows its phase while the list stays
usable.

```text
MCParcel  acme/mcp-catalog · a54e28f                                                            9 on · 3 need a step
      MCP                 Status
  ● Chrome DevTools       26 tools
  ● Context7              2 tools
> ⠋ GlitchTip             waiting for your browser...
  ⠋ Lighthouse            starting...
  ◐ Notion                next sign-in
  ⠋ Octocode              waiting
  ✕ Rails Blocks          rails-blocks-mcp not installed

Sign in to GlitchTip in your browser. Didn't open? https://errors.example/oauth/authorize?...
Esc skip this one   q stop
```

Each server first checks its stored sign-in as `auth status` does, then
starts and loads its tools as `tools <mcp>` does, at most three at a time
(`starting...`; one waiting for a free slot shows the spinner's first frame,
still, and `waiting`); a slow or failing one never holds up the others. One
that needs a browser sign-in (its stored sign-in is missing, or its server
asks for one) lines up for it instead (`next sign-in`). Sign-ins run one at
a time, in the order they were found, on their own: setup signs in as
`auth <mcp>` does (the same offline refusals first) and opens the browser;
the row reads `waiting for your browser...` and the bottom lines say `Sign in
to GlitchTip in your browser. Didn't open? <url>` (`Open this to sign in to
GlitchTip: <url>` where no browser can open), wrapped so the whole URL shows
where there is room, then `Esc skip this one   q stop`. Then its tools load.
Esc skips the sign-in that waits (`GlitchTip still needs a sign-in.`) and the
next one starts. In headless mode, or for a server with no browser sign-in
(a 1Password failure also returns `auth_required`), the row reads `run
mcparcel auth <mcp>` and nothing lines up. Without a sign-in waiting, the
bottom lines read `Setting up: k of n ready.` and the keys, with `q stop`.

When every server has settled the status says `All n are ready.` or `k of n
ready.`, and each row keeps what it found for the session: `N tools`, the
failure's short reason with `✕` (the pane and the tools page add the message
and its next step), `sign in`, or `run mcparcel auth <mcp>`. `s` again tries
the ones that did not start. `q` stops setting up: every running start and
sign-in is canceled, a server in line for or in a sign-in still needs it, and
one still starting is as it was before (`Stopped. k of n ready.`); setup
stays open, and `q` again quits. A run forgets what an earlier one found for
its servers. The summary on exit names what is left (see `setup` in the
command table).

Joining a catalog. `mcparcel join <owner/repo>` opens setup on two screens
before the list. The intro names the catalog (`Team · owner/repo · a54e28f`)
and its number of servers, then asks about each credential profile
requirement whose `suggest` names a 1Password account (catalog.md, "Joining
a catalog"), that at least one server uses and that has no local profile with
its ID yet, one at a time, the most used first (`(1 of 2)` when there are
several):

```text
MCParcel / Join
Zenjoy · zenjoy/prompt-engineering-club · 56ce8bf
25 MCP servers picked by the team.

This catalog asks for a 1Password profile: team.
  Shared API credentials from the 1Password vault MCP Integrations
Use this 1Password account? The catalog suggests it.
Account: zenjoy.1password.com
8 servers use it. Nothing is read from 1Password until a server starts.

Enter use it   e use another account   Esc skip 1Password for now
```

Enter saves the local profile with that ID right away, in its own revisioned
update: `{"mode":"desktop","account":"<account>"}` and nothing else, as
`config profile set` would. `e` takes another account, which must follow the
same rule as a suggestion (a 1Password sign-in address such as
`example.1password.com`); it is trimmed, lowercased and stripped of
`https://` and a trailing `/` first. Esc creates nothing. A requirement that
already has a local profile is not asked about, and a profile with that ID
that appears elsewhere while the question shows is kept, never replaced. The
intro lists it instead: `✓ 1Password: using your profile team (service
account, zenjoy.1password.com)` when its account is the suggested one,
whatever its mode (`desktop app` or `service account`), else (`[!]` in ASCII) `! 1Password:
your profile team uses other.1password.com, the catalog suggests
zenjoy.1password.com` (`uses a service-account token` without an account),
since the catalog's connections link to it by ID. One you confirm reads the
same `✓` line. A requirement without a suggestion and without a local profile
reads `◆ 1Password team: not set up. Add it with mcparcel config profile set
team.`; one you skip, `○ 1Password team: skipped for now`. Requirements are
named by their ID; each line has the requirement's description below it,
dim, in up to two lines. The key line stays at the bottom at every size. Where the 1Password desktop app cannot
read secrets (not macOS, or headless mode), a suggestion is such a note too,
pointing at a service-account profile, and is never asked about. The catalog cannot choose a token, a
reference or a service account, and nothing reads 1Password here. Turning on
a connection that needs the profile then links it as `enable` does.

When the catalog has presets, the next screen asks `What do you work on?
Pick any. You can change everything on the next screen.`, with one row per
preset (its label, the labels of its servers and their count), then `Start
empty`, and `N servers selected` below (on `Start empty` with presets picked,
the help reads `Enter start empty, without the picks`). Enter turns on the picked presets'
connections at once, in one change, as Space does for each, and opens the
list on the first domain, in tab order, that holds a picked server, with the
cursor on the first picked server there that needs a step (else the first
picked one); once the stored sign-ins are read it moves there again, unless
you pressed a key. One that is already on, or under review, stays as it is,
and one that misses an input or a profile stays off (the status counts them,
and Space on its row asks for what it misses). `u` turns them off
again in one step. Setting them up waits for `s`. Quitting (`q`, Esc or
Ctrl+C) keeps what you did: the source registration, a profile you
confirmed and the presets you picked.

Setup is keyboard-only; there is no mouse. Focus is visible without color:
`>` before the focused row, and brackets around the current tab. With color
the focused row is also in reverse video, and the current tab is a bold
reverse pill.

Setup draws characters beyond ASCII (the state glyphs, `·` separators, `‹` and
`›`, `←` and `→`, the pane's `│` rule, the To do marks and the spinner) only in a UTF-8
locale: the first non-empty value of `LC_ALL`, `LC_CTYPE` and `LANG` contains
`UTF-8` or `utf8` in any case, and `TERM` is not `dumb`. Otherwise every screen
is plain ASCII: the bracketed marks above, ` - ` separators, `<` and `>`, a `|`
rule and a `.oOo` spinner (`[.]`, `[o]`, ... on a row). The Marks legend on the help page (`?`) uses the
same set. Catalog text (labels, descriptions, names) with characters beyond
ASCII is shown escaped as a quoted ASCII string in both modes.

Color is on when `NO_COLOR` is empty or unset and `TERM` is set and not
`dumb`. It uses the terminal's own 16-color palette, so it follows the
terminal's theme: green for on and ready (the header's on-count only when at
least one row is, and a tool count), amber for a step (a review, a missing
input, a profile still to link, the header's step count), red for what
cannot run (unavailable, a command not installed, a failure), blue for
1Password, violet for sign-in and grey for secondary text. 256-color and truecolor terminals get finer greys. Color
only adds styling: the text is the same without it, so every state reads the
same.

Status messages are toasts: they clear after about 3 seconds, errors included.
The prompt of a pending enable stays until it is replaced. Animation is on when color is on and `MCPARCEL_REDUCE_MOTION` is
empty or unset. Then a toast dims for 300 ms before it clears; a row you turn
on, turn off or accept with Space in the list, or that a pending enable turns
on, flashes for about 180 ms (green when on, dim when off); and `Loading the
tools of <mcp>...` on the tools page and each row that is starting or
signing in have a spinner in place of its glyph. Nothing moves while the
screen is idle, nor while setting up only waits for you. With animation off
nothing animates: toasts clear without dimming, rows do not flash and the
spinner is a static `Loading...` (on a row, its first frame).

## Exceptional states

- No catalog: `No catalogs yet. Add one from a terminal: mcparcel add <owner/repo>.
  Press n to add a personal connection.`
- No results: `No MCPs match "x" in Design. Esc clears the search.` Tabs show
  match counts and Other stays visible.
- Offline: setup never goes online. With one catalog the header shows its
  snapshot age; the server page's technical facts (`i`) show it for every
catalog row. A
  tool load that cannot reach the runtime says `Could not connect. Configured
  MCPs on this device still work.`; a server that could not start says `Could
  not start <Label>.` and `mcparcel doctor shows why.`
- Missing input/profile: `○` (`◐` when on) with `needs <input>` or `link
  1Password` in Status. One
  Space turns an off row on (on a `◐` row it turns it off). Setup asks for each missing item in turn, with
  `Paper needs workspace and a 1Password profile. Esc keeps it off.` (`Esc
  leaves it unchanged.` for a row under review): a missing input opens its field
  editor; a missing credential profile is linked to the local profile with the requirement's ID without asking (as `enable`
  does, so it is not listed as missing), else to the only local profile, else the
  picker opens with two or more, or the `config profile set` hint with none.
  After each fill setup checks again; when nothing is missing it writes what
  you filled in and the enable as one change, and returns to the list on that
  row with `Paper is on.`, led by `Linked
  1Password profile work.` when it linked one. Esc on the field or profile
  screen returns to the list with `Paper stays off.` (`Paper is unchanged.` for
  a row under review) and writes nothing. `Turn on` on the server page asks
  the same way and returns to the page. Opening a server page another way
  ends the pending enable, so a later edit there never turns the row on. When only a profile that Space will link is
  missing, the row shows no step (`○`, nothing in Status), its To do reads
  `✓ 1Password: links team when on` and the compact summary line `Space
  turns it on and links 1Password profile team.`
- Source removal: `✕` (`○` once off) with `unavailable` in Status; `<source> no
  longer has this server. Setup keeps your choice for it in case it comes
  back.` It can only be turned off
  (Space turns it off, also when it is under review).
- Review required in setup: `◐` (`[~]` in ASCII) with `review change` in Status;
  its page leads with `Accept the change` (`mcparcel sync shows what
  changed`), which Space does too, as `mcparcel enable <id>` does.
- Changed elsewhere: a change made in another terminal or by a sync while
  setup is open shows up with setup's next change, which is applied to the
  fresh state. A change that no longer applies there is not made: `This
  changed elsewhere meanwhile, so the change no longer applies.` Edit and
  Remove of a personal connection are refused this way when its definition
  changed since the form or page showed it, so another edit is never
  overwritten. An enable
  that now misses an input asks for it, as Space does. `u` on a connection
  that changed elsewhere since says `That changed elsewhere since, so it
  cannot be undone.` and drops that step. A change that failed part way
  (`config_write_failed`) shows its message and the stored state; when that
  cannot be read either, the summary on exit says so.
- New catalog version: review via sync; no silent command/credential changes.
  An enabled connection whose execution or auth fields changed shows `◐` and
  `review change` (its page: `needs a review`) and cannot be called until
  accepted with `sync --apply --accept <mcp>` or `enable <mcp>`. The diff names every changed field. Only `enable` links
  the local profile with a newly required credential profile's ID; before
  `sync --apply --accept`, run `config profile bind <mcp> <profile>`.
- Expired auth: metadata still available; first protected call initiates auth
  (OAuth: returns `auth_required` pointing at `auth <mcp>`). In setup the
  Status reads `sign in` once the stored sign-in says so; the tools page says
  `Needs a sign-in first: s signs you in.`, and setting up lines the
  server up for a sign-in.
- No-input agents: receive an actionable structured auth error instead of a prompt;
  a server's approval request is declined with the "no prompt was possible" notice.

## Deferred surface

No hosted gateway, code-mode sandbox, catalog authoring/publishing commands,
automatic provider-key creation, Homebrew dependency or per-project configuration
discovery. No full mcporter CLI emulation: the argument forms above cover how
agents call tools through mcporter today, but `mcporter list <server> --schema` becomes
`mcparcel tools <mcp>`, `--output json` becomes `--json`, and function-call
syntax, ad-hoc `--stdio`/`--http-url` servers and positional arguments are not
supported. `config domain set` (local label overrides) is deferred. Resource/prompt commands are added if stage-1
workflow discovery proves they are needed for the compatibility promise.
