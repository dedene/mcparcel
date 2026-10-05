# MCParcel catalog and local configuration

Review draft, 4 October 2026. Proposed v1 contract, not implemented.
This resolves the configuration questions in [product.md](product.md) and
[cli.md](cli.md). Examples are fictional and contain no operational credentials.

## Decisions proposed for v1

- JSON, with `schemaVersion: 1`, strict decoding and a published JSON Schema.
  JSON keeps authoring, validation and agent edits straightforward. No executable
  config, interpolation language, YAML aliases, imports or plugins in v1.
- One `mcparcel.json` at the repository root. `add --path` supports an existing
  repository that needs another location. No recursive catalog discovery.
- A catalog defines connections directly: transport + credential requirements +
  domains. Do not introduce a separate reusable server-template registry yet.
- A connection has one enabled state, independent of domain tabs. Tool selection
  further narrows that connection. Domains organize; they do not grant permission.
- Everything starts disabled. An import can preserve the user's explicitly
  reviewed active selection; adding or syncing a catalog never enables new entries.
- Personal configuration binds local inputs and credential profiles without
  modifying the team definition. Arbitrary endpoint/command replacement requires
  a separate personal connection, avoiding invisible team-definition overrides.

## Shared catalog example

```json
{
  "schemaVersion": 1,
  "name": "Example team",
  "domains": {
    "design": {"label": "Design"},
    "research": {"label": "Research"}
  },
  "credentialProfiles": {
    "team": {"description": "Team API credentials from 1Password"}
  },
  "connections": {
    "paper": {
      "label": "Paper",
      "description": "Connect to Paper on this device",
      "domains": ["design"],
      "inputs": {
        "endpoint": {"kind": "url", "description": "Local Paper MCP endpoint"}
      },
      "transport": {
        "type": "http",
        "url": {"input": "endpoint"},
        "mode": "auto",
        "allowInsecureHttp": "loopback"
      }
    },
    "perplexity": {
      "label": "Perplexity",
      "domains": ["research"],
      "credentialProfile": "team",
      "transport": {
        "type": "stdio",
        "command": "npx",
        "args": ["-y", "example-perplexity-mcp@1.0.0"],
        "env": {
          "PERPLEXITY_API_KEY": {"secret": "op://example-vault/perplexity/api-key"},
          "npm_config_loglevel": "error"
        }
      }
    },
    "figma": {
      "label": "Figma",
      "domains": ["design"],
      "transport": {"type": "http", "url": "https://figma.example.invalid/mcp"},
      "auth": {"type": "oauth", "clientName": "MCParcel"}
    }
  }
}
```

The package name and endpoints above are deliberately non-operational. Obtain
actual transport details from the local migration, not these documentation values.
[All 32 existing connections](compatibility.md) must have sanitized fixtures before
accepting the schema. Paper's real URL stays a local value, not a fabricated default.

## Field contract

Identifiers use lowercase letters, digits and hyphens; existing 32 aliases fit.
Catalog domains are a map of ID to `{label}`; `other` is reserved for uncategorized
connections. Same domain IDs merge across sources (the first registered source's
label wins); different IDs remain distinct. A connection can name multiple domains.
Duplicate JSON keys, unknown fields, undeclared inputs/profiles/domains, invalid
unions and unsupported schema versions are errors. No ignored config fields.

