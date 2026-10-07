# MCParcel CLI contract

Review draft, 4 October 2026. Commands below are proposed, not implemented.
This revision specifies the earlier CLI sketch; [catalog.md](catalog.md) and
[runtime.md](runtime.md) define configuration and execution behaviour.
All commands, flags, help, messages, errors and terminal UI are English.

## Start and daily use

```sh
npx mcparcel add acme/mcp-catalog
npx mcparcel setup
npx mcparcel list
npx mcparcel tools paper --json
npx mcparcel call paper.<tool> --args-file request.json --json
```

`add` registers a GitHub catalog and validates metadata. It does not enable entries,
start servers or read 1Password. `setup` remains useful whenever selections change.
1Password authentication is implicit on the first call that needs it; there is no
mandatory `unlock` command. OAuth sign-in is explicit in this release: a call
without a stored session returns `auth_required` with next action
`mcparcel auth login <mcp>` instead of opening a browser. Private catalog access through `gh` is independent of 1Password.

Catalog README agent prompt:

> Add acme/mcp-catalog as an MCParcel catalog. Show its domains and
> connections, ask which I need, and enable my selection. Preserve my personal
> connections. Use MCParcel for subsequent MCP calls.

The initial add trusts the explicitly named source for later selected execution.
Print repository, file and revision. Changes are applied only by explicit sync.
A team can publish a single shell line using `add ... && setup`; agents can use
`catalog` and `enable` without driving a terminal UI.

## Command surface

The short `mcparcel` form assumes the native executable is available; all commands
also work via `npx mcparcel ...`. No global installation or Homebrew is required.

