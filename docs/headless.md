# Running MCParcel headless in a Kubernetes sidecar

This guide sets up MCParcel as the MCP layer of a claw-wrap sidecar: an agent
calls `mcparcel call front.<tool>`, claw-wrap runs the real `mcparcel` next to
it with the Front client credentials, and MCParcel mints an OAuth
`client_credentials` token, applies the tool policy and calls Front's MCP
server. The agent container holds no secret.

Built in stage 12 ([plan](superpowers/plans/2026-10-07-mcparcel-stage-12-headless-linux.md)).
The contracts behind this guide are in [runtime.md](runtime.md#headless-mode-stage-12),
[catalog.md](catalog.md) and [cli.md](cli.md). The end-to-end proof through
real claw-wrap in a read-only Linux container is in
[feasibility.md](feasibility.md) (`make test-headless-e2e`). Not yet done: a
run against the real Front server.

## The pod

```text
Pod (1 replica)
├─ agent       `mcparcel call front.<tool> ...`
│              /usr/local/bin/mcparcel = symlink to the claw-wrap client; no secrets
└─ claw-wrap   PID 1 is tini; claw-wrap daemon (UID check, HMAC, argv allowlist)
   sidecar     runs /usr/local/bin/mcparcel with FRONT_CLIENT_ID/SECRET and XDG_CONFIG_HOME=/etc
               └─ mcparcel daemon (own session, fds on /dev/null): tool policy, pool,
                  Front token in memory
   volumes:    /etc/mcparcel      <- ConfigMap mcparcel-config (read-only)
               /var/lib/mcparcel  <- emptyDir mcparcel-state (sidecar only)
```

The first call starts the MCParcel daemon from the CLI that claw-wrap ran. The
CLI starts it in a new session (`setsid`) with stdin, stdout and stderr on
`/dev/null` and no descriptor but the daemon lock, so claw-wrap's client
returns as soon as the CLI exits, and claw-wrap's SIGKILL to the CLI's process
group (sent after every call) does not reach the daemon. Later calls reuse the
daemon, its connection to Front and its token.

Start from claw-wrap's own sidecar guide (`docs/KUBERNETES.md` and
`examples/kubernetes/deployment.yaml` in the claw-wrap repository). This guide
covers only what MCParcel adds.

## Images

Sidecar: the claw-wrap image already runs `tini` as PID 1. Add the static Linux
build of MCParcel (`make build-linux` writes `dist/mcparcel-linux-amd64` and
`dist/mcparcel-linux-arm64`):

```dockerfile
FROM ghcr.io/dedene/claw-wrap:0.6.0
ARG TARGETARCH
COPY dist/mcparcel-linux-${TARGETARCH} /usr/local/bin/mcparcel
```

BuildKit (the default builder since Docker 23, and `docker buildx`) sets
`TARGETARCH` to `amd64` or `arm64` for each platform it builds, so
`docker buildx build --platform linux/amd64,linux/arm64` copies the matching
binary into each image. The legacy builder leaves it empty and the `COPY`
fails; name the file for your nodes instead. An `amd64` binary on `arm64`
nodes fails every call with an exec format error, which the agent sees as a
claw-wrap exec failure rather than an MCParcel error.

Agent: the `claw-wrap` binary plus `ln -s claw-wrap /usr/local/bin/mcparcel`.
The agent image does not contain MCParcel itself.

## PID 1 must reap

