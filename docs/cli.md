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
| `tools <mcp> [--cached]` | Live schema discovery or strictly cached schemas |
| `tools enable <mcp> <tool>...` / `tools disable <mcp> <tool>...` | Change personal tool selection; cannot override source policy |
| `call <mcp>.<tool> [key=value ...] [--args <json>] [--meta <json>]` | Invoke an enabled, allowed tool; a server's approval request during the call is shown as a prompt (see Approval prompts) |
| `setup` | Interactive domain and connection editor |
| `sync [<owner/repo>] [--apply [--accept <mcp>...]]` | Fetch and display update; only `--apply` changes active snapshot; `--accept` unblocks named connections whose execution or auth changed |
| `import mcporter --file <path> [--bindings <file>] [--only <id>...] [--apply]` | Preview or apply supported imports, with explicit unresolved-field report; unbound `${NAME}` in env/header values becomes `env:NAME` with an `environment_reference` warning |
| `local add --file <definition.json>` | Add a connection object containing `id` plus catalog connection fields |
| `local update <id> --file <definition.json>` | Replace that personal connection atomically; matching ID required |
| `local remove <id>` | Remove personal definition; never mutate a team catalog |
| `config validate --file <catalog.json>` | Validate schema and references offline |
| `config input set <mcp> <name> <value>` | Set a declared non-secret local input |
| `config profile set <name> --file <profile.json>` | Save a validated credential profile containing references only |
| `config profile bind <mcp> <profile>` | Bind the connection's declared credential requirement |
| `auth login <mcp>` | Browser sign-in for an HTTP connection; prints the URL on stderr and opens the browser; `--no-input` returns `auth_required` once the connection is known to use sign-in; JSON `{connection, signedIn}` (`signedIn: false` when the server never asked for sign-in) |
| `auth status [<mcp>]` | Offline, never starts the runtime: per HTTP connection `{connection, signedIn, refreshToken, accessTokenExpiresAt?, lastRefreshFailure?: {at, code}}` in `items`; no token. Without `<mcp>`: enabled HTTP connections marked OAuth or holding a sign-in |
| `auth lock` | End in-memory credential sessions and block stored OAuth reuse until reauthorization |
| `auth refresh <mcp>` | Invalidate credential lease; next call resolves/reconnects as necessary |
| `auth logout <mcp>` | Remove the Keychain sign-in through the runtime (starts it if needed); JSON `{connection, removed, providerRevoked}`; `providerRevoked` is always false for now |
| `doctor [<mcp>] [--live]` | Local prerequisite checks; only explicit live mode connects to specified MCP |
| `runtime status` / `runtime restart [--force]` / `runtime stop [--force]` | Inspect, restart or stop the daemon; restart and stop refuse active calls unless forced; restart recaptures the login environment and drops pooled sessions, so changed `env:` values apply; stop on a stopped runtime succeeds |
| `version` / `--help` | Version and English usage |

All commands provide `--json` except interactive `setup`; use selection/config
commands for equivalent machine actions. `--no-input` never opens UI, browser,
biometric or approval prompts (terminal or dialog). Missing necessary input yields an error with the next action.
`setup --no-input`, `setup --json` and setup without a TTY fail without writing.

`local` file updates can also change personal domain assignments; setup details
expose the same fields and input/profile bindings. Setup does not author team
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
  as the declared type fails with exit 2 and names the expected type. A property
  with no declared type, a union type or no schema entry is sent as a string.
- `key:=json` always parses the value as JSON, whatever the schema says.
- `--args <json>` takes a JSON object inline; `--args-file path` reads one from a
  file and `--args-file -` from stdin. The three are mutually exclusive with each
  other and with assignments. Values are payloads, not shell expressions.
- Duplicate keys fail. No dot-path nesting and no function-call syntax in v1.
- Coercion needs the tool's schema. `call` uses the cached schema when it matches
  the current config and auth identity, and loads it from the connection otherwise.
- `--timeout 120s` changes the call deadline. Cancellation/timeout never replays it.
  Time spent on an open approval prompt does not count toward it.