| Command | Contract |
| --- | --- |
| `add <owner/repo> [--path <file>] [--ref <ref>]` | Validate and register a source, idempotently |
| `remove <owner/repo>` | Remove local source registration; retained selections become unavailable; no remote write |
| `catalog [--domain <id>]` | Available metadata including disabled/missing-input entries |
| `list` | Enabled selections; clearly mark unavailable entries; no live connection |
| `inspect <mcp>` | Effective non-secret config, origin, requirements, blockers and revision |
| `enable <mcp>...` / `disable <mcp>...` | Atomically update selection for supplied IDs |
| `tools <mcp> [--cached]` | Live schema discovery; `--cached` has no cache yet and always returns `schema_cache_miss` |
| `tools enable <mcp> <tool>...` / `tools disable <mcp> <tool>...` | Change personal tool selection; cannot override source policy |
| `call <mcp>.<tool> [key=value ...] [--args <json>] [--meta <json>]` | Invoke an enabled, allowed tool; a server's approval request during the call is shown as a prompt (see Approval prompts). The CLI checks the connection and tool policy offline first: a denied tool (`tool_denied`, exit 4; `deny` wins over `allow`) or a disabled, unreviewed, unknown or ambiguous connection fails before the runtime is contacted, so no daemon starts, no connection opens and no token is requested. The runtime checks again at admission and before dispatch |
| `setup` | Interactive domain and connection editor (see "Domain matrix and terminal sketch"); renders on stderr, prints `Configuration saved at revision N.` or `No changes saved.` on stdout (plus `The last save could not be confirmed. Run mcparcel list to check the configuration.` after an unconfirmed save); Ctrl+C exits 130 after printing any save that already happened |
| `sync [<owner/repo>] [--apply [--accept <mcp>...]]` | Fetch and display update; only `--apply` changes active snapshot; `--accept` unblocks named connections whose execution or auth changed |
| `import mcporter --file <path> [--bindings <file>] [--only <id>...] [--apply]` | Preview or apply supported imports, with explicit unresolved-field report; unbound `${NAME}` in env/header values becomes `env:NAME` with an `environment_reference` warning |
| `local add --file <definition.json>` | Add a connection object containing `id` plus catalog connection fields |
| `local update <id> --file <definition.json>` | Replace that personal connection atomically; matching ID required |
| `local remove <id>` | Remove personal definition; never mutate a team catalog |
| `config validate --file <catalog.json>` | Validate schema and references offline |
| `config input set <mcp> <name> <value>` | Set a declared non-secret local input |
| `config profile set <name> --file <profile.json>` | Save a validated credential profile containing references only |
| `config profile bind <mcp> <profile>` | Bind the connection's declared credential requirement |
| `auth login <mcp>` | Browser sign-in for an HTTP connection; prints the URL on stderr and opens the browser (where no browser can open, it prints only the URL to open); `--no-input` returns `auth_required` once the connection is known to use sign-in; JSON `{connection, signedIn}` (`signedIn: false` when the server never asked for sign-in). Refused offline, without starting the runtime: a `client_credentials` connection (`invalid_arguments`, "<mcp> uses client credentials; there is nothing to sign in to.") and, in headless mode, every connection (`auth_required`, "This server needs sign-in, which headless mode cannot do.") |
| `auth status [<mcp>]` | Offline, never starts the runtime (it asks a running daemon for its 1Password sessions): per HTTP connection `{connection, state, signedIn, refreshToken, lastRefreshAt?, accessTokenExpiresAt?, refreshTokenExpiresAt?, lastRefreshFailure?: {at, code}, keepAlive, cause?: {code, message}, previousCause?: {code, message}, nextAction?}` in `items`, plus `events` (the health log) for `auth status <mcp>`; `state` is `ok`, `expiring` or `sign-in required`; causes are listed in runtime.md "Health log and `auth status` as built". `previousCause` is set for a signed-in session whose last sign-in followed a terminal failure and says why that sign-in was needed. `keepAlive` is `off` when the connection turned it off, else `unavailable` when its client secret is a 1Password reference, else its keep-alive interval (`24h` by default). Human output: `<connection>  <state>  (<cause>)`, and for one connection the cause message, `Last sign-in needed: <message>`, last refresh, expiries, keep-alive and next action. No token, client identifier or redirect. Without `<mcp>`: enabled HTTP connections marked OAuth, holding a sign-in, or whose server asked for sign-in (an unmarked connection whose call or login got `auth_required`; shown as `sign-in required`). `profiles` lists the local credential profiles an enabled connection uses (for `auth status <mcp>`: that connection's profile when it has `op://` references) as `{profile, mode, session, sessionExpiresAt?, connections}`; `session` is `active`, `expired`, `none` (no session, or the runtime is not running) or `unknown` (the daemon could not be asked or runs another version); human output `profile <id>  <mode>  <session>[  until <RFC3339>]`. Never the account or a reference. A connection with neither sign-in nor `op://` references gives `invalid_arguments` `client_credentials` connections are left out in every mode (their token lives in daemon memory only); naming one is `invalid_arguments`. Headless mode reads no Keychain: `items` is empty (human: "No sign-ins in headless mode.") |
| `auth lock` | Through the runtime (starts it if needed): end every 1Password session, cancel protected work (dispatched calls report `outcome_unknown`, queued ones `auth_required`), stop protected processes and bar every OAuth connection from reusing its stored session until its next `auth login` (cause `locked` in `auth status`). Secret-free connections keep running. JSON `{locked}` |
| `auth refresh <mcp>` | Drop the connection's cached 1Password values; its next call reads them again through the existing session (no prompt) and reconnects only when a value changed. Never starts the runtime; when it is not running nothing is cached and JSON is `{connection, invalidated: false}`, else `{connection, invalidated: true}`. A connection without `op://` references gives `invalid_arguments` (next action `mcparcel runtime restart` when it uses `env:` references) |
| `auth logout <mcp>` | Remove the Keychain sign-in through the runtime (starts it if needed); JSON `{connection, removed, providerRevoked}`; `providerRevoked` is always false for now. A `client_credentials` connection is refused offline like `auth login` |
| `doctor [<mcp>] [--live]` | Local prerequisite checks; only explicit live mode connects to specified MCP |
| `runtime status` / `runtime restart [--force]` / `runtime stop [--force]` | Inspect, restart or stop the daemon; status includes `stayAlive` (human: `Stay-alive: on` or `off`), true when `runtime.keepAlive` is set and an OAuth session may still need refreshing, and `credentialSessions` (`[{profile, mode, state, expiresAt}]`, omitted when there are none; no account or reference); restart and stop refuse active calls unless forced; restart recaptures the login environment and drops pooled sessions, so changed `env:` values apply; stop on a stopped runtime succeeds. Headless mode: status shows `Environment: daemon environment`, and restart starts the new daemon with the forwarded variables of the caller's own environment (no login shell). Against `runtime serve`, restart (also `--force`) is refused with `runtime_supervised` |
| `runtime serve` | Run the runtime in the foreground under a supervisor: takes the daemon lock itself (`runtime_busy`, exit 6, when another runtime holds it), logs to the daemon log and stderr, never exits when idle; SIGTERM or SIGINT ends dispatched calls as `outcome_unknown`, closes sessions, removes the socket and exits 0 (human `Runtime stopped.`, JSON `{"stopped":true}`). It marks the runtime directory supervised: other CLIs wait up to 15 s for it instead of auto-starting a daemon, then fail `runtime_supervised`. `runtime.supervised: true` in `config.json` (headless only) has the same effect before serve first ran, e.g. on a fresh pod. On Linux it requires headless mode |
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
`add`, `remove`, `sync` with or without `--apply`) fails `config_read_only`
before it takes a lock or sends a request. On Linux, `tools`, `call`, `auth`
and `runtime` in desktop mode fail `runtime_unsupported` ("On Linux, MCParcel
runs in headless mode only."); offline commands work. See
[headless.md](headless.md).
`setup --no-input`, `setup --json` and setup without a TTY (stdin and stderr must
be terminals, in the foreground) fail with `terminal_required` (exit 2) before
reading any configuration, and write nothing.

`local` file updates can also change personal domain assignments; setup details
expose the same fields and input/profile bindings. Setup never shows argument,
env or header values or a URL's path and query; replace them by typing a new
value in the personal form, or use `local update`. Setup does not author team
catalogs or edit protected policy.

`enable` validates all specified entries and local prerequisites before saving;
missing executable/auth availability is a reported runtime prerequisite, not a
reason to resolve secrets. Missing declared inputs/profile mappings block enable
with `config_required`. Bulk enable is all-or-nothing. Tool names may be configured
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
| 130 | User cancellation |

Error codes distinguish `auth_required`, `auth_expired`, `config_required`,
`config_conflict`, `connection_unavailable`, `review_required`, `tool_denied`,
`runtime_version_mismatch`, `runtime_config_mismatch`, `auth_account_conflict`,
`terminal_required` and `outcome_unknown`. `review_required` uses exit 4;
`auth_rate_limited` (exit 6) means 1Password refused one request; the session and
pooled process stay, and nothing is retried within the call;
`runtime_config_mismatch` exit 6; `auth_account_conflict` exit 3;
`terminal_required` exit 2 (setup without an interactive terminal, or with
`--json` or `--no-input`; the next action lists the equivalent commands).
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
sanitized OAuth error code is shown), `keychain_unavailable` (Keychain could not
read or store the sign-in, or it is too large) and `auth_callback_unavailable`
(fixed callback port in use), all exit 3. Catalog commands add `invalid_repository` (exit 2,
malformed `owner/repo` argument, nothing fetched), `invalid_catalog` (exit 2, fetched
content rejected) and `catalog_unavailable` (exit 4, repository unreachable or not
registered). Human `sync` with no registered catalogs prints a hint to run
`mcparcel add <owner/repo>`. `runtime stop --json` returns
`{"stopped":true,"wasRunning":<bool>}` and never starts a daemon. Errors include request ID when execution began. Upstream
secret-bearing messages are sanitized. No automatic execution of corrective hints.

