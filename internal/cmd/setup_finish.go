package cmd

import (
	"context"
	"maps"
	"slices"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
	"github.com/dedene/mcparcel/internal/ui"
)

// Setup's finish step reaches the runtime and the Keychain only through
// these functions, which the UI calls after the user asks it to start the
// saved connections. They run the same code as tools <mcp>, auth <mcp> and
// auth status.

// setupSignInState is ui.Options.NeedsSignIn: whether auth <mcp> would sign
// in to the saved connection id in a browser, outside headless mode, and
// whether its stored sign-in reads "sign-in required" as auth status shows
// it. An unmarked server can still ask for a sign-in when it connects.
func setupSignInState(ctx context.Context, paths config.Paths, store *config.Store, id string) ui.SignInState {
	if paths.Headless() {
		return ui.SignInState{}
	}
	state, err := store.Read(ctx)
	if err != nil {
		return ui.SignInState{}
	}
	snap, err := config.NewSnapshot(state)
	if err != nil {
		return ui.SignInState{}
	}
	canonical, c, err := snap.RuntimeConnection(id)
	if err != nil || !runtimeclient.PlansSignIn(c) {
		return ui.SignInState{}
	}
	st := ui.SignInState{Possible: true}
	if c.Transport.HTTP == nil || c.Auth == nil {
		return st
	}
	_, report, _, err := newSessionReader(paths).explain(ctx, canonical, canonical, c)
	st.Needed = err == nil && report.State == auth.StateSignInRequired
	return st
}

// setupSignIn is ui.Options.SignIn: auth <mcp>'s offline refusals, then its
// sign-in through the runtime. The URL goes to the screen, never to stderr,
// which the setup UI owns; the browser opens on its own goroutine, so a slow
// opener never holds up the runtime's frames. Headless mode has none.
func setupSignIn(paths config.Paths, opts *CommandOptions) func(context.Context, string, func(string, bool)) error {
	if paths.Headless() {
		return nil
	}
	return func(ctx context.Context, id string, onURL func(string, bool)) error {
		paths, err := runtimePaths()
		if err != nil {
			return err
		}
		snapshot, err := config.Load(paths)
		if err != nil {
			return err
		}
		plan, err := runtimeclient.PlanAuth(snapshot, id)
		if err != nil {
			return err
		}
		if err := signInPreflight(ctx, paths, plan, id, false); err != nil {
			return err
		}
		client, err := authRuntimeClient(opts)
		if err != nil {
			return err
		}
		open := newBrowser(client.Paths)
		client.OnAuthURL = func(u string) {
			opens := browserOpens()
			onURL(u, opens)
			if opens {
				go func() { _ = open(ctx, u) }()
			}
		}
		_, err = client.Auth(ctx, id)
		return err
	}
}

// setupHints is what the setup summary still asks for. signIn has
// "mcparcel auth <mcp>" for each enabled OAuth connection whose stored
// sign-in needs one (as auth status explains it) and each one the finish
// step left needing one; failed names each connection it could not start.
// One it started is in neither, and one in both, such as a failed browser
// sign-in, is listed once, under the sign-in it still needs. Only a connection auth <mcp> would run is
// listed from the Keychain: one under review, missing configuration or on an
// unsupported transport is left out, as is one whose item cannot be read.
// Headless mode stores no sign-ins: a connection left needing one counts as
// failed.
func setupHints(ctx context.Context, paths config.Paths, store *config.Store, result ui.Result) (signIn, failed []string) {
	snap, err := setupSnapshot(ctx, store)
	name := func(id string) string { return id }
	need := map[string]bool{}
	if err == nil {
		name = func(id string) string { return authName(snap.Effective, id) }
		if !paths.Headless() {
			for _, id := range storedSignIns(ctx, paths, snap) {
				need[id] = true
			}
		}
	}
	failedIDs := slices.Clone(result.Failed)
	for _, id := range result.SignIn {
		if paths.Headless() {
			failedIDs = append(failedIDs, id)
		} else {
			need[id] = true
		}
	}
	for _, id := range result.Started {
		delete(need, id)
	}
	failedIDs = slices.DeleteFunc(failedIDs, func(id string) bool { return need[id] })
	for _, id := range slices.Sorted(maps.Keys(need)) {
		signIn = append(signIn, output.AuthAction(name(id), id))
	}
	slices.Sort(failedIDs)
	for _, id := range failedIDs {
		failed = append(failed, output.AuthTarget(name(id), id))
	}
	return signIn, failed
}

func setupSnapshot(ctx context.Context, store *config.Store) (config.Snapshot, error) {
	state, err := store.Read(ctx)
	if err != nil {
		return config.Snapshot{}, err
	}
	return config.NewSnapshot(state)
}

// storedSignIns lists, sorted, the enabled OAuth connections (not
// client_credentials) auth <mcp> would run whose stored sign-in reads
// "sign-in required".
func storedSignIns(ctx context.Context, paths config.Paths, snap config.Snapshot) []string {
	effective := snap.Effective
	var sessions *sessionReader
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(effective.Connections)) {
		row := effective.Connections[id]
		c := row.Connection
		if !row.Enabled || c == nil || c.Transport.HTTP == nil || c.Auth == nil || clientCredentialsConn(c) {
			continue
		}
		if _, _, err := snap.RuntimeConnection(id); err != nil {
			continue
		}
		if sessions == nil {
			r := newSessionReader(paths)
			sessions = &r
		}
		_, report, _, err := sessions.explain(ctx, id, authName(effective, id), *c)
		if err == nil && report.State == auth.StateSignInRequired {
			ids = append(ids, id)
		}
	}
	return ids
}

// authName is the short name a command accepts for id: a personal
// connection's base name when it resolves back to id, else id.
func authName(effective *config.EffectiveConfig, id string) string {
	base := config.ConnectionBase(id)
	if base == "" {
		return id
	}
	ids := slices.Collect(maps.Keys(effective.Connections))
	if resolved, err := config.ResolveID(base, effective.Aliases, ids); err == nil && resolved == id {
		return base
	}
	return id
}
