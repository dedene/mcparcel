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
	SourceAges func(config.State) ([]output.SourceMetadata, error)           // recomputed after each change
	LoadTools  func(ctx context.Context, id string) (output.ToolList, error) // nil: no tools load and s sets nothing up
	Describe   func(error) *output.Error                                     // cmd.safeFailure
	Now        func() time.Time

	// The list's Status column reads what each server needs locally. Both
	// are read only from this device: never a prompt, the network or the
	// runtime.
	//
	// StoredSignIn reads the stored sign-in of a saved connection, as
	// mcparcel auth status does: whether setup can sign in to it here and
	// whether it needs to. A stored sign-in that cannot be read without a
	// prompt (a locked keyring) is Unknown. Setup reads it for the servers
	// that are on and sign in through a browser, keeps it for the session
	// and reads it again after a sign-in. Nil means no sign-in is known to
	// be needed or possible.
	StoredSignIn func(ctx context.Context, id string) SignInState
	// CommandAvailable reports whether a local command a server runs is
	// found on search, the PATH the connection sets ("": the PATH setup runs
	// with), or exists at its absolute path. Nil: not checked.
	CommandAvailable func(command, search string) bool

	// Setting up (s, finish.go) starts servers and signs in only when the
	// user asks for it; browsing and changes never call these.
	//
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

// Result reports how a setup session ended. Every change was written as it
// was made.
type Result struct {
	Interrupted bool
	Unconfirmed bool // the last change failed part way (config_write_failed) and no read settled it

	On    int    // servers on when setup ended
	Ready int    // of those, the ones with no step left
	Steps []Step // the others, by label
}

// Step is a server that is on with a step left, as its Status reads.
type Step struct {
	ID      string // canonical connection ID
	Label   string // display-safe
	Need    Need
	Detail  string // the input or the command it misses, display-safe
	Command string // a sign-in setup cannot do here: mcparcel auth <mcp>, display-safe; "" when s does it
}

// Need is the step a server that is on has left.
type Need int

const (
	NeedReview      Need = iota // changed upstream: a review to accept
	NeedUnavailable             // its source no longer defines it
	NeedInput                   // an input to fill in (Detail)
	NeedProfile                 // a 1Password profile to link
	NeedCommand                 // its command (Detail) is not found on PATH
	NeedSignIn                  // a browser sign-in: s, or Command where setup cannot
	NeedFailed                  // it could not start: mcparcel doctor
)

// Join names the catalog mcparcel join registered or found registered. The
// join screens read its suggestions and presets from the state.
type Join struct {
	SourceID   string // the GitHub source ID, such as github-42
	Repository string // owner/repo
	Commit     string // the snapshot's commit, shortened for display
}

// SignInState is what StoredSignIn reports for a saved connection.
type SignInState struct {
	Possible bool // setup can sign in to it here: an OAuth sign-in, not headless
	Needed   bool // its stored sign-in is missing or no longer usable
	Unknown  bool // its stored sign-in could not be read without a prompt
}

// NewModel builds the setup model on the initial state.
func NewModel(ctx context.Context, store *config.Store, initial config.State, opts Options) (*Model, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	state, err := newLive(initial)
	if err != nil {
		return nil, err
	}
	m := &Model{ctx: ctx, store: store, opts: opts, theme: newTheme(opts, nil), live: state, opened: initial}
	m.refreshSources()
	if opts.Join != nil {
		m.openJoin(*opts.Join)
	} else {
		m.landStep = true
		m.landOnStep()
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

// RunSetup runs the setup UI until the user quits. A canceled context
// returns its error.
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