## Domain matrix and terminal sketch

All 32 existing connections are covered by [compatibility.md](compatibility.md).
A connection appearing in multiple tabs has one shared selection. For example,
context7 belongs to Development and Research without creating two processes.

```text
+------------------------------------------------------------------------------+
| MCParcel / Setup                                      1 catalog + personal    |
| [Design 2/3]  Development 1/4  Research 0/4  Servers 0/2  ...                   |
|                                                                              |
| MCP          Enabled   Source                           Runs on              |
| > Paper      [x]       acme/mcp-catalog    This device          |
|   Figma      [ ]       acme/mcp-catalog    Remote               |
|   Excalidraw [x]       Personal                         This device          |
|                                                                              |
| Paper                                                                        |
| Connect to the Paper app on this device.                                      |
| Configuration ready. Connection not checked.                                 |
|                                                                              |
| Left/Right: domain   Up/Down: MCP   Space: toggle   Enter: details              |
| /: search   Tab: actions   A: add personal connection                          |
| 1 unsaved change                                          [Save] [Cancel]     |
+------------------------------------------------------------------------------+
```

Sources and counts are illustrative; the as-built renders are in
[acceptance.md](acceptance.md). The wide layout above (columns MCP, Enabled,
State, Source, Runs on) needs at least 100x30. Anything smaller uses a compact
list: MCP with a state tag (`(config)`, `(review)`, `(unavailable)`), Enabled,
Runs on and a truncated Source, one summary line and one help line ending in
`?: help`; tabs scroll with `<` and `>`. Below 40x10 setup shows `Terminal too
small (need 40x10). Resize, or press q.` Enabled reads `[x]` on, `[ ]` off and
`[!]` on with review required. State is `Ready`, `Configuration required`,
`Review required` or `Unavailable`, separate from Enabled.