| Field | Contract |
| --- | --- |
| `connections.<id>.label`, `description` | Optional display strings; label defaults to ID |
| `domains` | Optional array of declared IDs; empty appears under Other |
| `inputs` | Map of local, non-secret values: `kind` = `string`, `path` or `url`, optional `default`, required `description` |
| `credentialProfile` | Optional declared profile ID, required if any secret binding exists |
| `transport.type` | `stdio` or `http`; exactly one transport shape |
| stdio `command` | Literal executable name or `{input}` for a user-specific absolute path |
| stdio `args` | Ordered array of literal strings or `{input}`; empty by default |
| stdio `env` | Map of names to Value; empty by default |
| stdio `cwd` | Optional literal absolute path or `{input}`; defaults to user's home, never the daemon's launch directory |
| stdio `inheritEnv` | Optional explicit list beyond the safe base environment; no wildcard |
| HTTP `url` | Literal URL or `{input}`; no embedded username/password |
| HTTP `headers` | Map to Value; protected OAuth Authorization header cannot also be configured here |
| HTTP `mode` | `auto` (default), `streamable` or `sse` |
| HTTP `allowInsecureHttp` | `never` default; `loopback`, or `explicit` for a deliberately configured internal endpoint |
| `auth` | Omitted or `{type: "oauth", ...}`; API-key auth uses env/header bindings |
| `toolPolicy` | Optional `{allow: [exact names], deny: [exact names]}`; omitted allow means all, empty allow means none; deny wins |
| `lifecycle` | Optional `{idleTimeout: "session"}` default, or positive duration; session keeps used processes until daemon stop/auth expiry |
| `callTimeout` | Positive duration, default `120s`; override via CLI per call |

Value is exactly one of: a literal string, `{input: "name"}`, or
`{secret: "op://vault/item/field", prefix?: "Bearer ", suffix?: ""}`.
Secret bindings are allowed in env, HTTP headers and OAuth client fields only.
Personal definitions may also use `{secret: "env:NAME", prefix?, suffix?}`: the
daemon resolves NAME at connect time from its captured login environment, falling
back to the Keychain generic password with service NAME and the login user as
account. Temporary bridge until 1Password profiles (stage 6). Allowed in stdio env
and HTTP headers only, not OAuth client fields; protected variable names are
rejected. A GitHub catalog containing one fails `add`/`sync` with `invalid_catalog`.
`config validate` checks format only; it does not apply the personal-only rule.
No secret command-line arguments. Import of a credential embedded in argv is
flagged for an equivalent env/header-based configuration; never silently exposed.
No shell expansion, `${...}` expansion or command substitution; only an explicit
`env:NAME` reference reads the environment.

OAuth fields: `clientName`, `scopes` (array), `clientId` (Value), `clientSecret`
(Value), `tokenEndpointAuthMethod` (`none`, `client_secret_basic`, `client_secret_post`),
`redirectUrl` (optional explicit callback), `issuerUrl` (optional explicit override).
Use discovery otherwise; bind state/tokens to the discovered issuer and resource.
Missing registered client information is an actionable error if registration is
unavailable. Do not assume `clientName` is interchangeable with a registered ID.

## Source registration and immutable revisions

`add owner/repo` resolves the default branch through GitHub, reads `mcparcel.json`
at one immutable commit, validates it and registers its repository ID, owner/name,
path, tracked ref and commit. `--ref` accepts a branch, tag or commit; commit-pinned
sources do not advance. `--path` must be a repository-relative regular file without
`..`; reject symlinks/submodules and files above 2 MiB. Renamed/deleted repositories
are reported; a replacement repository with the old name needs explicit re-add.

Canonical connection ID: `github:owner/repo#connection-id`; personal ID:
`local:connection-id`. Only one catalog path/ref per GitHub repository in v1.
Repository numeric identity is also stored so name reuse cannot hijack a source.
Short names work if unambiguous. Import creates explicit personal aliases so a
later same-named team entry does not retarget an existing `paper` command.

Public repositories use GitHub HTTPS API directly. Private ones use an installed,
authenticated `gh api` process for repository metadata, commit resolution and content
fetches. No GitHub token copied into MCParcel storage or shared with MCP subprocesses.
Missing `gh`/login gives instructions; adding a public catalog does not require it.
This is a stated prerequisite for private catalogs, not a new silent login flow.

Fetch validation is strict: GitHub responses with case-variant duplicates of a consumed
field, truncated trees, or catalog content that is not valid UTF-8 are rejected and the
saved snapshot stays active. Config and import files must also be valid UTF-8.

