package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

type AuthCmd struct {
	Login  AuthLoginCmd  `cmd:"" help:"Sign in to an HTTP connection in the browser."`
	Status AuthStatusCmd `cmd:"" help:"Show stored sign-in state without contacting servers."`
	Logout AuthLogoutCmd `cmd:"" help:"Remove the stored sign-in for a connection."`
}
type (
	AuthLoginCmd struct {
		MCP string `arg:"" required:"" help:"Connection to sign in to."`
	}
	AuthStatusCmd struct {
		MCP string `arg:"" optional:"" help:"Connection to show; when omitted, OAuth connections, signed-in ones and ones whose server asked for sign-in."`
	}
	AuthLogoutCmd struct {
		MCP string `arg:"" required:"" help:"Connection whose stored sign-in to remove."`
	}
)

// Run sends the login to the runtime, which checks the connection before it
// honors --no-input. A client_credentials connection, and any connection in
// headless mode, is refused offline: no runtime starts.
func (c *AuthLoginCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	paths, err := runtimePaths()
	if err != nil {
		return err
	}
	if _, conn, err := authTarget(paths, c.MCP); err != nil {
		return err
	} else if clientCredentialsConn(conn) {
		return nothingToSignIn(c.MCP)
	}
	if paths.Headless() {
		return runtimeclient.HeadlessSignIn()
	}
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	open := newBrowser(client.Paths)
	client.OnAuthURL = func(u string) {
		fmt.Fprint(s.Err, signInPrompt(c.MCP, u, browserOpens))
		if browserOpens {
			_ = open(ctx, u)
		}
	}
	data, err := client.Login(ctx, c.MCP)
	if err != nil {
		return err
	}
	if opts.JSON {
		return writeSuccess(s, opts, data)
	}
	if !data.SignedIn {
		return writeSuccess(s, opts, c.MCP+" did not ask for sign-in. Nothing was stored.\n")
	}
	return writeSuccess(s, opts, "Signed in to "+c.MCP+".\n")
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
// open, or, where none can open, the URL to open themselves.
func signInPrompt(name, u string, opens bool) string {
	if opens {
		return fmt.Sprintf("Opening your browser to sign in to %s.\nIf it does not open, visit:\n%s\n", name, u)
	}
	return fmt.Sprintf("To sign in to %s, open this URL in a browser:\n%s\n", name, u)
}
