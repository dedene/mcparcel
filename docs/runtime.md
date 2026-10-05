# MCParcel runtime and authentication

Review draft, 4 October 2026. Proposed implementation contract; not tested software.
See [catalog.md](catalog.md) for values and [research.md](research.md) for evidence.

## Architecture

```text
Agent / human
    |
    v
npm launcher -> native CLI -> config + local catalog metadata
                       |
                       | tools / call / live doctor only
                       v
              per-user background process
               |       |          |
               |       |          +--> OAuth + OS Keychain
               |       +--> 1Password desktop bootstrap -> service-account SDK
               +--> stdio subprocesses / HTTP MCP connections
```

Recommend Go, Kong, the official MCP Go SDK, the official 1Password Go SDK,
zalando/go-keyring v0.2.8 for the macOS Keychain, and Bubble Tea for setup. Use SDK transport/auth primitives behind small adapters;
no custom MCP protocol implementation and no dependency on mcporter at runtime.
The 1Password desktop build must have CGO enabled where the SDK requires it.

A single native binary has a private daemon entrypoint. Metadata commands do not
start it. First runtime call starts it under a lock; other callers join the same
startup. Use a Unix-domain socket on macOS, framed JSON requests with protocol
version/request ID, and cancellation tied to CLI disconnect. Validate socket
ownership and peer UID. Same-OS-user software remains inside the trust boundary.

Copy the verified packaged binary to its private versioned data path before
starting it, so npm-cache cleanup cannot remove the daemon's executable. Different
CLI versions negotiate protocol compatibility. Refuse incompatible versions with
`runtime_version_mismatch` and `mcparcel runtime restart`; never kill active work
or launch a second credential daemon silently. Restart refuses active calls unless
`--force` explicitly cancels them, with unknown outcomes reported.

`runtime stop [--force]` uses the same shutdown path as restart and leaves no
daemon behind; on a stopped runtime it succeeds without creating anything.
Shutdown closes all owned sessions in parallel under one deadline and reaps their
process groups.

The handshake carries a SHA-256 of the absolute configuration directory. A caller
whose directory differs from the daemon's gets `runtime_config_mismatch` (exit 6,
next action `mcparcel runtime restart`); `restart` and `stop` intents are exempt
so they always work. One daemon serves one 1Password desktop account for its
lifetime: a profile naming another account fails with `auth_account_conflict`
(exit 3) until the runtime restarts.

Daemon exits after 24 hours with no requests and no active calls, unless
`runtime.keepAlive` is set (see OAuth session health); active credential
sessions can expire earlier. It is not registered as a permanent launch agent in v1.
No network listener or hosted gateway, no telemetry, and no arbitrary shell RPC.

## Connection ownership and concurrency

Pool connections by canonical ID + effective config hash + credential identity.
One initialization per key; serialize tool calls per connection by default to
protect stateful stdio servers. Different connections run concurrently. Never let
one caller's cancellation abort another caller's request or shared auth attempt.
An auth prompt stays alive while any waiting caller remains, within a 120s limit.

Retain stdio sessions by default so reasoning/browser state survives individual CLI
calls. An optional idle timeout is explicit and visible in `inspect`. Restart and
credential refresh may lose server state; report this instead of silently replaying.
Paper and other externally owned apps are connected to, never started or killed
unless their definition explicitly defines a managed process.

Before admission and immediately before dispatch, read the current config revision.
An already running daemon observes metadata changes without requiring metadata
commands to start it. Disable/remove/apply blocks new work immediately; existing
calls drain until their deadline, then owned affected connections close. Changed
connections reopen lazily with the new config. Explicit auth lock/expiry instead
cancels protected calls as specified below. Store/revision synchronization must
make a completed disable effective for every subsequent dispatch.

