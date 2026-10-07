package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

type AuthCmd struct {
	Login   AuthLoginCmd   `cmd:"" help:"Sign in to an HTTP connection in the browser."`
	Status  AuthStatusCmd  `cmd:"" help:"Show stored sign-in and 1Password session state without contacting servers."`
	Logout  AuthLogoutCmd  `cmd:"" help:"Remove the stored sign-in for a connection."`
	Lock    AuthLockCmd    `cmd:"" help:"End 1Password sessions and block stored sign-ins until the next auth login."`
	Refresh AuthRefreshCmd `cmd:"" help:"Read a connection's 1Password secrets again on its next call."`
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
	AuthLockCmd    struct{}
	AuthRefreshCmd struct {
		MCP string `arg:"" required:"" help:"Connection whose cached 1Password values to drop."`
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
	canonical, err := canonicalConnection(c.MCP)
	if err != nil {
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

// canonicalConnection resolves a name or alias to its canonical ID; a
// canonical ID is taken as is, so a removed connection can still be named.
func canonicalConnection(name string) (string, error) {
	if strings.Contains(name, ":") && config.ValidateCanonicalID(name) == nil {
		return name, nil
	}
	paths, err := commandPaths()
	if err != nil {
		return "", err
	}
	snapshot, err := config.Load(paths)
	if err != nil {
		return "", err
	}
	ids := make([]string, 0, len(snapshot.Effective.Connections))
	for id := range snapshot.Effective.Connections {
		ids = append(ids, id)
	}
	return config.ResolveID(name, snapshot.Effective.Aliases, ids)
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
	return writeSuccess(s, opts, "Locked. 1Password sessions ended; signed-in connections need mcparcel auth login.\n")
}

// Run drops the connection's cached 1Password values; it never starts the
// runtime. The connection is checked first, so a connection without op://
// references fails whether or not the runtime runs.
func (c *AuthRefreshCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	canonical, err := canonicalConnection(c.MCP)
	if err != nil {
		return err
	}
	paths, err := commandPaths()
	if err != nil {
		return err
	}
	snapshot, err := config.Load(paths)
	if err != nil {
		return err
	}
	if _, _, _, err = runtimeclient.RefreshTarget(snapshot, canonical); err != nil {
		return err
	}
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	data, err := client.Refresh(ctx, canonical)
	if err != nil {
		return err
	}
	if opts.JSON {
		return writeSuccess(s, opts, data)
	}
	if !data.Invalidated {
		return writeSuccess(s, opts, c.MCP+": nothing cached; the runtime is not running.\n")
	}
	return writeSuccess(s, opts, c.MCP+": the next call reads its 1Password secrets again.\n")
}
