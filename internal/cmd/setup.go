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
	return finishSetup(s, result, err)
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
		// The runtime client is built only when tools load: on the tools
		// page, or when the user sets up (s).
		LoadTools: func(ctx context.Context, id string) (output.ToolList, error) {
			client, err := newRuntimeClient(opts)
			if err != nil {
				return output.ToolList{}, err
			}
			return client.Tools(ctx, id, false)
		},
		// Each server's Status (setup_finish.go): local reads, no prompt.
		StoredSignIn: func(ctx context.Context, id string) ui.SignInState {
			return setupSignInState(ctx, paths, store, id)
		},
		CommandAvailable: setupCommandAvailable,
		// Setting up (s): nothing here runs until the user presses it.
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

const unconfirmedSummary = "The last change could not be confirmed. Run mcparcel list to check it.\n"

// finishSetup prints the summary. Every change was written as it was made,
// so a finished setup says what is on and what is left, in one line, then
// what to run for the steps setup could not take: mcparcel setup once for
// the sign-ins s does, mcparcel auth for each it cannot do here, and
// doctor for a server that could not start. An interrupted setup (Ctrl+C or
// a signal) exits 130 without it. Either way it reports a change that could
// not be confirmed.
func finishSetup(s *Streams, result ui.Result, err error) error {
	interrupted := result.Interrupted || errors.Is(err, context.Canceled)
	if err != nil && !interrupted {
		return err
	}
	summary := ""
	if result.Unconfirmed {
		summary = unconfirmedSummary
	}
	if !interrupted {
		return writeSuccess(s, &CommandOptions{}, summary+setupSummary(result))
	}
	if summary != "" {
		if _, err := io.WriteString(s.Out, summary); err != nil {
			return &commandWriteFailure{err}
		}
	}
	return context.Canceled
}

// setupSummary is "9 on: 8 ready, Rails Blocks needs ..." or "9 on, all
// ready.", then the commands for what setup could not do. Labels, details
// and commands in a Step are display-safe.
func setupSummary(r ui.Result) string {
	if r.On == 0 {
		return "Nothing is on.\n"
	}
	if len(r.Steps) == 0 {
		return fmt.Sprintf("%d on, all ready.\n", r.On)
	}
	parts := []string{fmt.Sprintf("%d ready", r.Ready)}
	var commands []string
	setup, doctor := false, false
	for _, step := range r.Steps {
		switch step.Need {
		case ui.NeedReview:
			parts = append(parts, step.Label+" needs a review")
		case ui.NeedUnavailable:
			parts = append(parts, step.Label+" is unavailable")
		case ui.NeedInput:
			parts = append(parts, step.Label+" needs input "+step.Detail)
		case ui.NeedProfile:
			parts = append(parts, step.Label+" needs a 1Password profile")
		case ui.NeedCommand:
			parts = append(parts, step.Label+" needs "+step.Detail+" installed")
		case ui.NeedSignIn:
			parts = append(parts, step.Label+" needs a sign-in")
			if step.Command != "" {
				commands = append(commands, step.Command)
			} else {
				setup = true
			}
		case ui.NeedFailed:
			parts = append(parts, step.Label+" could not start")
			doctor = true
		}
	}
	if setup {
		commands = append([]string{"mcparcel setup, then s signs you in"}, commands...)
	}
	if doctor {
		commands = append(commands, "mcparcel doctor")
	}
	out := fmt.Sprintf("%d on: %s.\n", r.On, strings.Join(parts, ", "))
	for _, command := range commands {
		out += "  " + command + "\n"
	}
	return out
}