Safe child base environment: PATH, HOME, TMPDIR, LANG, LC_ALL and system essentials
needed on the supported platform. The daemon is started by whichever harness makes
the first runtime call, so it must not take these from that caller: at start it
runs the user's login shell once (`$SHELL -l -c`, 10s limit, no rc output parsed
beyond a delimited `env` dump) and keeps that environment as the source for PATH
and for every `inheritEnv` name. If the capture fails, it falls back to the
caller's PATH and says so in `runtime status` and the daemon log. `runtime status`
prints the captured PATH; `runtime restart` recaptures it. On top of the base it
adds explicit non-secret inherited names, configured literals and resolved
bindings. An `env:NAME` reference (personal definitions, stdio env and HTTP headers)
resolves at connect time from that captured environment, else from the Keychain
generic password with service NAME and the login user as account
(`/usr/bin/security find-generic-password`, argv, 5s limit). Neither present gives
`config_required` naming the variable, with both fixes. Values are never logged or
persisted. A pooled session keeps its value until it reconnects; `runtime restart`
recaptures the environment and is how a key is rotated. `auth_required` on a
connection whose only credentials are `env:` references names those variables
instead of 1Password. Never forward `OP_SERVICE_ACCOUNT_TOKEN`, GitHub credentials
or other unrelated parent variables. `inheritEnv` cannot override protected credential names.
Known auth dependencies such as octocode's GitHub access need explicit bindings or
supported server-owned auth, demonstrated in the 32-server acceptance matrix.

Spawn executable + argv directly, no shell. Keep server stdout as protocol data;
server stderr is bounded and redacted, excluded from JSON stdout. Process groups
permit cleanup of owned descendants. MCParcel does not rewrite a definition's
arguments, so it cannot inject a container name. For `docker run --rm -i`, closing
stdin and signalling the client normally stops the container because the client
proxies signals; stage 5 verifies this for the one Docker definition. If the
container survives, its definition must name the container (`--name` or
`--cidfile` in its own args) and declare that name for cleanup. Never
enumerate/stop unrelated containers.

The daemon writes a size-bounded, redacted log to
`~/.local/state/mcparcel/daemon.log` (startup, captured PATH, connection opens and
closes, auth session changes, elicitations (`elicitation_forwarded`, then
`elicitation_accepted`, `elicitation_declined` or `elicitation_canceled`; an
automatic decline logs `elicitation_declined` alone; event names only), errors;
never arguments, results, server messages, prompt text, answers or secret values).
`runtime status` prints its path.

## HTTP, tool discovery and results

HTTP responses and SSE events are bounded at 16 MiB; the standalone SSE
listening stream is disabled, and a failed tool discovery retires the session so
the next call reconnects.

Try Streamable HTTP initialization first for `auto`; use legacy SSE only for a
recognized transport mismatch, before any tool call. The MCP Go SDK v1.8.0 does
not fall back by itself and reports a mismatch as an untyped error carrying only
the HTTP status text, so `auto` uses MCParcel's own pre-flight request to
recognize a mismatch. Its SSE client transport also has no OAuth hook. Legacy SSE
support is built only if the stage-1 probe finds a server that needs it. No fallback on auth failure,
rate limits, general network failure or a failed tool execution. Persist negotiated
mode in the connection state. Validate redirects; never forward authorization to
a changed origin. Insecure internal HTTP requires explicit per-connection consent.

`tools` follows pagination with repeated-cursor detection and updates an identity-
and config-scoped schema cache. `tools --cached` cannot authenticate or connect.
A list-changed notification invalidates the cache; missing tools get a precise error.
`call` checks enabled state and both tool filters before resolving secrets, then
validates arguments against the live/cached current schema and calls the SDK.
No schema/tool result text is treated as CLI instructions.

`--json` preserves the complete MCP CallToolResult, including structured content,
all content blocks, metadata and `isError`; do not flatten it to text. Human output
shows text and labels binary blocks. `--output-dir` explicitly exports image/audio
blocks with generated filenames, private permissions, no overwrite/path traversal,
and manifests; without that flag no response payload is saved to disk.

Protocol support starts with list/call, progress and cancellation. Stage 1 checks
whether real workflows need resources/prompts, roots, elicitation or sampling.
Provide resource/prompt operations if observed; do not advertise unsupported client
capabilities. A required missing capability blocks that connection's parity gate,
not a quiet downgrade of the promise that all 32 remain usable.

Elicitation (stage 8b): every connection advertises form elicitation, because some
servers (Codex `cua_repl`) refuse to work without it and answer approved apps
from their own saved approvals. MCParcel answers `elicitation/create` itself,
before the SDK validates it, and never accepts without the user's answer:

- Routing. A request is forwarded only to the CLI whose `tools/call` is in flight
  on that session. Calls are serialized per pooled session, so there is at most
  one. A request with no call in flight (connect, tool listing, after the call
  returned) or from a caller that cannot prompt is declined, as before.
- Who can prompt. The call's `request` frame carries `prompt`: `terminal`,
  `dialog` or absent (decline). Never together with `noInput`, only on `call`.
