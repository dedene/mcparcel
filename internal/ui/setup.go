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
	Color      bool                                                          // false when NO_COLOR is non-empty or TERM is "dumb" or empty; the depth comes from Bubble Tea
	Unicode    bool                                                          // glyphs beyond ASCII: a UTF-8 locale and TERM is not "dumb"
	SourceAges func(config.State) ([]output.SourceMetadata, error)           // recomputed after save and reload
	LoadTools  func(ctx context.Context, id string) (output.ToolList, error) // nil hides Connect and the finish step
	Describe   func(error) *output.Error                                     // cmd.safeFailure
	Now        func() time.Time

	// The finish step (finish.go) runs only after the user asks for it on the
	// screen a save offers; browsing, editing and saving never call these.
	//
	// NeedsSignIn says whether setup can sign in to a saved connection and
	// whether it needs to before it starts. It may read the Keychain. Nil
	// means no sign-in is known to be needed or possible.
	NeedsSignIn func(ctx context.Context, id string) SignInState
	// SignIn signs in to a saved connection as mcparcel auth <mcp> does. It
	// passes the sign-in URL to onURL (browser: a browser is opening it) and
	// must never write to the terminal. Nil (headless) leaves sign-ins to
	// mcparcel auth <mcp>.
	SignIn func(ctx context.Context, id string, onURL func(url string, browser bool)) error

	// Join opens setup on the join screens of one catalog (mcparcel join):
	// its 1Password suggestions, then its presets, then the list. Nil opens
	// the list.
	Join *Join
	// NoDesktopApp: the 1Password desktop app cannot read secrets here (not
	// macOS, or headless mode), so join does not ask about the desktop
	// profiles a catalog suggests.
	NoDesktopApp bool

	// ReduceMotion (MCPARCEL_REDUCE_MOTION) turns animation off; state
	// changes still render, without the transition. No color also turns it off.
	ReduceMotion bool
	// Tick schedules fn after d for an animation frame; nil uses tea.Tick.
	// Tests inject one that records the tick instead of sleeping.
	Tick func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd
}

// Result reports what a setup session saved.
type Result struct {
	Revision    uint64
	Saves       int
	Interrupted bool
	Unconfirmed bool // the last save failed with config_write_failed and no reload settled it

	// What the finish step left, by canonical ID, sorted; the latest run
	// wins. A connection setup never tried to start, or stopped before it
	// settled in its latest run, is in none of them.
	Started []string // started and its tools loaded, signed in where needed
	SignIn  []string // still needs a sign-in: skipped, stopped while signing in, or not possible here
	Failed  []string // could not start; mcparcel doctor says why
}

// Join names the catalog mcparcel join registered or found registered. The
// join screens read its suggestions and presets from the state.
type Join struct {
	SourceID   string // the GitHub source ID, such as github-42
	Repository string // owner/repo
	Commit     string // the snapshot's commit, shortened for display
}

// SignInState is what NeedsSignIn reports for a saved connection.
type SignInState struct {
	Possible bool // setup can sign in to it here: an OAuth sign-in, not headless
	Needed   bool // its stored sign-in is missing or no longer usable
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
	m := &Model{ctx: ctx, store: store, opts: opts, theme: newTheme(opts, nil), draft: draft}
	m.refreshSources()
	if opts.Join != nil {
		m.openJoin(*opts.Join)
	}
	return m, nil
}

// tick schedules an animation frame through Options.Tick.
func (m *Model) tick(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
	if m.opts.Tick != nil {
		return m.opts.Tick(d, fn)
	}
	return tea.Tick(d, fn)
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
	options := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithInput(opts.In),
		tea.WithOutput(opts.Out),
		tea.WithoutSignalHandler(),
	}
	_, err = tea.NewProgram(m, options...).Run()
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