The sidecar needs a PID 1 that reaps orphans. Once the CLI exits, the
MCParcel daemon it started is reparented to PID 1 of the sidecar. claw-wrap
does not reap processes it did not start, and neither does a process that was
never meant to be an init. Without a reaper, a daemon that exits (idle exit
after 24 hours, `runtime stop`, `runtime restart`, a crash) stays a zombie: it
holds a PID, and a `kill -0 <pid>` liveness check still reports it alive.
Orphaned children of stdio MCP servers end up there too. MCParcel never reaps
for PID 1 itself, because that would race Go's own `os/exec` wait. A runtime
that is PID 1 (`mcparcel runtime serve` as a container's entrypoint) writes
`pid1_no_reaper` to the daemon log instead.

Two setups reap:

- `tini` as the sidecar's PID 1. The claw-wrap image does this already; with
  plain `docker run`, pass `--init`. This is the recommended setup.
- `shareProcessNamespace: true` on the pod, so the pause container is PID 1
  for every container and reaps. It also lets the agent container see the
  sidecar's processes. If both containers run as the same UID, the agent can
  then read `/proc/<pid>/environ` of the MCParcel daemon, which holds
  `FRONT_CLIENT_SECRET`. claw-wrap's guide keeps `shareProcessNamespace: false`
  for that reason; use this only with separate UIDs.

## Configuration: a ConfigMap at /etc/mcparcel

MCParcel finds its configuration at `$XDG_CONFIG_HOME/mcparcel`. claw-wrap
passes the tool only `PATH`, `HOME`, `USER`, `TERM`, `COLORTERM` and the tool's
`env:` map, so set `XDG_CONFIG_HOME: /etc` there (see the claw-wrap block
below). Without it, MCParcel looks under `$HOME/.config/mcparcel`, finds no
`config.json`, assumes desktop mode, and on Linux every runtime command fails
with `runtime_unsupported`.

`config.json`:

```json
{"schemaVersion": 1, "runtime": {"mode": "headless", "stateRoot": "/var/lib/mcparcel"}}
```

`personal.json` (replace `<workspace>`; the allow list is a starting point to
check against `mcparcel tools front` on the real server):

```json
{
  "schemaVersion": 1,
  "connections": {
    "front": {
      "label": "Front",
      "transport": {"type": "http", "url": "https://mcp.frontapp.com/mcp", "mode": "streamable"},
      "auth": {
        "type": "oauth",
        "grant": "client_credentials",
        "tokenUrl": "https://<workspace>.frontapp.com/oauth/token",
        "clientId": {"secret": "env:FRONT_CLIENT_ID"},
        "clientSecret": {"secret": "env:FRONT_CLIENT_SECRET"}
      },
      "toolPolicy": {
        "allow": ["read_conversation", "create_draft", "update_draft", "add_comment", "tag_conversation", "assign_conversation"],
        "deny": ["send_message"]
      },
      "callTimeout": "60s"
    }
  }
}
```

`selections.json`:

```json
{"schemaVersion": 1, "revision": 1, "connections": {"local:front": {"enabled": true}}}
```

`deny` is redundant next to an explicit `allow`, but it documents intent and
still holds if someone widens `allow` later.

Create it with
`kubectl create configmap mcparcel-config --from-file=config.json --from-file=personal.json --from-file=selections.json`
and mount the whole ConfigMap read-only at `/etc/mcparcel` (no `subPath`).

Why this passes MCParcel's file checks: ConfigMap files are owned by root,
reached through the kubelet's `..data` symlinks, and the mount root may be
`0777` or `2777`. MCParcel accepts config files and directories owned by the
user or by root when they are not group- or other-writable, and anything on a
read-only mount counts as not writable. A writable mount with a world-writable
config file is still refused (`unsafe_local_path`).

That read-only exemption is safe only for a ConfigMap or Secret volume, or a
volume that no other container mounts read-write. A read-only mount says
nothing about who else can change the files: it describes this one mount.
Never put the configuration on a volume the agent container can write, such
as an emptyDir or PVC that an initContainer fills and the agent mounts
read-write. Whoever can write `personal.json` controls the tool policy and
where the client secret is sent (`auth.tokenUrl`), and the daemon rereads the
configuration on every request.

MCParcel never writes this directory in headless mode: no lock file, no
snapshot, no migration. Commands that would write it fail with
`config_read_only`. To change the policy, edit the ConfigMap. The daemon
rereads the configuration on every request, so after the kubelet swaps
`..data` (usually within a minute) the next call uses it; a changed Front
connection reconnects and mints a new token. A change to the `runtime` block
needs a pod restart.

GitHub catalogs (`add`, `sync`) are refused in headless mode; put connections
in `personal.json`. `op://` references are refused too (`config_required`):
headless mode never contacts 1Password. Use `env:` references.

## State: an emptyDir at /var/lib/mcparcel

`runtime.stateRoot` replaces the XDG state, data and cache directories and the
runtime directory:

| Path | Contents |
| --- | --- |
| `/var/lib/mcparcel/run` | `daemon.sock`, `daemon.lock`, `supervised` (only with `runtime serve`) |
| `/var/lib/mcparcel/state` | `daemon.log` |
| `/var/lib/mcparcel/data`, `/cache` | Created when used |

The root may be owned by root and world-writable without the sticky bit, as
the kubelet creates an emptyDir (`0777`, or `2777` with `fsGroup`). Everything
MCParcel creates below it is `0700` or `0600` and owned by the sidecar's UID,
and it is checked on every open.

Mount this volume in the sidecar only. A container that mounts it can replace
`run/` and put its own socket in front of the daemon; MCParcel accepts that
risk for the root itself, so keep the agent out. The token never touches this
volume: a `client_credentials` token lives in daemon memory only.

```yaml
# claw-wrap sidecar, in addition to claw-wrap's own mounts
volumeMounts:
  - name: mcparcel-config
    mountPath: /etc/mcparcel
    readOnly: true
  - name: mcparcel-state
    mountPath: /var/lib/mcparcel
# pod
volumes:
  - name: mcparcel-config
    configMap:
      name: mcparcel-config
  - name: mcparcel-state
    emptyDir: {}
```

The socket path must stay under 100 bytes; `/var/lib/mcparcel/run/daemon.sock`
is 33.

## The claw-wrap tool

```yaml
credentials:
  front_client_id:
    source: file:/etc/claw-wrap/secrets/front-client-id
  front_client_secret:
    source: file:/etc/claw-wrap/secrets/front-client-secret

tools:
  mcparcel:
    binary: /usr/local/bin/mcparcel
    timeout: 120s                  # above callTimeout (60s) plus connect time
    working_dir: /var/lib/claw-wrap
    use_pty: false                 # plain pipes: stdout stays one JSON envelope
    use_stdin: false               # stdin is /dev/null; nothing can wait for input
    request_env: []                # the agent cannot set XDG_CONFIG_HOME or anything else
    env:
      XDG_CONFIG_HOME: /etc        # a literal: MCParcel reads /etc/mcparcel
      FRONT_CLIENT_ID: front_client_id
      FRONT_CLIENT_SECRET: front_client_secret
    mode: allowlist
    allowed_args:
      - match: argv
        argv:
          - 'call'
          - 'front\.(read_conversation|create_draft|update_draft|add_comment|tag_conversation|assign_conversation)'
          - '--args'
          - '(?s)\{.*\}'
          - '--json'
        message: "only: mcparcel call front.<tool> --args '<json>' --json"
      - match: argv
        argv: ['tools', 'front', '--json']
      - match: argv
        argv: ['runtime', 'restart', '--json']
```

The two allow lists are separate gates. claw-wrap's decides which argv reaches
MCParcel; MCParcel's `toolPolicy` decides which tool runs. Keep them in step:
a tool that claw-wrap lets through and MCParcel denies fails `tool_denied`
(exit 4) before any daemon, connection or token request.

`env:` values that name a credential come from claw-wrap's credential store
(`file:` here, read again on every call); other values are literals.
MCParcel's CLI passes a daemon it starts only the variables enabled
connections reference (`FRONT_CLIENT_ID`, `FRONT_CLIENT_SECRET`) plus `PATH`,
`HOME`, `TMPDIR`, `LANG`, `LC_ALL` and `USER`, with `XDG_CONFIG_HOME` set to
match. No login shell runs and no Keychain is read.

`runtime restart` is in the allow list for secret rotation. Leave `--force`
out: it cancels the agent's calls in flight.

## Secret rotation

The daemon keeps the credentials it started with. A rotated Secret reaches
each new CLI process (claw-wrap reads `file:` credentials on every call), but
not the running daemon, which keeps minting with the old values until it
restarts. After rotating:

- run `mcparcel runtime restart --json` through claw-wrap. The CLI stops the
  daemon and starts a new one from its own environment, which has the new
  values. It refuses with `runtime_busy` while a call is active; retry; or
- restart the pod.

The old token is dropped with the daemon. Mints with a revoked secret fail
`auth_failed` (`invalid_client`) until one of the two happens.

## The 401 resend and what it assumes

Front's tokens last 900 seconds. MCParcel mints a new one 180 seconds before
expiry, so a 401 should be rare. When Front still answers a request with HTTP
401, MCParcel drops the token, mints a new one once and resends that request
once, `tools/call` included. A second 401 fails the call with `auth_failed`
(`token_rejected`). A 403 never mints again.

Resending a `tools/call` is safe only if the server checks the bearer token
before it runs the tool. A 401 is the status of the POST, sent before any body
or stream, and bearer checks normally sit in HTTP middleware in front of the
handler. MCParcel cannot verify this for Front. If Front ever ran a tool and
then answered 401, that tool would run twice. Nobody has tested this against
the real server, because it needs a write. The full rule is in
[runtime.md](runtime.md#oauth-as-built-stage-7-core).

## Verify

From the agent container:

```sh
mcparcel tools front --json
```

Expect `ok: true` and `items` with the allowed tools only; `send_message` is
never listed. A name in `allow` that Front does not advertise is left
out; compare with Front's tool list and fix the ConfigMap.

In the sidecar, `/var/lib/mcparcel/state/daemon.log` shows `daemon_started`,
then `oauth_token_minted` with `trigger: "first"` and `ttl: 900`. It never
contains the client ID, the secret, a token or the Front URL. It should not
contain `pid1_no_reaper`. `mcparcel runtime status` (in the sidecar, with
`XDG_CONFIG_HOME=/etc`) prints `Environment: daemon environment`; it never
starts a daemon.

`XDG_CONFIG_HOME=/etc mcparcel doctor --json` in the sidecar checks the mode,
state root, configuration, the `env:` variable names and the runtime's version
without starting a daemon or writing anything. It reads the variables of the
`kubectl exec` shell, not the daemon's, so a missing variable is only a warning
there. `runtime.binary` is skipped: headless mode runs the image's binary in
place and keeps no retained copy. `doctor front --live` never
starts a runtime in headless mode, also when the daemon idles out between
doctor's probe and its live call; run it after a claw-wrap call started one (or
under `runtime serve`). Do not add `doctor` to the claw-wrap allow list.

Do not run `tools` or `call` from a `kubectl exec` shell in the sidecar. That
shell does not have the Front variables, and a CLI started there that finds no
daemon starts one without them, which then fails `config_required` until a
`runtime restart` through claw-wrap.

## Failures you will see

| Code (exit) | Message | Cause and fix |
| --- | --- | --- |
| `config_required` (2) | `Environment variable FRONT_CLIENT_SECRET is not set.`, `details.variables: ["FRONT_CLIENT_SECRET"]` | The daemon was started without it: a missing claw-wrap `env:` entry or credential, or a daemon started from `kubectl exec`. Fix the source, then `runtime restart` through claw-wrap. Not cached: the restarted daemon tries again. |
| `config_required` (2) | `1Password references need the desktop app and are not available in headless mode.` | An `op://` reference in the config. Use `env:`. |
| `auth_failed` (3) | `The token endpoint rejected the client credentials (invalid_client).` | Wrong client ID or secret, a revoked secret, or a token URL for another workspace. Other RFC 6749 codes (`unauthorized_client`, `invalid_scope`) appear the same way; a 4xx without one shows `http_4xx` (a wrong `tokenUrl` path, for example). |
| `auth_failed` (3) | `The server rejected a newly issued access token (token_rejected).` | Front's MCP server refused a fresh token, usually because `tokenUrl` issues tokens for something else. |
| `auth_failed` (3) | `The server refused this client access (HTTP 403).` | The client lacks a scope or permission on Front. |
| `connection_failed` (6) | `Could not get an access token from the token endpoint (http_5xx).` (or `http_429`, `timeout`, `network_error`, `redirect`) | Token endpoint down, rate limited or unreachable; `redirect` means the token URL answered 3xx, which MCParcel never follows. Nothing is cached; the next call tries again. Also used, with its own message, when the MCP server is unreachable. |
| `tool_denied` (4) | `The tool is denied by connection policy.` | The tool is not in `allow`, or is in `deny`. Checked offline: no daemon starts and no request is sent. |
| `config_read_only` (2) | `This configuration is read-only (headless mode).` | A command that writes configuration: `enable`, `disable`, `tools enable/disable`, `local ...`, `config input/profile ...`, `import --apply`, `add`, `remove`, `sync` (also without `--apply`). Change the ConfigMap instead. |
| `runtime_unsupported` (2) | `On Linux, MCParcel runs in headless mode only.` | `config.json` is missing or not headless, usually because `XDG_CONFIG_HOME` is not `/etc`. |
| `unsafe_local_path` (2) | `A local runtime or configuration path is unsafe.` | A config file that is group- or other-writable on a writable mount, a state directory with the wrong owner or mode, or a socket path over 100 bytes. |
| `auth_required` (3) | `This server needs sign-in, which headless mode cannot do.` | A connection without `grant: "client_credentials"` asked for OAuth. Headless mode has no browser. |

A call whose server asks for approval returns at once with an
`elicitation_declined` warning (or error): headless mode never prompts.

## Alternative: a supervised runtime

`mcparcel runtime serve` runs the daemon in the foreground. It takes the
daemon lock itself, logs to the daemon log and to stderr, never exits when
idle, and on SIGTERM ends active calls (`outcome_unknown` for dispatched ones),
closes its sessions and exits 0. A supervised runtime makes CLIs wait up to 15
seconds for it instead of starting their own (then `runtime_supervised`, exit
6), and refuses `runtime restart` (`runtime_supervised`).

Declare it in the ConfigMap's `config.json`:

```json
{"schemaVersion": 1, "runtime": {"mode": "headless", "stateRoot": "/var/lib/mcparcel", "supervised": true}}
```

`supervised` is allowed in headless mode only. Do not rely on the
`run/supervised` file that `serve` writes once it holds the lock: the state
emptyDir is empty on every pod start, so a CLI that runs before `serve` has
started finds neither socket nor file and would auto-start a daemon from
claw-wrap's environment, which in this design has no Front variables. `serve`
then fails `runtime_busy` and restarts in a loop, while the auto-started
daemon answers `config_required`. With `supervised: true` the CLI never
starts a daemon, whatever the emptyDir holds. Remove both the setting and the
file to go back to auto-start.

With `serve` in a third container that holds the Front variables and mounts
`mcparcel-state` and `mcparcel-config` at the same paths, claw-wrap holds no
Front secret and only runs the CLI. Both containers must run as the same UID
(the socket checks the peer UID) and use the same configuration path, or the
CLI gets `runtime_config_mismatch`. That container needs a reaping PID 1 too.
Run it as a native sidecar (an `initContainers` entry with
`restartPolicy: Always`, Kubernetes 1.29 or later) listed before claw-wrap,
with a startup probe that waits for the socket (for example
`exec: {command: [test, -S, /var/lib/mcparcel/run/daemon.sock]}`), so
the kubelet starts claw-wrap and the agent only once the runtime answers.
Then calls do not spend their first 15 seconds waiting. Choose this or
auto-start before deploying; the guide above uses auto-start.