- Frames. The daemon sends one `elicit` frame (`promptId`, 32 hex characters, and
  an already cleaned, capped prompt: message, subtitle, `riskLevel`,
  `tool_params_display` as details, offered `persist` (`session` only), fields)
  on the call's socket after `dispatch`; the CLI answers with one `elicit_answer` frame (`promptId`,
  `action`, `persist`, `content`). Both are capped at 64 KiB and decoded strictly;
  a prompt that would not fit is declined as unsupported. One prompt at a time per
  socket. An answer whose ID is not the open prompt's (stale, duplicate) is dropped;
  an answer on a call that cannot prompt is a `protocol_error`. Before relaying,
  the answer is checked against the prompt it answers (persistence offered, form
  content types, required fields, enum values); a failed check becomes `cancel`.
  Accept goes to the server with `content` for a form and `_meta.persist` when the
  user picked a persistence. Version mismatch fails closed: binaries must match
  exactly in the handshake, and an older daemon rejects the unknown field.
- Scope. Approvals (no schema, or an object schema without properties) and forms
  of flat string, number, integer, boolean and string-enum fields. A persistence
  offer is shown only for an approval. URL mode, nested or other schemas, and any
  form sent to a dialog are declined as unsupported without asking.
- Choices. Decline, Allow once, and Allow for this session when the server's
  `_meta.persist` lists `session`. A listed `always` is dropped: Codex computer
  use does not store an approval it receives (it reads `_meta.persist` only for
  its statistics; the Codex app writes lasting approvals), so MCParcel offers no
  lasting approval; approve an app for good in the server's own app. A prompt or
  `elicit_answer` frame carrying `always` is invalid and fails closed.
- Details. A `tool_params_display` array of objects, each with a string
  `display_name` (else `name`) and a string, number or boolean `value`, becomes
  `Name: value` items joined by `; `, each cleaned, under the same 500-character
  cap; an empty array shows nothing. A string is shown as is and any other shape
  as compact JSON.
- Lifecycle. While a prompt is open the call deadline is paused. The CLI closes
  its prompt after 5 minutes and answers `cancel`; the daemon's own backstop is
  5 minutes 5 seconds. A CLI disconnect (such as a closed terminal) or Ctrl-C
  answers `cancel` and also cancels the call itself, which retires the pooled
  session. The CLI discards terminal input typed before a prompt opens. Other connections
  and other callers are not blocked while a prompt is open.

Anything but an accept during a call is reported with the server's message
(untrusted: one line, control and format characters removed, 300 characters) as
`elicitation_declined`, worded by reason (no prompt possible, unsupported, declined
by the user, canceled; see cli.md): a `warnings` entry beside an unchanged result,
or the error when the server answered the call with a JSON-RPC error. That error
keeps the pooled session. Only the first non-accept of a call is reported; one
outside a call is declined silently. Servers on protocol 2026-07-28 ask through
multi-round-trip results instead, which still end in `input_required`.

Residual risk: the terminal check cannot tell a person from an agent whose shell
tool allocates a pseudo-terminal and types the answer, and a computer-use or
accessibility agent can click the native dialog. The daemon trusts the prompt
mode a client declares, so any same-user process can speak the socket protocol
and answer its own prompt; `--no-input` binds only cooperating callers. No code
closes this gap inside the same-user trust boundary; the dialog is opt-in for
that reason, and an agent that must not grant approvals needs OS-level isolation
(another user, or a sandbox that denies the socket).

`call --meta` sends a validated JSON object as the `tools/call` `_meta`; the SDK
adds its own `io.modelcontextprotocol/*` keys under protocol 2026-07-28.