Keys (`?` shows them in setup; letters match either case):

| Where | Keys |
| --- | --- |
| List | Left/Right: domain. Up/Down: MCP; PgUp/PgDn, Home/End. Space: toggle. Enter: details. `/`: search. Tab/Shift+Tab: focus `[Save]`/`[Cancel]`, Enter activates. `A`: add a personal connection. Ctrl+S: save. `q` or Esc: cancel (Esc clears an active search first). `?`: help. Ctrl+C: quit now, discard unsaved changes, exit 130. |
| Search | Typing filters label, ID and description in every tab (the help lines show these keys while searching). Backspace, Ctrl+U. Enter or Down: back to the list, filter kept. Esc: clear. |
| Details | Up/Down: field. Enter: edit or choose. Space: toggle. `t`: tools. `l`: Connect and load tools. `e`: edit (personal only). Esc: back. |
| Tools | Up/Down. Space: toggle. `l`: Connect and load tools. Esc: back (cancels a running load). |
| Form or field | Typing, Backspace, Ctrl+U, paste (control characters stripped). Tab/Shift+Tab: next or previous field. Enter: apply. Esc: discard. |
| Conflict | `r`: reload and reapply. Esc: back to the draft. |
| Confirm | `y`/`n` (default N; Enter and Esc mean N). |

Every setup action has a non-interactive command, and both run the same code:

| Setup action | Command |
| --- | --- |
| Space: off to on | `enable <mcp>` |
| Space: on to off | `disable <mcp>` |
| Space on `[!]` | `enable <mcp>` (accepts, like `sync --apply --accept <mcp>`) |
| Set an input | `config input set <mcp> <name> <value>` |
| Choose a credential profile | `config profile bind <mcp> <profile>` |
| Toggle a tool | `tools enable <mcp> <tool>` / `tools disable <mcp> <tool>` |
| Connect and load tools | `tools <mcp>` |
| Add a personal connection | `local add --file <definition.json>` |
| Edit a personal connection | `local update <id> --file <definition.json>` |
| Browse, search | `catalog [--domain <id>]`, `list`, `inspect <mcp>` |

Not in setup, shown as hints: `add` (no catalog), `config profile set` (no
profile yet), `auth login` (sign-in needed), `sync` (review details) and
`local remove` (personal details).