`sync` fetches, validates and displays a candidate diff without changing the active
snapshot. `sync --apply` performs that review and applies the candidate explicitly.
Record the exact candidate commit in output. Compare-and-swap prevents a concurrent
update overwriting another user's edit. No background catalog update in v1.
Each source updates atomically; multi-source sync reports per-source success/failure.
A failed source retains its previous snapshot and gives a nonzero overall exit code.

Changed connection execution/auth config invalidates the connection and its schema
cache. If that connection is enabled, apply also puts it in `review_required`:
it stays selected but cannot be called until the user accepts it by ID with
`sync --apply --accept <id>` or `enable <id>`. Execution/auth config means
transport type, command, args, env, cwd, inheritEnv, URL, headers, HTTP mode,
insecure-HTTP policy, `auth`, `credentialProfile`, and any widening of `toolPolicy`.
Label, description and domain changes do not block. This keeps an unattended
`sync --apply` from running a changed command or sending a secret to a changed
endpoint. It is not access control: whoever can change the catalog's tracked
branch can reference any secret the bound profile can read, so protect that branch. Removed entries remain visible as unavailable selections and cannot execute.
Unchanged entries keep settings. New IDs are disabled. Treat renames as remove/add;
no fuzzy matching. Offline access uses the last valid snapshot with its age shown.
No cache TTL is presented as enforcement of centrally revoked access.

## Local state

macOS-first paths (respect explicit `XDG_CONFIG_HOME`, `XDG_DATA_HOME` and
`XDG_CACHE_HOME` when set):

- Config: `~/.config/mcparcel/config.json` — sources, credential profiles, aliases
  and runtime defaults.
- Personal definitions: `~/.config/mcparcel/personal.json` — same catalog schema.
- Selection: `~/.config/mcparcel/selections.json` — revision, enabled IDs, input
  bindings, per-connection profile bindings and disabled tools.
- Data: `~/.local/share/mcparcel/catalogs/<source-id>/<commit>.json` — snapshots.
- Data: `~/.local/share/mcparcel/runtime/<version>/mcparcel` — retained daemon binary.
- Cache: `~/.cache/mcparcel/schemas/` — protected schema metadata by connection,
  auth identity and config hash; never tool results.
- Runtime: private short directory beneath the OS user temp directory — lock/socket;
  verify owner, reject symlinks, directories `0700`, socket/files `0600`.
  `MCPARCEL_RUNTIME_DIR` overrides it; a non-clean absolute value is cleaned, a
  relative one is rejected.
- State: `~/.local/state/mcparcel/daemon.log` — size-bounded, redacted daemon log
  (respects `XDG_STATE_HOME`).

Local profile example (reference and account name are placeholders):

```json
{
  "mode": "desktop-service-account",
  "account": "Example account",
  "bootstrapRef": "op://Private/mcparcel-bootstrap/token",
  "sessionDuration": "24h"
}
```

This object is stored at `config.json.credentialProfiles.<local-profile-id>`.
Profile tagged union:
`desktop-service-account` requires `account` and `bootstrapRef`;
`desktop` requires `account` and has no service-account prompt-free guarantee.
An `environment` profile for CI (token from a named env variable) is deferred
from v1; the union leaves room for it. Both may specify
`sessionDuration` (default `24h`, positive and at most `24h` in v1). Credential
cache/lease maximum is `5m`; shortening a lease never extends a session. Only local
config can define profiles. Validate an `op://` bootstrap reference without resolving it.

Selections bind a catalog's `team` requirement to that local profile ID. Different
catalogs can use different profiles/accounts without trusting a catalog to choose
a bootstrap token or broaden vault access. Setup collects missing bindings when
an entry is enabled; it does not read any secret or open an auth prompt.

