package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
	term := os.Getenv("TERM")
	result, err := ui.RunSetup(ctx, store, state, ui.Options{
		In:    in,
		Out:   errOut,
		Color: os.Getenv("NO_COLOR") == "" && term != "" && term != "dumb",
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
		Describe: safeFailure,
		Now:      time.Now,
	})
	return finishSetup(s, result, err)
}

const unconfirmedSummary = "The last save could not be confirmed. Run mcparcel list to check the configuration.\n"

// finishSetup prints the summary. An interrupted setup (Ctrl+C or a signal)
// still reports a save that already happened, or one that could not be
// confirmed, then exits 130.
func finishSetup(s *Streams, result ui.Result, err error) error {
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
		return writeSuccess(s, &CommandOptions{}, summary)
	}
	if summary != "" {
		if _, err := io.WriteString(s.Out, summary); err != nil {
			return &commandWriteFailure{err}
		}
	}
	return context.Canceled
}