Details show the source with snapshot commit and age, domains, transport
(`stdio: <command> (N arguments)` or `HTTP: <host>`), inputs (`url` inputs, and
any input the HTTP URL comes from, as host only), the bound profile by name only, policy (`all tools` or `N allowed
by source`, `N denied by source`, `N disabled by you`), schema (`none cached` or
`loaded this session Xm ago`), the config revision and unsaved changes. Shared
(GitHub) definitions are read-only. Such an input starts empty and shows its
current host; an empty Enter keeps the value. The personal form edits label,
description, domains (comma-separated, pre-filled with the current tab when
adding), command, and arguments (space-separated) or URL; the transport type is
chosen when adding only. Editing keeps every other field (listed by name as
`Kept: ...`); arguments and URL are replace-only, and arguments that are not
literals are read-only. Input and form errors show the field path and reason,
never the value.

Changes stay in a draft until Save. Save writes all changes in one revisioned
update and keeps setup open (`Saved at revision N.`, or `Nothing to save.`
without a store call). Cancel writes nothing and asks `Discard N unsaved
changes? y/N` first when there are any; Ctrl+C discards unsaved changes and
exits 130. A config revision conflict keeps the draft and offers reload. Reload
reapplies the draft in the order it was made on top of the newer configuration
and names what it dropped: a change to a field or connection definition that
changed elsewhere is dropped (`changed elsewhere`), as is a change that no
longer applies, so setup never overwrites a newer CLI edit or enables a
definition you have not seen.

Setup never contacts GitHub, starts the runtime or reads credentials while you
browse, edit or save. Only `Connect and load tools` contacts the runtime, and
credentials may then be requested. Connect needs the saved configuration: the
connection must be saved enabled and ready with no unsaved change to it (tool
toggles excepted). Loaded tools are kept for the session only; the daemon hides
disabled and source-denied tools, so a disabled tool that is not loaded shows as
`(disabled)`. New tool names are added with `tools disable`.

Keyboard-only navigation, visible focus (`>` on the focused row and button,
brackets on the active tab) and plain ASCII are always used; color (when
`NO_COLOR` is unset and `TERM` is set and not `dumb`) only adds reverse video
and bold. No mouse.

## Exceptional states

- No catalog: `No catalogs yet. Add one from a terminal: mcparcel add <owner/repo>.
  Press A to add a personal connection.`
- No results: `No MCPs match "x" in Design. Esc clears the search.` Tabs show
  match counts and Other stays visible.
- Offline: setup never goes online. The header reads `1 catalog + personal` and
  details show the snapshot age. A failed connect says `Could not connect.
  Configured MCPs on this device still work.`
- Missing input/profile: `Configuration required`, separate from Enabled. Space
  on such a row opens details on the first missing field: `Paper needs: input
  workspace, credential profile. Fill them in, then press Space.`
- Source removal: `Unavailable`; `<source> no longer defines this connection.
  Your selection is kept for a future restoration.` It can only be turned off
  (Space turns it off, also when it is marked `[!]`).
- Review required in setup: `[!]`; details show the transport and profile name
  plus `Space accepts it (same as mcparcel enable <id>). Run mcparcel sync to see
  what changed.`
- Save conflict: `The configuration changed since setup loaded it. Your N
  changes are kept.` with `r: reload and reapply   Esc: back`. After reload:
  `Reloaded at revision M. Reapplied K changes. Dropped (changed elsewhere):
  enable Paper.` A durable save that was not confirmed (`config_write_failed`)
  shows its message plus `Press r to reload.` Reload lists changes the newer
  configuration already holds as `Already in the configuration: enable Paper.`;
  when they came from the unconfirmed save, it counts as saved.
- New catalog version: review via sync; no silent command/credential changes.
  An enabled connection whose execution or auth fields changed shows `Review
  required` and cannot be called until accepted with `sync --apply --accept <mcp>`
  or `enable <mcp>`. The diff names every changed field.
- Expired auth: metadata still available; first protected call initiates auth
  (OAuth: returns `auth_required` pointing at `auth login`). In setup it appears
  only after an explicit Connect, with `mcparcel auth login <id>`.
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
