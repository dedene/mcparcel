package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
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
// honors --no-input.
func (c *AuthLoginCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	open := newBrowser(client.Paths)
	client.OnAuthURL = func(u string) {
		fmt.Fprintf(s.Err, "Opening your browser to sign in to %s.\nIf it does not open, visit:\n%s\n", c.MCP, u)
		_ = open(ctx, u)
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
// connection's session and its refreshes.
func (c *AuthLogoutCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	canonical := c.MCP
	if !strings.Contains(c.MCP, ":") || config.ValidateCanonicalID(c.MCP) != nil {
		paths, err := commandPaths()
		if err != nil {
			return err
		}
		snapshot, err := config.Load(paths)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(snapshot.Effective.Connections))
		for id := range snapshot.Effective.Connections {
			ids = append(ids, id)
		}
		if canonical, err = config.ResolveID(c.MCP, snapshot.Effective.Aliases, ids); err != nil {
			return err
		}
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
