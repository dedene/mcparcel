package cmd

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

type AuthCmd struct {
	MCP    AuthMCPCmd    `arg:"" help:"Make a connection's credentials fresh: sign in again, read its 1Password secrets again or get a new client-credentials token."`
	Status AuthStatusCmd `cmd:"" help:"Show stored sign-in and 1Password session state without contacting servers."`
	Logout AuthLogoutCmd `cmd:"" help:"Remove the stored sign-in for a connection."`
	Lock   AuthLockCmd   `cmd:"" help:"End 1Password sessions and block stored sign-ins until the next mcparcel auth <mcp>."`
}
type (
	// AuthMCPCmd is auth <mcp>, a branching argument: kong tries the
	// subcommands first, so a connection named status, logout or lock is
	// reached by its canonical ID. Its first positional must carry the
	// parent field's name.
	AuthMCPCmd struct {
		MCP string `arg:"" help:"Connection whose credentials to make fresh."`
	}
	AuthStatusCmd struct {
		MCP string `arg:"" optional:"" help:"Connection to show; when omitted, OAuth connections, signed-in ones and ones whose server asked for sign-in."`
	}
	AuthLogoutCmd struct {
		MCP string `arg:"" required:"" help:"Connection whose stored sign-in to remove."`
	}
	AuthLockCmd struct{}
)

// keyringReachableCheck is keyringReachable and authRuntimeClient is
// newRuntimeClient; tests replace them.
var (
	keyringReachableCheck = keyringReachable
	authRuntimeClient     = newRuntimeClient
)

// Run makes the connection's credentials fresh through the runtime, which it
// starts if needed: 1Password values are read again first, then a browser
// sign-in or a new client_credentials token follows. Every deterministic
// refusal comes first, offline: a connection with nothing to authenticate, a
// sign-in in headless mode or under --no-input and, when no runtime runs, a
// sign-in without a reachable keyring (Linux without a session bus).
func (c *AuthMCPCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	paths, err := runtimePaths()
	if err != nil {
		return err
	}
	snapshot, err := config.Load(paths)
	if err != nil {
		return err
	}
	plan, err := runtimeclient.PlanAuth(snapshot, c.MCP)
	if err != nil {
		return err
	}
	if plan.SignIn {
		switch {
		case paths.Headless():
			return runtimeclient.HeadlessSignIn()
		case opts.NoInput:
			return auth.SignInNeedsInput(c.MCP, plan.Canonical)
		case !keyringReachableCheck(paths) && !daemonRunning(ctx, paths):
			// The runtime this sign-in would start inherits this session's
			// bus, which reaches no keyring. A running runtime has its own,
			// which its sign-in preflight checks.
			return output.KeyringUnreachableError()
		}
	}
	client, err := authRuntimeClient(opts)
	if err != nil {
		return err
	}
	open := newBrowser(client.Paths)
	client.OnAuthURL = func(u string) {
		opens := browserOpens()
		fmt.Fprint(s.Err, signInPrompt(c.MCP, u, opens))
		if opens {
			_ = open(ctx, u)
		}
	}
	data, err := client.Auth(ctx, c.MCP)
	if err != nil {
		return err
	}
	if opts.JSON {
		return writeSuccess(s, opts, data)
	}
	return writeSuccess(s, opts, authLines(c.MCP, data))
}

// authLines is auth <mcp>'s human output: one line per step, in step order.
func authLines(name string, data runtimeclient.AuthData) string {
	var b strings.Builder
	if data.SecretsRefreshed != nil && *data.SecretsRefreshed {
		b.WriteString("Read the 1Password secrets for " + name + " again.\n")
	}
	if data.SignedIn != nil {
		if *data.SignedIn {
			b.WriteString("Signed in to " + name + ".\n")
		} else {
			b.WriteString(name + " did not ask for sign-in. Nothing was stored.\n")
		}
	}
	if data.TokenRenewed != nil && *data.TokenRenewed {
		b.WriteString("Got a new access token for " + name + ".\n")
	}
	return b.String()
}

