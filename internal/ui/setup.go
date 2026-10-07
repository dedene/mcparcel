// Package ui implements the interactive `mcparcel setup` terminal UI.
package ui

import (
	"context"
	"errors"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// Options injects everything setup needs from the CLI.
type Options struct {
	In         io.Reader                                                     // terminal stdin (*os.File in production; never nil)
	Out        io.Writer                                                     // terminal stderr; the UI renders here
	Color      bool                                                          // false when NO_COLOR is non-empty or TERM is "dumb" or empty
	SourceAges func(config.State) ([]output.SourceMetadata, error)           // recomputed after save and reload
	LoadTools  func(ctx context.Context, id string) (output.ToolList, error) // nil hides Connect
	Describe   func(error) *output.Error                                     // cmd.safeFailure
	Now        func() time.Time
}

// Result reports what a setup session saved.
type Result struct {
	Revision    uint64
	Saves       int
	Interrupted bool
	Unconfirmed bool // the last save failed with config_write_failed and no reload settled it
}

// NewModel builds the setup model on the initial state.
func NewModel(ctx context.Context, store *config.Store, initial config.State, opts Options) (*Model, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	draft, err := NewDraft(initial)
	if err != nil {
		return nil, err
	}
	m := &Model{ctx: ctx, store: store, opts: opts, style: style{color: opts.Color}, draft: draft}
	m.refreshSources()
	return m, nil
}

// RunSetup runs the setup UI until the user saves and leaves, cancels or
// quits. A canceled context returns its error.
func RunSetup(ctx context.Context, store *config.Store, initial config.State, opts Options) (Result, error) {
	if opts.In == nil || opts.Out == nil {
		return Result{}, errors.New("setup needs terminal input and output")
	}
	m, err := NewModel(ctx, store, initial, opts)
	if err != nil {
		return Result{}, err
	}
	p := tea.NewProgram(m,
		tea.WithContext(ctx),
		tea.WithInput(opts.In),
		tea.WithOutput(opts.Out),
		tea.WithoutSignalHandler(),
	)
	_, err = p.Run()
	result := m.Result()
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		result.Interrupted = true
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, context.Canceled
	}
	return result, err
}
