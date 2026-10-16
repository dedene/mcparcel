package cmd

import (
	"context"
	"os"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
	"github.com/dedene/mcparcel/internal/ui"
)

// Setup reads stored sign-ins and looks up commands to show each server's
// Status; it reaches the runtime only when the user sets servers up (s).
// These functions run the same code as auth status, tools <mcp> and
// auth <mcp>.

// setupSignInState is ui.Options.StoredSignIn: whether auth <mcp> would
// sign in to the saved connection id in a browser, outside headless mode,
// and whether its stored sign-in reads "sign-in required" as auth status
// shows it. It reads the keyring without a prompt: an item it cannot read
// that way (a locked Linux keyring) is Unknown. An unmarked server can
// still ask for a sign-in when it connects.
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
	sessions := newSessionReader(paths)
	sessions.keyring = auth.NoPrompt(sessions.keyring)
	_, report, _, err := sessions.explain(ctx, canonical, canonical, c)
	st.Needed = err == nil && report.State == auth.StateSignInRequired
	st.Unknown = err != nil
	return st
}

// setupCommandAvailable is ui.Options.CommandAvailable: whether the
// runtime would find command on search, the PATH the connection sets. Else
// the runtime looks on the PATH of the login shell, which only it
// captures, so this looks on this process's PATH.
func setupCommandAvailable(command, search string) bool {
	if search == "" {
		search = os.Getenv("PATH")
	}
	if search == "" {
		search = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	_, err := mcpclient.Executable(command, search, "")
	return err == nil
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
