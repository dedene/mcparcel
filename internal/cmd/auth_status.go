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

type authStatusData struct {
	Items    []authStatusItem  `json:"items"`
	Profiles []authProfileItem `json:"profiles"`
}

// authStatusItem keeps the stage 7 fields and adds the explanation. Events
// appear only for auth status <mcp>.
type authStatusItem struct {
	Connection            string             `json:"connection"`
	State                 string             `json:"state"`
	SignedIn              bool               `json:"signedIn"`
	RefreshToken          bool               `json:"refreshToken"`
	LastRefreshAt         string             `json:"lastRefreshAt,omitempty"`
	AccessTokenExpiresAt  string             `json:"accessTokenExpiresAt,omitempty"`
	RefreshTokenExpiresAt string             `json:"refreshTokenExpiresAt,omitempty"`
	LastRefreshFailure    *authStatusFailure `json:"lastRefreshFailure,omitempty"`
	KeepAlive             string             `json:"keepAlive,omitempty"`
	Cause                 *auth.SessionCause `json:"cause,omitempty"`
	PreviousCause         *auth.SessionCause `json:"previousCause,omitempty"`
	NextAction            string             `json:"nextAction,omitempty"`
	Events                []auth.HealthEvent `json:"events,omitempty"`
}
type authStatusFailure struct {
	At   string `json:"at"`
	Code string `json:"code"`
}

// Run reads Keychain items, the health file and the OAuth lock locally, and
// asks a running daemon for its 1Password sessions; it never starts the
// runtime and never prints a token, a redirect, a client identifier, an
// account or a reference.
func (c *AuthStatusCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	paths, err := runtimePaths()
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
	profile := ""
	if c.MCP != "" {
		id, err := config.ResolveID(c.MCP, effective.Aliases, ids)
		if err != nil {
			return err
		}
		ids = []string{id}
		if clientCredentialsConn(effective.Connections[id].Connection) {
			return nothingToSignIn(c.MCP)
		}
		if row := effective.Connections[id].Connection; row != nil && len(config.SecretRefs(*row)) > 0 {
			profile = row.CredentialProfile
		}
	}
	if paths.Headless() {
		// Headless mode stores no sign-in and reads no Keychain (Ruling 3);
		// its service-account profiles still have sessions.
		data := authStatusData{Items: []authStatusItem{}, Profiles: []authProfileItem{}}
		if c.MCP == "" || profile != "" {
			if rows := profileRows(ctx, paths, snapshot, profile); rows != nil {
				data.Profiles = rows
			}
		}
		if opts.JSON {
			return writeSuccess(s, opts, data)
		}
		var b strings.Builder
		b.WriteString("No sign-ins in headless mode.\n")
		writeProfileRows(&b, data.Profiles)
		return writeSuccess(s, opts, b.String())
	}
	sessions := newSessionReader(paths)
	data := authStatusData{Items: []authStatusItem{}, Profiles: []authProfileItem{}}
	for _, id := range ids {
		row := effective.Connections[id]
		if row.Connection == nil || row.Connection.Transport.HTTP == nil {
			if c.MCP != "" && profile == "" {
				e := output.NewError("invalid_arguments", nil)
				e.Message = c.MCP + " uses neither sign-in nor 1Password."
				e.NextAction = "" // nothing to do; not the default's tools hint
				return e
			}
			continue
		}
		if c.MCP == "" && !row.Enabled || clientCredentialsConn(row.Connection) {
			// A client_credentials token lives in daemon memory only.
			continue
		}
		name := id
		if c.MCP != "" {
			name = c.MCP
		}
		state, report, found, err := sessions.explain(ctx, id, name, *row.Connection)
		if err != nil {
			if c.MCP == "" && row.Connection.Auth == nil {
				// The listing shows an unmarked connection only when it holds
				// an item; one the keyring cannot read is left out.
				continue
			}
			var safe *output.Error
			if errors.As(err, &safe) && safe.Code == "keychain_unavailable" {
				// Keeps a pending keyring prompt's own text.
				return safe
			}
			return output.NewError("keychain_unavailable", nil)
		}
		if c.MCP == "" && !found && row.Connection.Auth == nil {
			continue
		}
		item := authStatusItem{
			Connection: id, State: report.State, RefreshToken: state.RefreshToken != "",
			LastRefreshAt: report.LastRefreshAt, AccessTokenExpiresAt: report.AccessTokenExpiresAt, RefreshTokenExpiresAt: report.RefreshTokenExpiresAt,
			KeepAlive: report.KeepAlive, Cause: report.Cause, PreviousCause: report.PreviousCause, NextAction: report.NextAction,
		}
		item.SignedIn = report.State != auth.StateSignInRequired
		if state.Failure != nil {
			item.LastRefreshFailure = &authStatusFailure{At: time.Unix(state.Failure.At, 0).UTC().Format(time.RFC3339), Code: state.Failure.Code}
		}
		if c.MCP != "" {
			item.Events = report.Events
		}
		data.Items = append(data.Items, item)
	}
	if c.MCP == "" || profile != "" {
		if rows := profileRows(ctx, paths, snapshot, profile); rows != nil {
			data.Profiles = rows
		}
	}
	if opts.JSON {
		return writeSuccess(s, opts, data)
	}
	var b strings.Builder
	for _, item := range data.Items {
		fmt.Fprintf(&b, "%s  %s", output.DisplayMetadata(item.Connection), item.State)
		if item.Cause != nil {
			fmt.Fprintf(&b, "  (%s)", output.DisplayMetadata(item.Cause.Code))
		}
		b.WriteString("\n")
		if c.MCP != "" {
			writeAuthStatusDetail(&b, item)
		}
	}
	if len(data.Items) == 0 && len(data.Profiles) == 0 {
		b.WriteString("No OAuth connections.\n")
	}
	writeProfileRows(&b, data.Profiles)
	return writeSuccess(s, opts, b.String())
}