// Run removes the item through the runtime, which first stops the
// connection's session and its refreshes. A canonical ID works also when
// the connection is gone from the config, so its leftover item can go.
func (c *AuthLogoutCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	paths, err := runtimePaths()
	if err != nil {
		return err
	}
	canonical := c.MCP
	id, conn, err := authTarget(paths, c.MCP)
	switch {
	case err == nil && clientCredentialsConn(conn):
		return nothingToSignIn(c.MCP)
	case err == nil:
		canonical = id
	case !strings.Contains(c.MCP, ":") || config.ValidateCanonicalID(c.MCP) != nil:
		return err
	}
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	data, err := client.Logout(ctx, canonical)
	if err != nil {
		return err
	}
	if opts.JSON {
		return writeSuccess(s, opts, data)
	}
	if !data.Removed {
		return writeSuccess(s, opts, "No stored sign-in for "+c.MCP+".\n")
	}
	return writeSuccess(s, opts, "Removed the stored sign-in for "+c.MCP+". The provider was not asked to revoke it.\n")
}

// daemonRunning reports whether a compatible runtime runs; it never starts
// one.
func daemonRunning(ctx context.Context, paths config.Paths) bool {
	client := runtimeclient.Client{Paths: paths, Version: version}
	status, err := client.Status(ctx)
	return err == nil && status.Running && status.Compatible
}

// Run ends every credential session through the runtime, which it starts if
// needed: the daemon is the only writer of the OAuth lock.
func (c *AuthLockCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	data, err := client.Lock(ctx)
	if err != nil {
		return err
	}
	if opts.JSON {
		return writeSuccess(s, opts, data)
	}
	return writeSuccess(s, opts, "Locked. 1Password sessions ended; signed-in connections need mcparcel auth <mcp>.\n")
}

// authTarget resolves name offline to its canonical ID and connection, which
// is nil for a catalog row without a usable definition.
func authTarget(paths config.Paths, name string) (string, *config.Connection, error) {
	snapshot, err := config.Load(paths)
	if err != nil {
		return "", nil, err
	}
	ids := make([]string, 0, len(snapshot.Effective.Connections))
	for id := range snapshot.Effective.Connections {
		ids = append(ids, id)
	}
	canonical, err := config.ResolveID(name, snapshot.Effective.Aliases, ids)
	if err != nil {
		return "", nil, err
	}
	return canonical, snapshot.Effective.Connections[canonical].Connection, nil
}

// clientCredentialsConn reports whether c gets its token with the
// client_credentials grant, which has no sign-in and stores nothing.
func clientCredentialsConn(c *config.Connection) bool {
	return c != nil && c.Auth != nil && c.Auth.Grant == config.GrantClientCredentials
}

func nothingToSignIn(name string) *output.Error {
	e := output.NewError("invalid_arguments", nil)
	e.Message = name + " uses client credentials; there is nothing to sign in to."
	return e
}

// signInPrompt tells the user where to sign in: the browser that is about to
// open, or, where none can open, the URL to open themselves. Over SSH it adds
// how a browser on another machine reaches the loopback callback.
func signInPrompt(name, u string, opens bool) string {
	if opens {
		return fmt.Sprintf("Opening your browser to sign in to %s.\nIf it does not open, visit:\n%s\n", name, u)
	}
	prompt := fmt.Sprintf("To sign in to %s, open this URL in a browser:\n%s\n", name, u)
	if os.Getenv("SSH_CONNECTION") != "" {
		if host, port, ok := callbackAddress(u); ok {
			prompt += fmt.Sprintf("The sign-in returns to http://%s:%s on this machine. If your browser runs elsewhere, first run there: ssh -N -L %s:%s:%s <this host>\n", host, port, port, host, port)
		}
	}
	return prompt
}

// callbackAddress is the loopback host and port of the sign-in URL's
// redirect_uri: 127.0.0.1 (also for localhost) or [::1].
func callbackAddress(u string) (host, port string, ok bool) {
	parsed, err := url.Parse(u)
	if err != nil {
		return "", "", false
	}
	redirect, err := url.Parse(parsed.Query().Get("redirect_uri"))
	if err != nil {
		return "", "", false
	}
	port = redirect.Port()
	if _, err = strconv.ParseUint(port, 10, 16); err != nil {
		return "", "", false
	}
	switch redirect.Hostname() {
	case "127.0.0.1", "localhost":
		return "127.0.0.1", port, true
	case "::1":
		return "[::1]", port, true
	}
	return "", "", false
}
