package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
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
		MCP string `arg:"" optional:"" help:"Connection to show; all signed-in or OAuth connections when omitted."`
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

type authStatusData struct {
	Items []authStatusItem `json:"items"`
}
type authStatusItem struct {
	Connection           string             `json:"connection"`
	SignedIn             bool               `json:"signedIn"`
	RefreshToken         bool               `json:"refreshToken"`
	AccessTokenExpiresAt string             `json:"accessTokenExpiresAt,omitempty"`
	LastRefreshFailure   *authStatusFailure `json:"lastRefreshFailure,omitempty"`
}
type authStatusFailure struct {
	At   string `json:"at"`
	Code string `json:"code"`
}

// Run reads Keychain items locally; it never starts the runtime or prints a token.
func (c *AuthStatusCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	paths, err := commandPaths()
	if err != nil {
		return err
	}
	snapshot, err := config.Load(paths)
	if err != nil {
		return err
	}
	effective := snapshot.Effective
	ids := make([]string, 0, len(effective.Connections))
	for id := range effective.Connections {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if c.MCP != "" {
		id, err := config.ResolveID(c.MCP, effective.Aliases, ids)
		if err != nil {
			return err
		}
		ids = []string{id}
	}
	keyring := newKeyring(paths)
	data := authStatusData{Items: []authStatusItem{}}
	for _, id := range ids {
		row := effective.Connections[id]
		if row.Connection == nil || row.Connection.Transport.HTTP == nil {
			if c.MCP != "" {
				e := output.NewError("invalid_arguments", nil)
				e.Message = "Only HTTP connections use sign-in."
				return e
			}
			continue
		}
		if c.MCP == "" && !row.Enabled {
			continue
		}
		state, err := auth.LoadOAuth(ctx, keyring, id)
		found := err == nil
		if err != nil && !errors.Is(err, auth.ErrNoSession) {
			return output.NewError("keychain_unavailable", nil)
		}
		if c.MCP == "" && !found && row.Connection.Auth == nil {
			continue
		}
		u, _ := config.LiteralText(row.Connection.Transport.HTTP.URL)
		item := authStatusItem{Connection: id, RefreshToken: state.RefreshToken != ""}
		item.SignedIn = found && state.URL == u && state.Failure == nil && item.RefreshToken
		if state.AccessExpiry != 0 {
			item.AccessTokenExpiresAt = time.Unix(state.AccessExpiry, 0).UTC().Format(time.RFC3339)
		}
		if state.Failure != nil {
			item.LastRefreshFailure = &authStatusFailure{At: time.Unix(state.Failure.At, 0).UTC().Format(time.RFC3339), Code: state.Failure.Code}
		}
		data.Items = append(data.Items, item)
	}
	if opts.JSON {
		return writeSuccess(s, opts, data)
	}
	var b strings.Builder
	for _, item := range data.Items {
		fmt.Fprintf(&b, "%s  ", output.DisplayMetadata(item.Connection))
		switch {
		case item.SignedIn:
			b.WriteString("signed in  refresh token: yes\n")
		case item.LastRefreshFailure != nil:
			fmt.Fprintf(&b, "sign-in required (last refresh failed: %s at %s)\n", output.DisplayMetadata(item.LastRefreshFailure.Code), item.LastRefreshFailure.At)
		default:
			b.WriteString("sign-in required\n")
		}
	}
	if len(data.Items) == 0 {
		b.WriteString("No OAuth connections.\n")
	}
	return writeSuccess(s, opts, b.String())
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
