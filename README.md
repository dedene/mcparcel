<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/brand/logo-dark.svg">
    <img alt="MCParcel" src="docs/brand/logo-light.svg" width="320">
  </picture>
</p>

# MCParcel

One CLI for every MCP call. A team keeps its MCP server definitions in a
GitHub repository, each person picks the servers and tools they want, and
credentials are fetched only when a call needs them.

```sh
mcparcel tools context7
mcparcel call context7.resolve-library-id libraryName=react query="React hooks"
```

Agents and harnesses all use the same command, so you configure servers and
sign-ins once instead of in every AI client. One line in a global `AGENTS.md`
is enough:

> Use mcparcel for all MCP calls.

## Why

If you use more than a couple of MCP servers across Claude Code, Codex and
other agents, the configuration drifts. Every client has its own server list,
API keys end up in plain JSON files, and each new teammate rebuilds the setup
by hand. MCParcel puts the shared part in one place and keeps the personal part
on your machine:

- A **catalog** is a JSON file in a GitHub repo. It holds server definitions and
  secret references, never secret values. `mcparcel add <owner/repo>`
  registers one, and `mcparcel sync` shows what changed before you apply it.
- Your **selection** of servers and tools stays local. `mcparcel setup` is a
  keyboard-driven terminal UI with tabs per domain (Design, Research, …).
  `enable` and `disable` do the same from scripts.
- **Personal connections** (a local app such as Paper, a server only you use)
  live next to the catalogs and never touch them.
- **Credentials** come from 1Password (`op://` references) or environment
  variables. OAuth sign-ins are stored in the macOS Keychain and refreshed for
  you. One 1Password approval covers a work session, so you don't get a
  biometric prompt on every call.
- A local **runtime** keeps server sessions open between calls, so an agent
  doesn't pay a cold start on each tool call.

## Status

Release candidate `0.1.0-rc.1`. It is used daily, but it is early software:

- The desktop build runs on **macOS with Apple Silicon** only. The binary is
  ad-hoc signed, not notarized.
- On **Linux** it runs headless from a static binary, for example as a
  Kubernetes sidecar. There is no 1Password or Keychain integration there; see
  [docs/headless.md](docs/headless.md).
- It is not published to npm yet. You build and install it from source.

## Install

You need Go 1.26 and Node.js 20 or newer.

```sh
git clone https://github.com/dedene/mcparcel.git
cd mcparcel
make npm-pack
npm install -g ./dist/mcparcel-0.1.0-rc.1.tgz
```

Or run it without a global install:
`npx --yes --package ./dist/mcparcel-0.1.0-rc.1.tgz mcparcel doctor`.
Upgrades and rollbacks are in [docs/migration.md](docs/migration.md).

## Getting started

1. Run `mcparcel doctor`. It works offline and writes nothing. Every `fail` row
   tells you what to do next.
2. Coming from [mcporter](https://github.com/openclaw/mcporter)? Preview an
   import of its config with
   `mcparcel import mcporter --file ~/.mcporter/mcporter.json`. The preview
   lists what would be imported and which values still need a credential
   binding. Add `--apply` once it looks right. MCParcel only reads mcporter's
   file, so mcporter keeps working and you can switch one server at a time.
3. Pick your servers with `mcparcel setup`, or `mcparcel enable <mcp>`.
4. Look at a server's tools and make a call:

   ```sh
   mcparcel tools context7
   mcparcel call context7.resolve-library-id libraryName=react
   ```

   Arguments are typed by the tool's schema. Add `--json` for one
   machine-readable envelope with stable error codes.
5. For a server that uses OAuth, sign in once with `mcparcel auth login <mcp>`.
   A call never opens a browser by itself; it fails with `auth_required` and
   tells you which command to run.

## Team catalogs

A catalog is a `mcparcel.json` file in any GitHub repository you can read
(private repos work through `gh`):

```json
{
  "schemaVersion": 1,
  "name": "Example team",
  "domains": {"research": {"label": "Research"}},
  "credentialProfiles": {
    "team": {"description": "Team API credentials from 1Password"}
  },
  "connections": {
    "perplexity": {
      "label": "Perplexity",
      "domains": ["research"],
      "credentialProfile": "team",
      "transport": {
        "type": "stdio",
        "command": "npx",
        "args": ["-y", "example-perplexity-mcp@1.0.0"],
        "env": {
          "PERPLEXITY_API_KEY": {"secret": "op://example-vault/perplexity/api-key"}
        }
      }
    }
  }
}
```

Register it with `mcparcel add acme/mcp-catalog`. Validate a catalog before
you push it with `mcparcel config validate --file mcparcel.json`. The full
format, including HTTP servers, OAuth and inputs, is in
[docs/catalog.md](docs/catalog.md).

## Documentation

- [docs/cli.md](docs/cli.md): commands, arguments, JSON output and exit codes.
- [docs/catalog.md](docs/catalog.md): catalog format, local state and merging.
- [docs/runtime.md](docs/runtime.md): the runtime, 1Password sessions and OAuth.
- [docs/migration.md](docs/migration.md): installing, upgrading and moving over
  from mcporter.
- [docs/headless.md](docs/headless.md): running headless on Linux.

## Development

```sh
make build   # bin/mcparcel
make test    # unit and CLI tests
make ci      # format, lint, vet, tests and packaging tests
```

## License

[MIT](LICENSE)