- `--meta '<json object>'` sends the object as the `_meta` of that `tools/call`
  (for example Codex's `x-codex-turn-metadata`). At most 64 KiB as sent (compact JSON with `<`, `>` and `&`
  escaped as `<` and so on), one JSON object,
  no duplicate keys. Keys the SDK or the MCP specification reserve are rejected:
  `progressToken` and any prefix whose second label is `modelcontextprotocol` or
  `mcp` (such as `io.modelcontextprotocol/`). Any of these fail with
  `invalid_arguments` (exit 2) before the runtime is contacted. Given once only.
- `--output-dir <path>` explicitly saves binary result blocks; `--json` always
  preserves complete protocol content, even when exports are requested.

## Approval prompts

A server can ask its user something in the middle of a call through MCP form
elicitation (Codex computer use: `Allow Computer Use to use "<App>"?`). `call`
shows that request only when stdin and stderr are both terminals, the CLI is in
the terminal's foreground process group, and neither `--json` nor `--no-input` is
given. The prompt goes to stderr and names the connection from the `call` target
as the asker, then the server's message, `Note:` (subtitle), `Risk:` and
`Details:`, each cleaned of control, escape, bidi and blank padding characters and
capped. Input typed before the prompt appears is discarded. An approval offers
`1) Decline (default)`, `2) Allow once`, and `3) Allow for this session` and
`4) Always allow` only when the server offers them; an absent number is invalid
input, and three invalid entries decline. A form with flat string, number,
integer, boolean or string-enum fields offers `1) Decline (default) 2) Answer`,
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
instead; `--no-input` never does. The dialog has at most three buttons: Decline
(default), Allow once and the strongest persistence offered. It gives up after
the same 5 minutes (cancel) and shows approvals only; forms are declined.

Residual risk: MCParcel cannot prove that a human answered. An agent whose shell
tool allocates a pseudo-terminal passes the terminal check and can type `4`
itself, granting `Always allow` to its own request; a computer-use or
accessibility agent can click the dialog. The daemon trusts the prompt mode the
CLI declares, so any process running as the same user can speak the socket
protocol and answer its own prompt. That is why the dialog is opt-in.
`--no-input` binds only a cooperating caller; an agent that must not grant
approvals needs OS-level isolation (another user, or a sandbox that denies the
socket).

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
Human mode prints each warning's message and next action on stderr. List output includes `items`, relevant
source revisions and cache age. Secret bindings remain references, never values.

| Exit | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Unexpected internal failure |
| 2 | Usage, invalid config, missing input or ambiguous ID |
| 3 | Authentication required/denied/expired |
| 4 | Disabled/unavailable connection or denied tool |
| 5 | MCP tool returned an error result |
| 6 | Connection failure, timeout, unknown outcome or runtime mismatch |
| 7 | Concurrent config conflict |
| 130 | User cancellation |

Error codes distinguish `auth_required`, `auth_expired`, `config_required`,
`config_conflict`, `connection_unavailable`, `review_required`, `tool_denied`,
`runtime_version_mismatch`, `runtime_config_mismatch`, `auth_account_conflict` and
`outcome_unknown`. `review_required` uses exit 4; `runtime_config_mismatch` exit 6;
`auth_account_conflict` exit 3.
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

Sources and counts are illustrative. Details show local input/profile binding,
transport, policy, tool selection, config revision and cached schema age. Loading
live tools is an explicit action labelled `Connect and load tools`; auth may follow.
A personal editor mirrors `local add/update`. Shared definitions are read-only.
Changes stay in a draft until Save. Cancel writes nothing. A config revision
conflict preserves the draft and offers reload; never overwrite newer CLI edits.

Small terminals use a compact list with the same keys; keyboard-only navigation,
visible focus and plain ASCII fallback are required. No mandatory mouse or color.

## Exceptional states

- No catalog: show `add <owner/repo>` and personal connection entry.
- No results: empty state with search reset; don't hide Other.
- Offline: cached metadata and revision age; already configured local MCPs still work.
- Missing input/profile: `Configuration required`, separate from enabled/reachable.
- Source removal: `Unavailable`; preserve local selection for a future restoration.
- New catalog version: review via sync; no silent command/credential changes.
  An enabled connection whose execution or auth fields changed shows `Review
  required` and cannot be called until accepted with `sync --apply --accept <mcp>`
  or `enable <mcp>`. The diff names every changed field.
- Expired auth: metadata still available; first protected call initiates auth
  (OAuth: returns `auth_required` pointing at `auth login`).
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