`config.json` also holds `schemaVersion: 1`, `sources` (registration records),
`aliases` (short-name → canonical-ID) and optional `runtime` defaults. Local domain
label overrides are deferred from v1.
`selections.json` has `schemaVersion: 1`, `revision` (increasing integer), and
`connections` mapping canonical IDs to `{enabled, inputs, credentialProfile?,
disabledTools}`. No selection means disabled. Input values are literal strings.
Catalog `toolPolicy` intersects with personal disabled tools; enabling cannot
remove a source deny. UI saves and CLI mutations use the same revisioned store.

Write temp + fsync + rename with a config lock. Config files hold references, not
values, and may be hand-written or symlinked from a dotfiles repository: they must
be regular files owned by the user and not writable by group or other. Runtime and
state files (socket, lock, daemon log) are stricter: no symlink components,
directories `0700`, files `0600`. Symlinks in the ancestors of the state and
runtime directories (`XDG_STATE_HOME`, `MCPARCEL_RUNTIME_DIR`, e.g. macOS `/var`)
are resolved once; the final directory must be a real directory and pass the
ownership and `0700` checks.
The config lock is `.mcparcel.lock` (mode `0600`) inside the resolved configuration
directory, created by the first writer, so every writer of one configuration takes
the same lock whatever its state directory; add it to `.gitignore` when the
directory lives in a dotfiles repository. A document counts as absent only when its
path does not exist: a symlink whose target is missing fails closed and never
falls back to defaults. When a multi-file save would pass through a state that does
not resolve, the affected connection is written disabled with `reviewRequired`
first, so a crash between files never blocks unrelated connections.
A stale UI save gives `config_conflict`, preserving both the disk state and UI draft.
No transaction journal in v1. Each file is replaced atomically on its own. A sync
writes the new snapshot file first (its name is the commit, so it never overwrites
anything) and then replaces `config.json`, which points to it; a crash in between
leaves the old pointer and an unused snapshot. Operations that would need two
mutable files to change together are ordered so that the intermediate state is
valid: selections never reference an ID before the config that defines it exists.

## Migration mapping

| mcporter field | MCParcel equivalent |
| --- | --- |
| Server map key | Connection ID plus preserved alias |
| `description` | `description` |
| `command`, `args`, `env` | stdio transport; unbound `${NAME}` becomes an `env:NAME` reference |
| `baseUrl`, `headers` | HTTP URL/headers with deliberate HTTP policy |
| `auth: oauth` | `auth.type: oauth` |
| `clientName` | `auth.clientName` |
| `oauthScope` | `auth.scopes`, split according to OAuth space-delimited syntax |
| `oauthClientId`, `oauthClientSecret` | OAuth Value bindings; preserve literal public client-ID when applicable |
| `oauthTokenEndpointAuthMethod` | `auth.tokenEndpointAuthMethod` |
| `oauthRedirectUrl` | `auth.redirectUrl` |

`import mcporter --file <path>` defaults to a dry-run report. `--apply` writes
personal definitions and reviewed selections, never the source file. Unknown fields,
nonempty imports, potential embedded secrets and unresolved credential mappings
must be reported. Embedded secrets include credential-named arguments, the value
part of `--flag=value`, `NAME=value` and Docker `-e`/`--env` forms, and any URL
with a password or a credential-named query parameter in arguments, env values,
headers or the base URL; the value never appears in a report, error or file.
Block application of an affected connection until resolved;
`--only <id>...` permits an explicit supported subset, with omitted rows reported.
An unbound `${NAME}` in an env or header value becomes an `env:NAME` reference
(prefix/suffix kept) with an `environment_reference` warning; protected names and
OAuth client references stay unresolved and block the connection.
A local `--bindings <file>` maps source env names to local profile/op-refs instead;
its format is a JSON map whose values are `{profile, secret}`. A connection must
map to one profile; conflicting profiles block its import. Never read source
credential stores, shell profiles or expanded env values to guess mappings.

Import does not publish personal endpoints or copy OAuth sessions. Validate first,
then export reviewed non-secret definitions to a team catalog by ordinary file edits.
A separate authoring/export command is deferred.
