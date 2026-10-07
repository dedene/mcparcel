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
startup. Use a Unix-domain socket (macOS and Linux), framed JSON requests with protocol
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
`runtime.keepAlive` is set and an OAuth session is stored (see OAuth session
health); active credential
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
commands to start it. Disable/remove/apply blocks new work immediately; a call
already dispatched runs until it ends or reaches its deadline. The next request
the daemon handles, for any connection, closes the pooled session (and any process
it owns) of every disabled, removed, under-review or changed connection once its
last call has ended. The daemon does not watch config files, so until that request
the session stays open but unreachable. Changed connections reopen lazily with the
new config. Explicit auth lock/expiry instead
cancels protected calls as specified below. Store/revision synchronization must
make a completed disable effective for every subsequent dispatch.

Safe child base environment: PATH, HOME, TMPDIR, LANG, LC_ALL and system essentials
needed on the supported platform. The daemon is started by whichever harness makes
the first runtime call, so it must not take these from that caller. In desktop
mode (headless mode: see "Headless mode" below), at start it
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
`~/.local/state/mcparcel/daemon.log` (headless: `<stateRoot>/state/daemon.log`) (startup, captured PATH, connection opens and
closes, auth session changes, elicitations (`elicitation_forwarded`, then
`elicitation_accepted`, `elicitation_declined` or `elicitation_canceled`; an
automatic decline logs `elicitation_declined` alone; event names only), errors;
never arguments, results, server messages, prompt text, answers or secret values).
`runtime status` prints its path.

## HTTP, tool discovery and results

Each MCP message is bounded at 16 MiB: a stdio line, or an HTTP response
including the notifications and requests streamed before the result (an SSE
event therefore too). A call answer over the limit is reported as
`result_too_large` (exit 6, `dispatched`, `outcome:"unknown"`) and retires the
session; a pretty-printed stdio message over the limit hits the SDK's own bound
and stays `outcome_unknown`. The standalone SSE listening stream is disabled, and
a failed tool discovery retires the session so the next call reconnects.

IPC frames the daemon reads (requests, answers) stay at 16 MiB. Response frames,
read only by the CLI, may reach 64 MiB, because `encoding/json` escapes `<`, `>`
and `&` (sixfold) when the daemon re-encodes a result. Call data over 63 MiB is
`result_too_large` without retiring the session. The CLI and the daemon check
frames as strict JSON in one pass that keeps no values. The daemon encodes a
frame before its write budget starts; the budget is 2 seconds plus 1 second per
4 MiB of frame, so a CLI that stops reading cannot hold a connection, while a
large result still arrives. A shutdown cancels a response write in flight; a
response written after its request ended (canceled or forced) is capped at the
shutdown timeout.

