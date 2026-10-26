# MCParcel catalog and local configuration

The catalog and local configuration contract, alongside [cli.md](cli.md).
Examples are fictional and contain no operational credentials.

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
Paper's real URL stays a local value, not a fabricated default.

## Field contract

Identifiers use lowercase letters, digits and hyphens; existing 32 aliases fit.
Catalog domains are a map of ID to `{label}`; `other` is reserved for uncategorized
connections. Same domain IDs merge across sources (the first registered source's
label wins); different IDs remain distinct. A connection can name multiple domains.
Duplicate JSON keys, unknown fields, undeclared inputs/profiles/domains, invalid
unions and unsupported schema versions are errors. No ignored config fields.

| Field | Contract |
| --- | --- |
| `minVersion` | Optional, top level, GitHub catalogs only (personal.json rejects it: `personal.minVersion: allowed only in a GitHub catalog`). A semantic version without build metadata, such as `0.2.0`. `add` and `sync` (preview and `--apply`) refuse a fetched catalog that needs a newer MCParcel with `catalog_requires_upgrade` (exit 4) and keep the saved snapshot. Prerelease builds rank below their release (`0.1.0-rc.1` < `0.1.0`); build metadata is ignored; a development build (a version that is not semver) never refuses. Reading the configuration never refuses on `minVersion` alone: a saved snapshot above the running version, after a rollback, keeps working, and `doctor` reports it (`version.catalog`). A snapshot that uses fields the running version does not know (such as `presets` or `suggest` under `0.1.0`) cannot be read at all, so rolling back below such a catalog's `minVersion` first needs `mcparcel remove <owner/repo>` |
| `credentialProfiles.<id>.suggest` | Optional, GitHub catalogs only, needs `minVersion` `0.2.0` or newer. `{mode: "desktop", account: "<sign-in address>"}`: the local profile the catalog proposes for this requirement. See [Joining a catalog](#joining-a-catalog-suggestions-and-presets) |
| `presets` | Optional, GitHub catalogs only, needs `minVersion` `0.2.0` or newer. Ordered array of 1 to 32 `{id, label, description?, connections}` offered as starting selections when a teammate joins. See [Joining a catalog](#joining-a-catalog-suggestions-and-presets) |
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
| HTTP `mode` | `auto` (default), `streamable` or `sse`; `sse` passes validation but is `runtime_unsupported` at runtime |
| HTTP `allowInsecureHttp` | `never` default; `loopback`, or `explicit` for a deliberately configured internal endpoint |
| `auth` | Omitted or `{type: "oauth", grant?, ...}`; `grant` is `authorization_code` (default, browser sign-in) or `client_credentials` (token from a client ID and secret, see OAuth fields); API-key auth uses env/header bindings |
| `toolPolicy` | Optional `{allow: [exact names], deny: [exact names]}`; omitted allow means all, empty allow means none; deny wins |
| `lifecycle` | Optional `{idleTimeout: "session"}` default, or positive duration; session keeps used processes until daemon stop/auth expiry. `keepAlive`: how often the daemon refreshes an idle OAuth session, `"off"` or a duration of at least `1h` (default `24h`); not part of the connection hash and never triggers review |
| `callTimeout` | Positive duration, default `120s`; override via CLI per call |
| `startupTimeout` | Optional positive duration, default `30s`: limit for connect, initialize and tool listing of this connection; not part of the connection hash |

Value is exactly one of: a literal string, `{input: "name"}`, or
`{secret: "op://vault/item/field", prefix?: "Bearer ", suffix?: ""}`.
Secret bindings are allowed in env, HTTP headers and OAuth client fields only.
The three parts of an `op://` reference may contain only letters, digits, `_`,
`.` and `-` (1Password rejects anything else, verified 2026-10-07): a vault or
item whose name has spaces or colons must be referenced by its ID.
Personal definitions may also use `{secret: "env:NAME", prefix?, suffix?}`: the
daemon resolves NAME at connect time from its captured login environment, falling
back to the Keychain generic password with service NAME and the login user as
account (macOS only; Linux has no such fallback). In headless mode it resolves NAME from the daemon's own environment
only, without a Keychain fallback (runtime.md "Headless mode"); `op://`
references work there only through a service-account profile. Temporary bridge until 1Password profiles (stage 6). Allowed in stdio env,
HTTP headers and the OAuth `clientId`/`clientSecret` fields; protected variable names are
rejected. A GitHub catalog containing one fails `add`/`sync` with `invalid_catalog`.
`config validate` checks format only; it does not apply the personal-only rule.
No secret command-line arguments. Import of a credential embedded in argv is
flagged for an equivalent env/header-based configuration; never silently exposed.
No shell expansion, `${...}` expansion or command substitution; only an explicit
`env:NAME` reference reads the environment.

OAuth fields: `clientName`, `scopes` (array), `clientId` (Value), `clientSecret`
(Value), `tokenEndpointAuthMethod` (`none`, `client_secret_basic`, `client_secret_post`),
`redirectUrl` (optional explicit callback), `issuerUrl` (optional explicit override).
`tokenEndpointAuthMethod` is sent in dynamic registration only; the SDK picks the
method from server metadata, and enforcing the configured one is deferred.
A dynamically registered client uses the method the registration response
names; a response that names none and issues no `client_secret` registers a
public client (`none`: client ID in the form body), not RFC 7591's
`client_secret_basic` default, which some servers refuse with an empty secret.
`redirectUrl` must be `http` on `127.0.0.1`, `[::1]` or `localhost`; without it the
callback is `http://127.0.0.1:<random port>/callback`.
Use discovery otherwise; bind state/tokens to the discovered issuer and resource.
Missing registered client information is an actionable error if registration is
unavailable. Do not assume `clientName` is interchangeable with a registered ID.

`grant` (stage 12): `authorization_code` (the default, everything above) or
`client_credentials`. A `client_credentials` connection needs an HTTP
transport, `tokenUrl`, `clientId` and `clientSecret`; `scopes` is optional and
`tokenEndpointAuthMethod` is `client_secret_post` (the default, client ID and
secret in the form body) or `client_secret_basic`. `tokenUrl` is `https`, or
`http` on a loopback host when the transport's `allowInsecureHttp` is
`loopback` or `explicit`. `redirectUrl`, `issuerUrl` and `clientName` are
rejected, and `tokenUrl` is rejected for `authorization_code`. The token URL is
used as given: no discovery, no dynamic registration, no Keychain item. Both
fields sit under `auth`, so changing them puts an enabled catalog connection in
`review_required` like any other auth change. Example from a personal
definition (`env:` references are personal-only):

```json
"auth": {
  "type": "oauth",
  "grant": "client_credentials",
  "tokenUrl": "https://<workspace>.frontapp.com/oauth/token",
  "clientId": {"secret": "env:FRONT_CLIENT_ID"},
  "clientSecret": {"secret": "env:FRONT_CLIENT_SECRET"}
}
```

## Joining a catalog: suggestions and presets

Two optional fields help a new teammate get started. Both are GitHub catalog
fields: personal.json rejects them (`personal.presets: allowed only in a GitHub
catalog`, and the same for `personal.credentialProfiles.<id>.suggest`).

```json
{
  "schemaVersion": 1,
  "minVersion": "0.2.0",
  "credentialProfiles": {
    "team": {
      "description": "MCP Integrations vault",
      "suggest": {"mode": "desktop", "account": "example.1password.com"}
    }
  },
  "presets": [
    {"id": "developer", "label": "Developer", "description": "Library docs", "connections": ["docs"]},
    {"id": "support", "label": "Support", "connections": ["docs", "inbox"]}
  ],
  "connections": {
    "docs": {"transport": {"type": "http", "url": "https://docs.example.invalid/mcp"}},
    "inbox": {
      "credentialProfile": "team",
      "transport": {
        "type": "http",
        "url": "https://inbox.example.invalid/mcp",
        "headers": {"Authorization": {"secret": "op://example-vault/inbox/token", "prefix": "Bearer "}}
      }
    }
  }
}
```

`suggest` proposes the local profile for a credential requirement. `mode` must
be `desktop`, the only mode that needs no secret: a suggestion never carries a
token, an `op://` reference, a `bootstrapRef` or a service-account mode, and
any other mode or extra field is rejected. `account` is the 1Password sign-in
address: a lowercase host name with at least two labels, each 1 to 63 letters,
digits or hyphens that do not start or end with a hyphen, 253 characters at
most (`example.1password.com`, `team.1password.eu`). An account ID or email
address is rejected.

`presets` are named starting selections, such as roles, shown in catalog order.
Each `id` is an identifier, unique among the presets. `label` is required, at
most 64 characters; `description` is optional, at most 200; neither may contain
control characters such as newlines. `connections` lists 1 or more connection
IDs of this catalog, without duplicates.

Consent: the catalog suggests, the teammate confirms. Nothing is created or
enabled from the catalog alone. A teammate who accepts a suggestion gets a
local profile whose name is the requirement ID, so it links to every
connection that declares that requirement; they can pick another account or
skip 1Password instead. Picking presets turns their connections on at once,
in one change the teammate sees in the list and can undo with `u` in one
step. A catalog can never choose a token or an account
without the user agreeing. A local profile that already has the requirement's
ID is kept and links to the catalog's connections by name; `join` shows its
mode and account when they differ from the suggestion. A suggestion no
connection uses is not asked about. The desktop app reads 1Password on macOS
only: elsewhere `join` shows a suggestion as a note pointing at a
service-account profile instead of asking.

`mcparcel join <owner/repo>` is where both show up ([cli.md](cli.md), "Joining
a catalog"): it asks about each suggestion whose requirement has no local
profile yet, then offers the presets, before the setup list.

A catalog that uses either field must set `minVersion` to `0.2.0` or newer (a
`0.2.0` prerelease such as `0.2.0-rc.1` also counts); otherwise it fails
validation with `minVersion: 0.2.0 or newer required by presets` (or
`by credentialProfiles.<id>.suggest`). MCParcel `0.1.0` does not know these
fields and rejects the whole catalog as `invalid_catalog`, so every teammate
must upgrade to `0.2.0` before the team catalog starts using them, and cannot
go back to `0.1.0` while a saved snapshot uses them (`remove` the catalog
first). From `0.2.0`
on, MCParcel reads `minVersion` before the strict check: a catalog that uses
fields from a later release and sets `minVersion` accordingly gets
`catalog_requires_upgrade`, not `invalid_catalog`.

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
The unauthenticated API allows 60 requests an hour per IP address, which a whole
office shares. When it is rate limited, MCParcel uses `gh api` instead; without
`gh` the fetch fails as rate limited and the saved snapshot stays active.
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
`sync --apply --accept <id>` or `enable <id>`. When the change adds a
credential profile requirement, only `enable` links the local profile with
that ID; `sync --apply --accept` needs `config profile bind` first.
Execution/auth config means transport type, command, args, env, cwd,
inheritEnv, URL, headers, HTTP mode, insecure-HTTP policy, `auth`,
`credentialProfile`, and any widening of `toolPolicy`.
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
- Data: `~/.local/share/mcparcel/runtime/<version>/mcparcel` — retained daemon binary;
  desktop only; the previous version is kept for rollback (see runtime.md,
  "Packaging and upgrades as built").
- Cache: `~/.cache/mcparcel/schemas/` — protected schema metadata by connection,
  auth identity and config hash; never tool results.
- Runtime: private short directory beneath the OS user temp directory — lock/socket;
  verify owner, reject symlinks, directories `0700`, socket/files `0600`.
  `MCPARCEL_RUNTIME_DIR` overrides it; a non-clean absolute value is cleaned, a
  relative one is rejected.
- State: `~/.local/state/mcparcel/daemon.log` — size-bounded, redacted daemon log
  (respects `XDG_STATE_HOME`).
- State: `~/.local/state/mcparcel/auth-lock.json` — the connections `auth lock`
  barred from reusing their stored OAuth session (`{"v":1,"connections":[ids]}`,
  `0600`). Written only by the daemon; a completed sign-in removes its entry.

Headless mode (stage 12, [headless.md](headless.md)): `config.json` →
`runtime: {mode: "headless", stateRoot: "/var/lib/mcparcel"}`. `mode` is
`desktop` (default) or `headless`; `stateRoot` is a clean absolute path other
than `/`, required in headless mode and rejected in desktop mode. Data, cache,
state and runtime then live under `<stateRoot>/data`, `/cache`, `/state` and
`/run` (`XDG_DATA_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME` and
`MCPARCEL_RUNTIME_DIR` no longer apply); the configuration stays at
`$XDG_CONFIG_HOME/mcparcel` and is read-only (`config_read_only` for every
write). macOS and Linux support both modes. On Linux, 1Password works
through service-account profiles only, in either mode. `runtime.supervised: true`
(headless only, default `false`) declares that `mcparcel runtime serve` owns
the runtime, so no CLI auto-starts one ([runtime.md](runtime.md)).

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
`desktop` requires `account` and has no service-account prompt-free guarantee:
it reads through the 1Password app itself, which may ask again after the app
locks, so there is no 24-hour promise and `--no-input` is served only from
the 5-minute cache (`auth_required` after it).
`service-account` reads a 1Password service-account token without the desktop
app, for headless mode, CI and Linux. It takes exactly one of `tokenEnv` and
`tokenFile`:

```json
{"mode": "service-account", "tokenEnv": "OP_SERVICE_ACCOUNT_TOKEN", "sessionDuration": "8h"}
```

```json
{"mode": "service-account", "tokenFile": "/var/run/secrets/mcparcel/op-token"}
```

- `account` and `bootstrapRef` are forbidden, even empty: the token alone
  selects the 1Password account. The desktop modes in turn forbid `tokenEnv`
  and `tokenFile`.
- `tokenEnv` names an environment variable that starts with `OP_`, other than
  `OP_CONNECT_TOKEN`; there is no default name. Every `OP_` name is protected:
  no `env:` reference or `inheritEnv` can name it, so the token is never passed
  to a child process. Any process that has the variable in its environment
  still exposes it to its own user (runtime.md "Same uid"); on desktop,
  `tokenFile` keeps it out of the login session. The runtime reads the variable from its own environment
  map: the login-shell capture in desktop mode, the daemon's environment in
  headless mode ([runtime.md](runtime.md)).
- `tokenFile` is a clean absolute path of printable ASCII (no `~`). It is not
  checked when the configuration is read; the runtime opens it on every
  bootstrap with the configuration file walk (symbolic links resolved once,
  so a Kubernetes Secret volume's `..data` layout works). The file must be a
  regular file owned by the user or by root, at most 16 KiB, holding one token
  of printable ASCII without inner spaces (surrounding whitespace, such as a
  trailing newline, is dropped). It is never readable or writable by others,
  on any mount. Group read or write is accepted only on a read-only mount (a
  Kubernetes Secret with `defaultMode: 0440` and `fsGroup`); a read-only
  mount proves the file cannot change, not that the token is private.
  Anything else is `unsafe_local_path`.

A service-account profile bootstraps without a prompt, so it also works under
`--no-input` ([runtime.md](runtime.md)). Profiles of every mode may specify
`sessionDuration` (default `24h`, positive and at most `24h` in v1). Credential
cache/lease maximum is `5m`; shortening a lease never extends a session. Only local
config can define profiles. Validate an `op://` bootstrap reference without resolving it.

A `config.json` with a `service-account` profile needs an MCParcel that knows
the mode. An older MCParcel rejects the file, so every command that reads the
configuration fails with `invalid_config` ([migration.md](migration.md)).

Selections bind a catalog's `team` requirement to that local profile ID. Different
catalogs can use different profiles/accounts without trusting a catalog to choose
a bootstrap token or broaden vault access. Enabling a connection, with `enable` or
with Space in setup, links its requirement to the local profile with the same ID
when the selection has no profile or names one that no longer exists. A catalog
only names the requirement: the linked profile is one you created with `config
profile set`, a binding you made is never replaced, and no profile is ever
created. A requirement with no profile of that ID stays `config_required` until
you bind one with `config profile bind`, or choose one in setup. In setup, one
Space on such an entry also asks for missing inputs and binds an existing profile
([cli.md](cli.md#exceptional-states)). Setup reads no secret and opens no auth
prompt while you browse or change things. While you browse it reads the stored
sign-in of each enabled OAuth connection, as `auth status` does, from that
connection's Keychain (or Secret Service) item, without a prompt: on Linux it
reads only an unlocked item, and a locked keyring shows `sign-in unknown`.

`config.json` also holds `schemaVersion: 1`, `sources` (registration records),
`aliases` (short-name → canonical-ID) and optional `runtime` defaults. Local domain
label overrides are deferred from v1.
`selections.json` has `schemaVersion: 1`, `revision` (increasing integer), and
`connections` mapping canonical IDs to `{enabled, inputs, credentialProfile?,
disabledTools}`. No selection means disabled. Input values are literal strings.
Catalog `toolPolicy` intersects with personal disabled tools; enabling cannot
remove a source deny. Setup changes and CLI mutations use the same revisioned store.

Write temp + fsync + rename with a config lock. Config files hold references, not
values, and may be hand-written or symlinked from a dotfiles repository: they must
be regular files owned by the user or by root and not writable by group or
other; the same holds for each directory on the way to them, where a sticky
bit also counts as safe. A file or directory on a read-only mount counts as
not writable, so a Kubernetes ConfigMap (root-owned files behind `..data`
symlinks, a mount root that may be `0777`) is accepted in every mode (stage
12). Root can already change anything, and a ConfigMap or Secret volume cannot
be changed from the pod. The check sees only this mount, though: a volume that
another container mounts read-write (an emptyDir or PVC shared with an agent)
still passes and can still be changed, so keep the configuration off such
volumes ([headless.md](headless.md#configuration-a-configmap-at-etcmcparcel)). Runtime and
state files (socket, lock, daemon log) are stricter: no symlink components,
directories `0700`, files `0600`. Symlinks in the ancestors of the state and
runtime directories (`XDG_STATE_HOME`, `MCPARCEL_RUNTIME_DIR`, e.g. macOS `/var`)
are resolved once; the final directory must be a real directory and pass the
ownership and `0700` checks. A headless `stateRoot` itself only has to be
owned by the user or root, whatever its mode (an `emptyDir` is `0777` or
`2777`); everything below it gets the full checks.
The config lock is `.mcparcel.lock` (mode `0600`) inside the resolved configuration
directory, created by the first writer, so every writer of one configuration takes
the same lock whatever its state directory; add it to `.gitignore` when the
directory lives in a dotfiles repository. A document counts as absent only when its
path does not exist: a symlink whose target is missing fails closed and never
falls back to defaults. When a multi-file save would pass through a state that does
not resolve, the affected connection is written disabled with `reviewRequired`
first, so a crash between files never blocks unrelated connections.
Setup has no draft: when the store changed elsewhere since setup last read it,
setup applies its change once more to the fresh state, or does not make a change
that no longer applies there, and shows the fresh state either way.
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
(prefix/suffix kept) with an `environment_reference` warning; the same holds for
`oauthClientId`/`oauthClientSecret`, so all 32 owner entries import. Protected
names stay unresolved and block the connection.
A local `--bindings <file>` maps source env names to local profile/op-refs instead;
its format is a JSON map whose values are `{profile, secret}`. A connection must
map to one profile; conflicting profiles block its import. Never read source
credential stores, shell profiles or expanded env values to guess mappings.

Import does not publish personal endpoints or copy OAuth sessions. Validate first,
then export reviewed non-secret definitions to a team catalog by ordinary file edits.
A separate authoring/export command is deferred.
