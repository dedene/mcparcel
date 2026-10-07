# Installing, upgrading and rolling back MCParcel

Release candidate `0.1.0-rc.1`, 7 October 2026. MCParcel is installed from a local
tarball; it is not published to npm. The npm package runs on macOS with Apple
Silicon only; for Linux, see [headless.md](headless.md). Stage 11 adds the
per-connection migration from mcporter to this page.

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

## Roll back to mcporter

MCParcel never writes mcporter's configuration or its token cache, so mcporter
works as it did before.

1. Run `mcparcel runtime stop`.
2. Point your agent instructions (for example the global `AGENTS.md`) back at
   mcporter.
3. Optional: run `mcparcel auth logout <mcp>` for each OAuth connection to remove
   MCParcel's Keychain item. It does not revoke the token at the provider yet.
4. Optional: `npm uninstall -g mcparcel`.