Handshake: stdio connections start with the classic `initialize` (2025-11-25) and
never send the newer `server/discover` probe, because a strict stdio server may
ignore the probe or exit on it (Codex's `cua_repl` exits). HTTP connections let SDK
v1.8.0 probe first and fall back to `initialize` on an error reply.

## 1Password sessions

Default recommendation: individually revocable, read-only service accounts for
team access, provisioned by an administrator. Store each bootstrap token in a vault
that its intended user can read through the desktop app. MCParcel neither creates
accounts nor grants vault access. Profile metadata is personal; catalogs only
refer to a credential requirement. A shared bootstrap is possible but has weaker
individual revocation and audit; do not make it the automatic default.

On the first protected call:

1. Bind the connection to its local credential profile and check selection/policy.
2. Use desktop SDK authentication to read that profile's bootstrap reference.
3. Create the service-account SDK client in daemon memory; destroy the desktop
   client reference when bootstrap completes. Do not persist or export the token.
4. Resolve only the secrets required for this connection. Deduplicate concurrent
   identical references and keep values in a cache for at most 5 minutes.
5. Admit calls until the profile session expires after 24 hours by default.
   Credential refresh does not extend the session. Shorter durations are allowed.

The bootstrap reference can live in a private vault, but service accounts cannot
read built-in Personal/Private/Employee vaults. API keys for service-account access
must live in a suitable explicitly accessible vault. Personal integrations can
use their own profile/vault. A `desktop` profile is an explicit fallback for keys
that cannot move, with no promise of 24-hour prompt-free access. An `environment`
profile for CI is deferred from v1; when added it explicitly names a token env
variable and is never inferred automatically.
`--no-input` forbids both desktop prompts and browser login, returning `auth_required`.

Expiry uses elapsed time plus a wall deadline; clock rollback cannot extend a
session. Check before every admission and after sleep/resume. On expiry or
`auth lock`, reject new protected work, cancel in-flight protected calls, close
protected transports, stop owned protected subprocesses and discard credential
references. Cancellation cannot reverse remote effects; report `outcome_unknown`
where dispatch occurred. Secret-free Paper remains available.

Go/SDK memory cannot guarantee forensic zeroization; avoid claiming otherwise.
OS swap/core dumps and child process memory remain outside a plaintext-config ban.

## Refresh, failure and offboarding

Before a protected call, ensure its credential lease is current (maximum 5 minutes).
If its resolved values changed, drain existing work and reconnect before admitting
new work. If revalidation fails, deny new work on that connection even if an old
process still has credentials. Bound drain by the active call's timeout; no new
work enters while rotating. `auth refresh <mcp>` invalidates the lease explicitly.
API-key creation at providers and automatic service-account provisioning are out
of scope; updating the 1Password item is supported.

A bootstrap token change takes effect at next bootstrap (or lock/re-authorize),
not by silently polling the user's private vault. A rejected/revoked service account
ends that profile's session; do not retry indefinitely or silently switch identities.
SDK retry/backoff must honor context deadlines and rate limits. Never replay a
`tools/call` automatically, including after a 401 or timeout with uncertain effects.
A fresh authorization can prepare the next user-initiated call.

Removing a person from 1Password alone does not revoke a separately issued service
account or copied provider key. Administrative offboarding must revoke that person's
service account and appropriate provider/OAuth access. With shared accounts, rotate
shared tokens/keys as required. The 24-hour window is local convenience policy, not
server-enforced membership validation. A central broker for immediate membership
checks is a different deployment and explicitly deferred.

## OAuth recommendation for review

Keep distributed API keys and OAuth client secrets in 1Password. Store each user's
OAuth refresh token, registration credentials and issuer/resource binding in macOS
Keychain; access tokens stay in memory. Storage uses `zalando/go-keyring` v0.2.8, which
reads and writes through Apple's `/usr/bin/security` tool: no Developer ID
signature is required and an upgrade causes no Keychain prompt. Accepted cost: any
process running as the user can read these items the same way. This is a proposed exception for personal
OAuth session state, not permission to put shared API keys in local files. Do not
use a plaintext fallback if Keychain is unavailable. Pin the storage backend.

Why: refreshing a user's OAuth session should not require writing a shared vault or
race another user's token. Keeping refresh state in 1Password instead would require
per-user writable items and refresh coordination; defer that alternative unless
review rejects Keychain. The ten OAuth connections remain required either way.

Build on the SDK's `auth.AuthorizationCodeHandler`: it supports a pre-registered
client, client-ID metadata documents and dynamic registration, a fixed redirect
URL, and token persistence hooks. It chooses the token-endpoint auth method from
the server's metadata (`client_secret_post` before `client_secret_basic`) and has
no override. A definition's `tokenEndpointAuthMethod` goes into dynamic
registration only; enforcing it (filtering the advertised methods) is deferred.

Use authorization code + PKCE where applicable, unique state, validated loopback
callback and issuer/resource/audience binding. Preserve registered redirect URLs and
client authentication method from existing definitions. Dynamic registration and
refresh token rotation need fixture coverage; never invent a generic callback for
a server with a registered fixed URL. Do not import mcporter's token cache.

Serialize refresh per token identity; save rotated refresh tokens atomically before
other calls can use them. Login is explicit in this release: `mcparcel auth login
<mcp>`. A call without a usable stored session returns `auth_required` with next
action `mcparcel auth login <mcp>`; implicit browser login on first need is
deferred. Headless calls return structured instructions, never hang. A callback port conflict gives
an actionable error; don't silently substitute a nonregistered port.

OAuth-only profiles use their provider token lifetime and Keychain session, without
an unrelated 1Password prompt. If OAuth needs a 1Password client secret, its calls
also require that credential profile session. `auth lock` clears all in-memory auth
and sets a persistent local locked flag for OAuth. A locked connection cannot reuse
its stored tokens: the next interactive call starts a fresh browser authorization;
`--no-input` fails. Clear the flag only after successful authorization. This is
deliberately independent of whether Keychain would permit a silent read.
`auth logout <mcp>` removes local OAuth tokens/registration; provider-side revocation
is a separate capability, reported accurately (`providerRevoked` is always false
for now). `auth status` reads the Keychain item locally, never starts the runtime
and prints no token. `auth lock` is not built yet.

### OAuth as built (stage 7 core)

- Daemon only. `auth login` sends IPC `login`; the daemon connects with an OAuth
  handler in login mode, streams the authorization URL to the CLI (`auth_url`
  frame), and the CLI prints it on stderr and runs `/usr/bin/open` (argv, https or
  loopback http only). 10-minute deadline; a pending login holds the connection's
  gate, so its calls queue; Ctrl-C cancels. `--no-input` returns `auth_required`
  after the connection check (unknown: `connection_unavailable`; not sign-in
  capable: `invalid_arguments`). A login the server never challenges stores
  nothing and does not keep its session.
- Flow: SDK v1.8.0 `AuthorizationCodeHandler`. PRM + AS discovery, preconfigured
  client (`clientId`/`clientSecret`, env refs allowed) else DCR, PKCE S256, state,
  RFC 9207 `iss`, issuer/resource binding. Loopback listener on 127.0.0.1 (random
  port unless `redirectUrl` fixes it); port in use: `auth_callback_unavailable`.
  The callback page (signed in, failed, expired, mismatch) is embedded, no JS.
  The OAuth HTTP client never follows a redirect of a token or registration
  request; metadata GETs follow at most 10, never to http from https or into
  loopback.
- No PRM and no valid authorization-server metadata at the origin (some servers,
  e.g. self-hosted GlitchTip, name the MCP URL itself as issuer): the MCP URL is
  tried as issuer at its path-inserted well-known URLs, with the same strict
  issuer check, and becomes the stored issuer and resource. The SDK only takes
  the authorization server from PRM or the origin, so for this sign-in its
  client answers the path-inserted PRM URL with a document naming the MCP URL;
  its other metadata GETs keep the SDK's non-public-address dial check.
- Keychain item: service `mcparcel-oauth`, account = canonical connection ID, one
  compact JSON value: URL, issuer, resource, token URL, auth style, DCR client ID
  and secret (never a preconfigured one), refresh token, last access expiry, last
  terminal failure `{at, code}`. Access tokens stay in daemon memory. Budget about
  2.8 KB of JSON (go-keyring's 4096-byte `security -i` line, checked before Set);
  larger: `keychain_unavailable`, no split. No plaintext fallback. Keychain errors:
  `keychain_unavailable`. Re-login with DCR registers a new client.
- Use: access token attached by the SDK; refreshed in `Token()` when it expires
  within 30 s, and on a 401 for non-tool requests (one resend). A 401 on
  `tools/call` is never resent: `auth_expired`, next action "Run the call again."
  Refreshes are serialized per connection; a rotated refresh token is saved before
  the new access token is used. Refresh omits RFC 8707 `resource`.
- Failure: `invalid_grant` or another OAuth error code, or an issuer or resource
  that fetched protected-resource metadata names differently, records the failure,
  clears the refresh token and gives `auth_required`; later calls fail before any
  network request. 5xx, 429 or network errors give `connection_failed` and record
  nothing. Metadata that cannot be fetched during a 401 is not an issuer change:
  the refresh goes to the stored token URL. A token the server rejects right
  after a refresh ends that session with `auth_required`, without another
  refresh; a 403 gives `auth_failed`.
- Unmarked HTTP connections without a credential header are OAuth-capable: a 401
  gives `auth_required` pointing at `auth login`, and a stored session is used
  when present (one Keychain read per new session). A connection with a credential
  header answers 401 as `auth_required` naming its `env:` variables; `auth login`
  rejects it (`invalid_arguments`).
- Log events: `oauth_signed_in`, `oauth_refreshed`, `oauth_refresh_failed`,
  `oauth_signed_out`. Never token, code, verifier or provider text.
- Deferred: implicit login, keep-alive refresh, `auth lock`, failure history,
  client reuse, `tokenEndpointAuthMethod` enforcement, provider revocation.

## OAuth session health

Evidence from mcporter's credential cache on 4 October 2026 (field names,
booleans and timestamps only): all eleven OAuth entries hold a refresh token, so
"the provider gives no refresh token" is not why users have to sign in again.
Access tokens live between 10 minutes and 90 days depending on the provider; four
providers issue tokens that last an hour or less. Some entries were last updated
months ago. The cache records no refresh attempts or failures, so the cause of a
re-login cannot be read back afterwards. The cache is a plaintext file readable
by other local users.

Likely causes, most probable first; stage 7b's health log is what confirms them:

1. Refresh-token rotation raced by several processes. Each agent call can run its
   own client; two of them refresh the same token, the provider rotates it, and
   the loser holds a refresh token that is now revoked (many providers then
   revoke the whole token family). Short-lived access tokens make this frequent.
2. Idle expiry. A refresh token that is not used for the provider's idle window
   expires; rarely used connections hit this.
3. A refresh response that was received but not saved, after which the stored
   refresh token is stale.
4. A dynamic client registration that the provider dropped or that was not reused.

MCParcel's answer:

- One refresher. Only the daemon refreshes, serialized per token identity, and it
  stores a rotated refresh token before anyone else can use the session.
- Refresh early. Refresh when 20% of the access-token lifetime remains rather
  than on a 401, and keep idle sessions inside the provider's idle window by
  refreshing them periodically while the daemon runs and once at daemon start.
- Remember why. Every authorization, refresh and failure is recorded per
  connection with the provider's OAuth error code and no token material.
  `auth status <mcp>` turns that into a cause and a next action.
- Ask for durable sessions. Request `offline_access` where advertised and reuse
  the stored client registration on re-login.
- Never surprise. Keep-alive never opens a browser, never refreshes a locked or
  logged-out connection, and can be turned off per connection.

A refresh token that the provider expires on an absolute schedule cannot be kept
alive; the health log makes that visible instead of looking like a fault. A
daemon that is not running refreshes nothing, and a closed laptop is accepted as
such. For a machine that stays on, `runtime.keepAlive: true` in `config.json`
(default `false`) disables the 24-hour idle exit while an OAuth session is
stored, so refreshing continues until the machine restarts; the next `mcparcel`
use starts the daemon again. No launchd agent is installed.

`runtime.approvalDialog: true` in `config.json` (default `false`) lets a call
without a terminal (or with `--json`, never with `--no-input`) show a server's
approval request as a native macOS dialog. The CLI runs `/usr/bin/osascript`
with a fixed script and passes the title, text, timeout and button labels as argv,
never as script source. Approvals only (forms are declined), with buttons
Decline (default), Allow once, and Allow for this session when offered; it
gives up after 5 minutes, which answers `cancel`. The CLI reads the setting only
when it cannot prompt on the terminal; an unreadable config means no dialog.

The loopback callback serves one self-contained page with no external requests:
signed in, failed, expired, and state mismatch. Provider-supplied text is
HTML-escaped. The page carries MCParcel's own identity; catalogs cannot restyle it.

## Failure contract

Default deadlines: connection 30s, auth 120s, call 120s (overridable per call),
owned-process graceful shutdown 5s then force. A definition's `startupTimeout`
replaces the 30s for that connection's connect, initialize and tool listing
(Codex `cua_repl` wants `120s`). The call deadline still bounds the whole call,
startup included, so raise `callTimeout` with it; `tools` keeps its 180s request
deadline. Time spent on an open approval prompt is excluded from the call
deadline (startup and credential deadlines are not paused). No automatic retry of tool calls.
Server tool errors preserve the MCP result and use exit 5. A disconnect after
request dispatch uses `outcome_unknown`; prior-to-dispatch errors use connection
or auth codes. CLI Ctrl-C cancels its call, not the daemon or other callers.

Metadata may contain private tool names and account references: protected files,
no raw debug HTTP dumps or unbounded child stderr. Redact resolved credentials from
all diagnostics, including SDK errors. Normal tool results are user-requested data;
MCParcel cannot certify that a remote tool will never return confidential content.