`auto` (and an omitted mode) uses Streamable HTTP. The MCP Go SDK v1.8.0 does
not fall back by itself and reports a mismatch as an untyped error carrying only
the HTTP status text, so when every POST of the connect (`server/discover`, then
`initialize`) is refused with 400, 404 or 405 and nothing else explains the
failure, MCParcel
sends one diagnostic GET to the endpoint (5s limit, first 4 KiB read). A
`200 text/event-stream` answer that announces `event: endpoint` makes the connect
fail with `runtime_unsupported` ("This server speaks only legacy SSE"); anything
else stays `connection_failed`. The probe sends no MCP message and nothing falls
back. `streamable` never probes. Mode `sse` is `runtime_unsupported` before any
effect: legacy SSE support is built only once a server that needs it is recorded
(the SDK's SSE client transport also has no OAuth hook). No probe after auth
failure, rate limits, server errors or general network failure, and no retry of a
failed tool execution. MCP HTTP requests never follow redirects: any 3xx answer,
or a request to another origin, is `connection_failed`, so authorization never
reaches a changed origin. Insecure internal HTTP requires explicit per-connection
consent.

`tools` and every `call` discover tools live from the connection, following
pagination with repeated-cursor detection; the SDK's list cache is disabled. There
is no schema cache yet: `tools --cached` cannot authenticate or connect and always
returns `schema_cache_miss`. A tool missing from the live list is `tool_not_found`.
`call` checks enabled state and both tool filters before resolving secrets, then
coerces and validates arguments against the live schema
(jsonschema-go, draft-07 and 2020-12, local refs only, with a nil loader so nothing
is fetched) and calls the SDK. A schema that cannot be checked safely (another
draft, remote/dynamic/unresolvable refs, a `$ref` cycle through in-place
applicators (`allOf`/`anyOf`/`oneOf`, `not`, `if`/`then`/`else`,
`dependentSchemas` and draft-07 `dependencies`), more than 1000 applicator paths,
a validator panic) is not given to the validator; the call goes out with a
`schema_unchecked` warning. `const`/`enum`/`default`/`examples` values are data,
but a property or definition with one of those names is still a schema. Before
validating, the daemon bounds the validator's work for these arguments: an upper
bound over every branch, pattern and conditional per argument node, weighted by
the size of the value checked (a failing branch prints it), at most 500,000
units; past that the call also goes out `schema_unchecked`. Type lookup for
coercion inspects at most 1024 subschemas per argument. Validator messages are
rebuilt from schema path and keyword, by unwrapping the validator's error chain
rather than parsing its text, so argument values never reach the error. No schema/tool result text is treated as CLI instructions.

Call results are kept raw: the daemon captures the server's `tools/call` answer
below the SDK's typed decoding (the stdio connection, or a tee on the HTTP
response body for that POST; 401/403 bodies belong to the OAuth path and are not
captured). An SSE body is read the way the SDK reads it: lines end at LF, field
values and event names are trimmed, and a final event that ends at EOF without
its blank line still counts. Unknown content blocks and fields are kept, numbers keep their exact
digits (big integers in `structuredContent` or `_meta`), and invalid base64 is
passed on untouched. `data.result` is that JSON compacted, with `<`, `>` and `&`
escaped by `encoding/json`. The raw result must be strict JSON (no duplicate keys,
valid UTF-8, at most 125 levels so its frame stays within the 128-level IPC limit,
`isError` absent or boolean), else `protocol_error` with `outcome:"unknown"`. A
JSON-RPC error answer becomes `server_error` (exit 6) with the cleaned message and
`rpcCode`; the session is kept unless the error came with a non-2xx HTTP status.

`--json` preserves the complete MCP CallToolResult, including structured content,
all content blocks, metadata and `isError`; do not flatten it to text. Human output
shows text and labels binary blocks. `--output-dir` explicitly exports image/audio
blocks with generated filenames, private permissions, no overwrite/path traversal,
and the `artifacts` list in `data`; the CLI does this after the daemon answers,
and the CLI rejects a daemon response that already carries `artifacts`
(`protocol_error`). Without that flag no response payload is saved to disk.

Protocol support starts with list/call, progress and cancellation. Stage 1 checks
whether real workflows need resources/prompts, roots, elicitation or sampling.
The audit (feasibility.md) found no resources, prompts, roots or sampling in use,
so none are built; the client advertises form elicitation only, and a sampling
request gets the SDK's -32601 (the call then ends however the server answers).
Do not advertise unsupported client capabilities. A required missing capability
blocks that connection's parity gate, not a quiet downgrade of the promise that
all 32 remain usable.

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
  The callback page (signed in, failed, expired, mismatch) is embedded, no JS;
  it follows the browser's light or dark preference and declares
  `color-scheme: light dark`.
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
  `keychain_unavailable`. Re-login with DCR reuses the stored client when it is
  safe (see "Client reuse" below), else registers a new one.
- Use: access token attached by the SDK; refreshed in `Token()` once a fifth of
  its lifetime (at least 30 s) remains, and on a 401 for non-tool requests (one
  resend). The refresh point is checked on the wall clock, because the
  monotonic clock stops while a Mac sleeps, and on the monotonic clock, which
  catches a wall clock moved back. A 401 on
  `tools/call` is never resent: `auth_expired`, next action "Run the call again."
  Refreshes are serialized per connection; a rotated refresh token is saved before
  the new access token is used. Refresh omits RFC 8707 `resource`.
