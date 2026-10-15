package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/ui"
)

// SetupCmd opens the interactive setup UI. It renders on stderr and prints
// its summary on stdout.
type SetupCmd struct{}

func (c *SetupCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	// Both checks run before any config read, so a rejected setup reads and
	// writes nothing.
	if opts.JSON || opts.NoInput {
		return output.NewError("terminal_required", nil)
	}
	in, _ := s.In.(*os.File)
	errOut, _ := s.Err.(*os.File)
	if !newSetupTerminal(in, errOut) {
		return output.NewError("terminal_required", nil)
	}
	paths, err := commandPaths()
	if err != nil {
		return err
	}
	store := config.NewStore(paths)
	state, err := store.Read(ctx)
	if err != nil {
		return err
	}
	result, err := runSetupUI(ctx, store, state, setupOptions(paths, store, opts, in, errOut))
	return finishSetup(s, result, err, func(r ui.Result) ([]string, []string) { return setupHints(ctx, paths, store, r) })
}

// runSetupUI runs the setup program; tests drive the model instead.
var runSetupUI = ui.RunSetup

// setupOptions wires setup, and join, to the terminal and the runtime.
func setupOptions(paths config.Paths, store *config.Store, opts *CommandOptions, in, errOut *os.File) ui.Options {
	term := os.Getenv("TERM")
	return ui.Options{
		In:           in,
		Out:          errOut,
		Color:        os.Getenv("NO_COLOR") == "" && term != "" && term != "dumb",
		Unicode:      unicodeTerminal(os.Getenv),
		ReduceMotion: os.Getenv("MCPARCEL_REDUCE_MOTION") != "",
		SourceAges: func(st config.State) ([]output.SourceMetadata, error) {
			return sourceMetadata(paths, st, time.Now())
		},
		// The runtime client is built only when the user asks to connect.
		LoadTools: func(ctx context.Context, id string) (output.ToolList, error) {
			client, err := newRuntimeClient(opts)
			if err != nil {
				return output.ToolList{}, err
			}
			return client.Tools(ctx, id, false)
		},
		// The finish step a save offers: nothing here runs until the user
		// starts it (setup_finish.go).
		NeedsSignIn: func(ctx context.Context, id string) ui.SignInState {
			return setupSignInState(ctx, paths, store, id)
		},
		SignIn:       setupSignIn(paths, opts),
		NoDesktopApp: paths.Headless() || !desktopOnePassword(paths),
		Describe:     safeFailure,
		Now:          time.Now,
	}
}

// unicodeTerminal reports whether setup may draw glyphs beyond ASCII: the
// locale (the first of LC_ALL, LC_CTYPE and LANG that is set) is UTF-8 and
// TERM is not "dumb".
func unicodeTerminal(getenv func(string) string) bool {
	if getenv("TERM") == "dumb" {
		return false
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if value := strings.ToLower(getenv(name)); value != "" {
			return strings.Contains(value, "utf-8") || strings.Contains(value, "utf8")
		}
	}
	return false
}

const unconfirmedSummary = "The last save could not be confirmed. Run mcparcel list to check the configuration.\n"

// finishSetup prints the summary. A finished setup then lists what hints
// reports: the sign-ins still needed and the connections the finish step
// could not start. An interrupted setup (Ctrl+C or a signal) still reports a
// save that already happened, or one that could not be confirmed, then exits
// 130.
func finishSetup(s *Streams, result ui.Result, err error, hints func(ui.Result) (signIn, failed []string)) error {
	interrupted := result.Interrupted || errors.Is(err, context.Canceled)
	if err != nil && !interrupted {
		return err
	}
	summary := ""
	if result.Saves > 0 {
		summary = fmt.Sprintf("Configuration saved at revision %d.\n", result.Revision)
	}
	if result.Unconfirmed {
		summary += unconfirmedSummary
	}
	if !interrupted {
		if summary == "" {
			summary = "No changes saved.\n"
		}
		actions, failed := hints(result)
		if len(actions) > 0 {
			summary += "Sign in before first use:\n"
			for _, action := range actions {
				summary += "  " + output.DisplayMetadata(action) + "\n"
			}
		}
		if len(failed) > 0 {
			for i, name := range failed {
				failed[i] = output.DisplayMetadata(name)
			}
			summary += "Could not start " + strings.Join(failed, ", ") + ". Run mcparcel doctor to see why.\n"
		}
		return writeSuccess(s, &CommandOptions{}, summary)
	}
	if summary != "" {
		if _, err := io.WriteString(s.Out, summary); err != nil {
			return &commandWriteFailure{err}
		}
	}
	return context.Canceled
}
