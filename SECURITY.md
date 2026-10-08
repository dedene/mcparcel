# Security policy

MCParcel handles credentials: it resolves 1Password references, stores OAuth
tokens in the macOS Keychain and passes secrets to MCP servers. Reports about
any of that are welcome.

## Reporting a vulnerability

Please report privately through
[GitHub's private vulnerability reporting](https://github.com/dedene/mcparcel/security/advisories/new).
Don't open a public issue for a security problem.

Include the MCParcel version (`mcparcel --version`), your platform and the
steps to reproduce. Leave out real tokens, `op://` references and server URLs;
a redacted example is enough.

You can expect a first reply within a week. Once a fix is released, the
advisory is published with credit to you, unless you prefer to stay anonymous.

## Supported versions

MCParcel is pre-1.0. Only the latest release gets security fixes.

## What counts

For example:

- a secret, token or `op://` reference showing up in output, logs, error
  messages or files on disk;
- another local user or process reaching the runtime socket, or reading
  MCParcel's state;
- a catalog from a repository you registered making MCParcel run something
  you did not enable, or bypass a tool policy;
- the OAuth sign-in flow accepting a callback it should refuse.

A catalog author can define what a connection runs; enabling a connection
means trusting that definition. That alone is not a vulnerability.