// sessionReader explains stored OAuth sessions from the Keychain item, the
// health file and the OAuth lock. auth status and the setup summary share it.
type sessionReader struct {
	keyring auth.Keyring
	health  map[string]auth.ConnectionHealth
	locks   map[string]bool
	lockErr error
	now     time.Time
}

func newSessionReader(paths config.Paths) sessionReader {
	// An unreadable health file means no history; status still answers.
	health, _ := auth.ReadHealth(paths.StateDir)
	// An unreadable lock file fails closed, as in the daemon.
	locks, lockErr := auth.ReadOAuthLock(paths.StateDir)
	return sessionReader{keyring: keyringFactory(paths), health: health, locks: locks, lockErr: lockErr, now: time.Now()}
}

// explain reads id's Keychain item and explains it for an HTTP connection
// the user calls name. found is false when no item is stored; err is any
// other keyring failure.
func (r sessionReader) explain(ctx context.Context, id, name string, c config.Connection) (auth.OAuthState, auth.SessionReport, bool, error) {
	state, err := auth.LoadOAuth(ctx, r.keyring, id)
	if err != nil && !errors.Is(err, auth.ErrNoSession) {
		return state, auth.SessionReport{}, false, err
	}
	found := err == nil
	u, _ := config.LiteralText(c.Transport.HTTP.URL)
	report := auth.ExplainSession(auth.SessionInput{Connection: id, Name: name, URL: u, State: state, Found: found, Locked: r.lockErr != nil || r.locks[id], Health: r.health[id], KeepAlive: keepAliveSetting(c), Now: r.now})
	return state, report, found, nil
}

// keepAliveSetting is what auth status shows for keep-alive: "off", else
// "unavailable" when the client needs a 1Password reference, which the
// background never resolves (only a pooled session is kept alive), else the
// interval.
func keepAliveSetting(c config.Connection) string {
	setting := ""
	if c.Lifecycle != nil {
		setting = c.Lifecycle.KeepAlive
	}
	switch {
	case setting == "off":
		return "off"
	case len(config.SecretRefs(c)) > 0:
		return "unavailable"
	case setting == "":
		return "24h"
	}
	return setting
}

func writeAuthStatusDetail(b *strings.Builder, item authStatusItem) {
	if item.Cause != nil {
		fmt.Fprintf(b, "  %s\n", output.DisplayMetadata(item.Cause.Message))
	}
	if item.PreviousCause != nil {
		fmt.Fprintf(b, "  Last sign-in needed: %s\n", output.DisplayMetadata(item.PreviousCause.Message))
	}
	for _, line := range [][2]string{
		{"Last refresh", item.LastRefreshAt},
		{"Access token expires", item.AccessTokenExpiresAt},
		{"Refresh token expires", item.RefreshTokenExpiresAt},
		{"Keep-alive", item.KeepAlive},
		{"Next", item.NextAction},
	} {
		if line[1] != "" {
			fmt.Fprintf(b, "  %s: %s\n", line[0], output.DisplayMetadata(line[1]))
		}
	}
}