- Failure: `invalid_grant` or another OAuth error code, or an issuer or resource
  that fetched protected-resource metadata names differently, records the failure,
  clears the refresh token and gives `auth_required`; later calls fail before any
  network request. 5xx, 429 or network errors give `connection_failed`; they are
  recorded in the health file only, never in the Keychain item. Metadata that
  cannot be fetched during a 401 is not an issuer change: the refresh goes to the
  stored token URL. A token the server rejects right after the refresh that
  minted it is a terminal failure too (`token_rejected`, persisted like
  `invalid_grant`); a late 401 for a token that a later refresh already
  replaced is retried with the current one. A 403 gives `auth_failed`.
- Shutdown: closing a handler (logout, a config change, daemon exit) waits for a
  refresh or sign-in in progress, so a rotated refresh token is saved before the
  item is deleted or a new session loads it. It also tries once more to save a
  session whose save failed, so the only in-memory copy of a rotated refresh
  token is not dropped with it; after that a closed handler never writes the
  item again.
- Unmarked HTTP connections without a credential header are OAuth-capable: a 401
  gives `auth_required` pointing at `auth login`, and a stored session is used
  when present (one Keychain read per new session). The first such
  `auth_required` from the server (call, tools or login; not `--no-input` login,
  which contacts nothing) writes a Keychain item holding only the URL, so
  `auth status` lists the connection as `sign-in required`; an existing item is
  kept, sign-in replaces it and `auth logout` removes it. Servers are never
  probed for the list. A connection with a credential
  header answers 401 as `auth_required` naming its `env:` variables; `auth login`
  rejects it (`invalid_arguments`).
- Log events: `oauth_signed_in`, `oauth_refreshed`, `oauth_refresh_failed`,
  `oauth_signed_out`, and `oauth_sign_in_failed` with `stage` (`discovery`,
  `registration`, `authorization`, `callback`, `token_exchange`, `token_save`)
  and `code`: the provider's OAuth error code when it is a plain token (e.g.
  `access_denied`, `invalid_client`), else a class such as `http_500`,
  `timeout`, `network_error`, `canceled`, `issuer_mismatch`,
  `registration_unsupported` or `<stage>_failed`. Never token, code, state,
  verifier, URL, connection name or provider text.
- Deferred: implicit login, `auth lock`,
  `tokenEndpointAuthMethod` enforcement, provider revocation.
- `client_credentials` (stage 12): `auth.grant: "client_credentials"` with
  `tokenUrl`, `clientId`, `clientSecret`, `scopes?` and
  `tokenEndpointAuthMethod?` (`client_secret_post` by default, or
  `client_secret_basic`). The token URL is used as is: no discovery, no
  registration, no Keychain item, no health record, no keep-alive. Each pooled
  session owns one in-memory token; a config change, a retired session or
  `runtime restart` drops it. Concurrent requests share one token request. A
  token is minted again when `max(lifetime/5, 10 s)`, capped at `lifetime/2`,
  remains (Front: 180 s before the 900 s expiry); one without `expires_in` is
  used until a 401. `auth login` and `auth logout` refuse such a connection
  offline (`invalid_arguments`, "there is nothing to sign in to") and
  `auth status` leaves it out. Token-endpoint failures: `invalid_client` and
  the other RFC 6749 codes give `auth_failed` naming the code; 5xx, 429,
  timeouts and network errors give `connection_failed`; no failure is cached
  past the request, and no body, secret or token appears in any output. Log
  events: `oauth_token_minted` (`trigger` `first`, `expiry` or `401`; `ttl` in
  seconds, 0 when unknown), `oauth_token_mint_failed` (`code`, `status`: the
  token endpoint's HTTP status, 0 without an answer) and
  `oauth_token_rejected` (`code` `token_rejected` or `http_403`, `status`).
  Never the connection URL, client ID, secret or token.
- 401 on a `client_credentials` connection (decision D7), `tools/call`
  included: the handler drops the rejected token, mints a new one once and the
  SDK resends the request once. If the resend is answered 401 too, the call
  fails `auth_failed` (`token_rejected`) and the session is retired; a 401
  for a token that an earlier 401 minted less than 30 s before is
  `token_rejected` at once, without another token request. A 403 never mints:
  `auth_failed`. The call's `data.result` comes from the 2xx answer to the
  resend; the 401 body is never captured. When the new token cannot be had
  (the token endpoint fails after a 401, or while a pooled session refreshes
  its token before a request), the request is not sent or resent, so the
  call fails with the token endpoint's own error (`connection_failed` for
  5xx, 429, timeouts and network errors) and is not dispatched: never
  `token_rejected` or `outcome_unknown`. `token_rejected` means a resend with
  a new token was answered 401; `auth_required` from 1Password (an `op://`
  client ID or secret under `--no-input`) stays `auth_required`.
  - Reasoning: a 401 is the resource server's authentication answer (RFC 6750
    §3.1, `invalid_token`). It arrives as the HTTP status of the POST that
    carries the JSON-RPC message, before any response body or SSE stream, so
    the server refused the request before a handler ran it: resending it
    cannot run the tool twice. The client holds no refresh token and needs no
    user, so a new token costs one request and no prompt.
  - Scope: only `client_credentials` connections. It is an exception to
    "never resend `tools/call`"; `authorization_code` connections keep
    `auth_expired` with "Run the call again.", and no other status or error
    is resent.
  - Residual assumption: the server authenticates before it executes. A
    server that runs the tool and then answers 401 would run it twice. Bearer
    token checks in HTTP middleware answer before dispatch; MCParcel cannot
    verify that for a given server, so the deployment guide names this
    assumption.

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
  logged-out connection, and can be turned off per connection
  (`lifecycle.keepAlive: "off"`).

A refresh token that the provider expires on an absolute schedule cannot be kept
alive; the health log makes that visible instead of looking like a fault. A
daemon that is not running refreshes nothing, and a closed laptop is accepted as
such. For a machine that stays on, `runtime.keepAlive: true` in `config.json`
(default `false`) disables the 24-hour idle exit while an OAuth session is
stored, so refreshing continues until the machine restarts; the next `mcparcel`
use starts the daemon again. No launchd agent is installed.

### Health log and `auth status` as built (stage 7b, task A)

- File: `<StateDir>/oauth-health.json` (`~/.local/state/mcparcel/`), mode 0600
  in the private state directory. Only the daemon writes it, through one
  in-memory copy; every change replaces the file atomically (random
  `.oauth-health-<nonce>.tmp`, `O_EXCL|O_NOFOLLOW`, fsync, rename, directory
  fsync). Writes are best effort and never fail a refresh or sign-in. The CLI
  only reads it.
- Format: `{"v":1,"connections":{"<canonical id>":{"pending":…,"client":…,
  "redirect":…,"events":[…]}}}`. An event has `at` (Unix seconds), `kind`
  (`authorized`, `refreshed`, `refresh_failed`, `reauthorization_required`,
  `logout`), and as applicable `trigger` (`call`, `keep_alive`, `start`,
  `login`), `code` (sanitized OAuth or MCParcel code), `status` (HTTP),
  `terminal`, `accessTtl` and `refreshTtl` (seconds; `refreshTtl` is the
  provider's `refresh_token_expires_in`), `refreshToken` (a sign-in got one),
  `rotated`, `idle` (seconds since the last success), `interrupted` and
  `reusedClient`. `client` is the first 12 hex characters of sha256(client ID);
  `redirect` is the loopback redirect the client was registered with. Never a
  token, code, secret, client ID or other URL.
- Bounds: 50 events per connection (newest kept); above 1 MiB the connection
  whose last event is oldest is dropped until it fits. An unreadable, unsafe,
  oversized, invalid or non-v1 file reads as empty and is replaced on the next
  write.
- Interrupted rotation: `pending` is written before every refresh request and
  cleared by a recorded success, sign-in, logout or terminal failure, and by a
  failed refresh that cannot have rotated anything: one that got an HTTP answer
  (`status` set, e.g. 503 or 429) or never reached the provider (code
  `unreachable`: the name did not resolve or the connection was refused). A
  refresh that starts while an earlier marker is still set (crash, network
  timeout or reset, or a Keychain save that failed) and gets `invalid_grant` is
  recorded with `interrupted: true`, unless the provider's last recorded
  refresh did not rotate its refresh token.
- The Keychain item stays at version 1; `LoadOAuth` rejects unknown fields, so
  history never goes there.
- `auth status`: per item `state` (`ok`, `expiring`, `sign-in required`),
  `lastRefreshAt` (last sign-in or refresh), `refreshTokenExpiresAt` (that time
  plus its `refreshTtl`, when known), `cause {code, message}` and `nextAction`
  (`mcparcel auth login <mcp>` when sign-in is required); for a signed-in
  session whose last sign-in followed a terminal failure, `previousCause
  {code, message}` says why that sign-in was needed (human output: `Last
  sign-in needed: …`); `events` only for `auth status <mcp>`. `expiring` means signed in with a known refresh-token
  expiry under 72 hours, or a last refresh that failed transiently. Never
  prints `client`, `redirect` or a token; an unreadable health file means no
  history.
- Causes, first match wins: `signed_out` (no item, last event a logout),
  `keychain_missing` (no item, but a sign-in or refresh is recorded after the
  last logout), `never_signed_in`, `server_requested` (item holds only the
  URL), `url_changed`, `interrupted_refresh`, `refresh_expired_or_revoked`
  (`invalid_grant`; the message gives the idle time, "Keep-alive was off",
  that keep-alive runs only while the daemon runs when none ran during an idle
  time longer than its interval plus 6 hours, and whether the known
  refresh-token lifetime had passed), `client_rejected`
  (`invalid_client`, `unauthorized_client`), `issuer_changed`,
  `token_rejected`, `refresh_rejected` (any other terminal code, named in the
  message), `no_refresh_token`, and `refresh_failing` (signed in, last refresh
  failed transiently). A failure recorded before the health log existed is
  explained from the Keychain item's last failure.
- Requesting refresh tokens: the SDK adds `offline_access` when the server
  advertises it, and dynamic registration asks for the `authorization_code` and
  `refresh_token` grants.
- Client reuse: a re-login presents the stored dynamically registered client as
  pre-registered when the item's URL, issuer and resource still match, its last
  failure is not `invalid_client` or `unauthorized_client`, its stored auth
  style equals the one the SDK picks from the metadata (the SDK ignores the
  registered method for a pre-registered client), and the health file still
  remembers that client with its redirect (a configured `redirectUrl` must
  equal it). The sign-in then listens on the stored redirect; if that port is
  taken, it registers a new client on a random port. Any failed sign-in with a
  reused client, timeouts and cancels included, forgets it, so a client the
  provider dropped (its `/authorize` never redirects back) costs one timed-out
  sign-in, not every one.

### Keep-alive and stay-alive as built (stage 7b, task B)

- Schedule: the daemon sweeps once at start (trigger `start`), then every 5
  minutes (`keep_alive`), one connection at a time. Targets are the runnable
  OAuth-capable connections whose `lifecycle.keepAlive` is not `"off"`. A
  target is due when its last recorded sign-in or refresh (health file, wall
  clock) is older than its interval (`lifecycle.keepAlive`, at least `1h`,
  default `24h`), when none is recorded, when its history ends in a logout or
  terminal failure, or when the clock moved back. A laptop that slept or a
  daemon that was stopped is caught up at the first sweep.
- Refresh: the sweep takes the connection's gate without waiting; a busy
  connection is retried at the next sweep. A live pooled session is refreshed
  in place, so its in-memory refresh token stays current; otherwise a
  temporary handler loads the Keychain item, refreshes and closes; if its save
  of a rotated refresh token fails, it retries after 1, 2 and 4 seconds before
  closing (and closing tries once more). A sweep that finds the daemon shutting
  down stops before loading the item. It never
  signs in, opens a browser or prompts: a client secret that is a 1Password
  reference is kept alive only through a pooled session (`auth status` shows
  `keepAlive: "unavailable"`), and `env:` client values come only from the
  captured login environment, never from the Keychain fallback. A logout or
  sign-in waits for the gate, and shutdown waits for a refresh in progress.
- Outcome: success clears any backoff. No session, a recorded terminal
  failure, a Keychain item for another URL, or an unreadable client value
  makes the connection dormant: skipped, with no Keychain read, until the
  daemon records another sign-in or refresh for it (counted, not compared by
  time, so a wall clock moved back cannot keep a new session dormant). Other failures back off 5 minutes, doubling
  up to 6 hours; a backoff more than 6 hours away (the clock moved back) is
  ignored. Every refresh lands in the health log with its trigger.
- Stay-alive: with `runtime.keepAlive: true`, an idle daemon asks whether any
  target may still hold a session (before the first sweep: yes; then any that
  is not dormant and whose history does not end in a logout or terminal
  failure). If so it restarts its idle clock instead of exiting. It keeps the
  daemon, not credential sessions: 1Password leases still end on schedule.
  `runtime status` reports `stayAlive` (`Stay-alive: on|off`).
- Locked login Keychain: a background Keychain read while the login keychain
  is locked could make macOS show an unlock dialog. Tests cannot exercise
  this; it is accepted, and the user confirms the behaviour by observation.

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

## Headless mode (stage 12)

Headless mode runs the runtime without a desktop, for a server or a
Kubernetes sidecar. The deployment guide is [headless.md](headless.md).

- Switch: `config.json` → `runtime.mode: "headless"` (default `"desktop"`).
  Never an environment variable or auto-detection. macOS supports both modes;
  Linux supports headless only: in desktop mode `tools`, `call`, `auth`,
  `runtime` and the daemon fail `runtime_unsupported` ("On Linux, MCParcel
  runs in headless mode only."), while offline commands keep working. The CLI
  and the daemon read the mode from the same `config.json`, so they agree on
  their paths.
- State root: `runtime.stateRoot` (a clean absolute path, required in
  headless mode, rejected otherwise) replaces the state, data and cache
  directories and the runtime directory with `<root>/state`, `/data`,
  `/cache` and `/run`; the configuration directory is unchanged. The root
  itself may be owned by root and world-writable without the sticky bit (a
  Kubernetes `emptyDir`, `0777` or `2777`); its ancestors and everything below
  it get the usual checks, and directories MCParcel creates below it are
  `0700` even inside a setgid root. The socket path must stay under 100 bytes.
  Accepted risk: another container that mounts the same volume can replace
  `<root>/run`.
- Read-only configuration: headless mode never writes the configuration
  directory. Every store update fails `config_read_only` (exit 2) before it
  takes the config lock, and `add` and `sync` (also the preview) fail before
  any request. Reads take no lock when `.mcparcel.lock` is absent, so the
  daemon's per-request reload works on a read-only ConfigMap; a changed
  ConfigMap applies on the next request. Config file ownership is in
  [catalog.md](catalog.md#local-state).
- Environment source: no login shell runs. The daemon's own environment is
  the source for `env:` references and `inheritEnv`, restricted to the names
  enabled connections reference plus `PATH`, `HOME`, `TMPDIR`, `LANG`,
  `LC_ALL` and `USER` (`XDG_*` and `MCPARCEL_*` names are never forwarded). A
  CLI that starts a headless daemon passes it exactly those variables from its
  own environment, with `HOME` and `XDG_CONFIG_HOME` pointing at the
  configuration. There is no Keychain fallback: a missing variable is
  `config_required` with `details.variables` (message "Environment variable
  NAME is not set.", next action to set it and run `mcparcel runtime
  restart`); the error is not cached. `runtime restart` from a caller with new
  values rotates a secret. `runtime status` shows `Environment: daemon
  environment`.
- No desktop credential source: the daemon constructs no 1Password resolver,
  no Keychain lookup and no OAuth keyring. An `op://` reference fails
  `config_required` ("1Password references need the desktop app and are not
  available in headless mode."). A connection that needs browser sign-in
  fails `auth_required` ("This server needs sign-in, which headless mode cannot
  do."), or, when its credentials are `env:` references, names those
  variables; nothing is written to a Keychain. `auth login` is refused before
  the runtime starts, `auth status` lists nothing, keep-alive and stay-alive
  are off. Only `grant: "client_credentials"` gets OAuth tokens (see "OAuth as
  built", including the 401 resend of decision D7).
- No prompts: headless behaves as if `--no-input` were given, with a terminal
  attached and with `runtime.approvalDialog` set: no terminal prompt, no
  dialog, no browser; a server's approval request is declined at once with
  the `elicitation_declined` notice.
- Policy offline: `call` checks the connection and tool policy before it
  contacts the runtime (in every mode), so a denied tool starts no daemon,
  opens no connection and requests no token. The daemon still checks at
  admission and before dispatch.
- Linux: static builds (`make build-linux`, `CGO_ENABLED=0`, amd64 and
  arm64). CLI and daemon check the socket peer's UID with `SO_PEERCRED`
  (macOS: `LOCAL_PEERCRED`).

Lifecycles:

- Auto-start, as on macOS: the first runtime command starts the daemon in a
  new session (`setsid`), with stdin, stdout and stderr on `/dev/null` and only
  the daemon lock as an extra descriptor. A wrapper that kills the CLI's
  process group after the call (claw-wrap) does not reach it, and the CLI's
  pipes close when the CLI exits. It exits after 24 idle hours. Once the CLI
  exits it is an orphan, so the container needs a reaping PID 1 (`tini`,
  `docker run --init` or a shared PID namespace); when the daemon finds itself
  PID 1 it logs `pid1_no_reaper`, and it never reaps for PID 1 itself, which
  would race `os/exec`.
- `mcparcel runtime serve`: the runtime in the foreground under a supervisor.
  It takes the daemon lock itself (`runtime_busy`, exit 6, when another
  runtime holds it), logs to the daemon log and stderr, has no idle exit, and
  applies the same environment, credential, config, peer and version rules as
  an auto-started daemon. SIGTERM or SIGINT is a forced shutdown: dispatched
  calls report `outcome_unknown`, sessions close under the usual deadline,
  the socket is removed and serve exits 0. A SIGTERM to an auto-started
  daemon does the same. Serve writes `<runtime dir>/supervised`, which stays
  after it exits: a CLI then waits up to 15 seconds for the supervised runtime
  instead of starting its own, and fails `runtime_supervised` (exit 6) if it
  does not answer; `runtime restart` (with or without `--force`) is refused
  with `runtime_supervised`; `runtime stop` still works. Delete the file to
  return to auto-start. On Linux, serve also requires headless mode.
  The file exists only once serve has taken the lock, and a fresh state root
  (an emptyDir on every pod start) has none, so a CLI that runs first would
  still auto-start a daemon. `runtime.supervised: true` in `config.json`
  (headless only) declares the supervised lifecycle in the configuration
  itself: CLIs behave as if the file were present, and the auto-start daemon
  entry point refuses to run with `runtime_supervised`.

## Failure contract

Default deadlines: connection 30s, auth 120s, call 120s (overridable per call),
owned-process graceful shutdown 5s then force. A definition's `startupTimeout`
replaces the 30s for that connection's connect, initialize and tool listing
(Codex `cua_repl` wants `120s`). The call deadline still bounds the whole call,
startup included, so raise `callTimeout` with it; `tools` keeps its 180s request
deadline. Time spent on an open approval prompt is excluded from the call
deadline (startup and credential deadlines are not paused). No automatic retry of tool calls.
Server tool errors preserve the MCP result and use exit 5; a JSON-RPC error
answer is `server_error` (exit 6, `rpcCode`, no `outcome`). A disconnect after
request dispatch uses `outcome_unknown`; an answer over the size limit
`result_too_large` and a malformed answer `protocol_error`, both with
`outcome:"unknown"`; prior-to-dispatch errors use connection or auth codes. CLI Ctrl-C cancels its call, not the daemon or other callers.

Metadata may contain private tool names and account references: protected files,
no raw debug HTTP dumps or unbounded child stderr. Redact resolved credentials from
all diagnostics, including SDK errors. Normal tool results are user-requested data;
MCParcel cannot certify that a remote tool will never return confidential content.
