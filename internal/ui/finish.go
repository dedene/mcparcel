package ui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/output"
)

// The finish step. A save that turned connections on, or changed ones that
// are on, offers to start them; until the user presses Enter on that offer
// nothing contacts the runtime or reads a credential. Then each connection
// checks its stored sign-in, starts and loads its tools, finishLimit at a
// time, through Options.NeedsSignIn and Options.LoadTools. Those that need a
// browser sign-in line up and sign in one at a time, each on Enter, through
// Options.SignIn; a slow or failing connection never holds up the others.

// finishLimit is how many connections start at once.
const finishLimit = 3

type finishState int

const (
	finWaiting  finishState = iota // waiting for a free slot
	finStarting                    // checking its sign-in, starting, loading tools
	finQueued                      // needs a sign-in, behind another one
	finAsk                         // needs a sign-in, next in line: Enter signs in
	finSigning                     // waiting for the browser sign-in
	finStarted                     // started, tools loaded
	finFailed
	finSkipped
	finManual  // needs a sign-in setup cannot do here: mcparcel auth <mcp>
	finStopped // stopped before it settled
)

// finishRow is one connection on the finish screen. Its seq changes with
// every attempt, so a result from an older one is ignored.
type finishRow struct {
	id, label string
	state     finishState
	signIn    SignInState
	signedIn  bool // signed in during this run; its next load follows it
	tools     int
	reason    string // why it failed, display-safe
	url       string // the sign-in URL, display-safe
	browser   bool   // a browser is opening the URL
	seq       int
	cancel    context.CancelFunc
}

// finishRun is the finish screen: an offer until started.
type finishRun struct {
	rows    []*finishRow
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	queue   []*finishRow // sign-ins in line, in the order they were found
	offset  int          // scroll position of the rows
	back    screen       // where Esc on the offer and Enter when done return to
}

type (
	finishMsg struct {
		row    *finishRow
		seq    int
		signIn SignInState
		list   output.ToolList
		err    error
	}
	signInURLMsg struct {
		row     *finishRow
		seq     int
		url     string
		browser bool
		next    tea.Cmd // waits for the sign-in to end
	}
	signInMsg struct {
		row *finishRow
		seq int
		err error
	}
)

// working reports whether a connection is starting, waiting to or signing in.
func (f *finishRun) working() bool {
	return f != nil && f.started && slices.ContainsFunc(f.rows, func(r *finishRow) bool {
		return r.state == finWaiting || r.state == finStarting || r.state == finSigning
	})
}

// settled reports whether every connection has an outcome.
func (f *finishRun) settled() bool {
	return !slices.ContainsFunc(f.rows, func(r *finishRow) bool { return r.state < finStarted })
}

func (f *finishRun) count(state finishState) int {
	n := 0
	for _, r := range f.rows {
		if r.state == state {
			n++
		}
	}
	return n
}

// current is the connection whose sign-in is asked for or running, or nil.
func (f *finishRun) current() *finishRow {
	for _, r := range f.rows {
		if r.state == finAsk || r.state == finSigning {
			return r
		}
	}
	return nil
}

// noteSaved adds the connections a save changed to those the finish step
// offers: each one that is now saved on and ready. ids are the connections
// the save changed (Draft.pendingIDs before the save).
func (m *Model) noteSaved(ids []string) {
	state, effective := m.draft.State(), m.draft.Effective()
	for _, id := range ids {
		row, ok := effective.Connections[id]
		if ok && lookOf(state, row).ready() && !slices.Contains(m.unoffered, id) {
			m.unoffered = append(m.unoffered, id)
		}
	}
}

// offerFinish shows the offer for the connections saved since the last one,
// in label order. It contacts nothing.
func (m *Model) offerFinish() {
	if m.opts.LoadTools == nil || len(m.unoffered) == 0 {
		return
	}
	effective := m.draft.Effective()
	run := &finishRun{back: m.screen}
	for _, id := range m.unoffered {
		if row, ok := effective.Connections[id]; ok && lookOf(m.draft.State(), row).ready() {
			run.rows = append(run.rows, &finishRow{id: id, label: rowLabel(row)})
		}
	}
	if len(run.rows) == 0 {
		return
	}
	slices.SortStableFunc(run.rows, func(a, b *finishRow) int { return strings.Compare(a.label, b.label) })
	m.finish, m.screen = run, screenFinish
}

// startFinish is Enter on the offer: the first contact with the runtime.
func (m *Model) startFinish() tea.Cmd {
	f := m.finish
	f.started = true
	f.ctx, f.cancel = context.WithCancel(m.ctx)
	m.unoffered = nil
	m.spinFrame = 0
	return m.pump()
}

// pump starts waiting connections while fewer than finishLimit run (those
// just signed in first), asks for the next sign-in when none is asked for or
// running, and keeps the spinner going.
func (m *Model) pump() tea.Cmd {
	f := m.finish
	active := f.count(finStarting)
	var cmds []tea.Cmd
	for _, signedIn := range []bool{true, false} {
		for _, r := range f.rows {
			if active < finishLimit && r.state == finWaiting && r.signedIn == signedIn {
				cmds = append(cmds, m.startRow(r))
				active++
			}
		}
	}
	if f.current() == nil && len(f.queue) > 0 {
		f.queue[0].state = finAsk
		f.queue = f.queue[1:]
	}
	return tea.Batch(append(cmds, m.spin())...)
}

// startRow checks the connection's stored sign-in, unless it just signed in,
// then starts it and loads its tools.
func (m *Model) startRow(r *finishRow) tea.Cmd {
	ctx, cancel := context.WithCancel(m.finish.ctx)
	r.seq++
	r.state, r.cancel, r.reason = finStarting, cancel, ""
	id, seq, check, load := r.id, r.seq, m.opts.NeedsSignIn, m.opts.LoadTools
	// A sign-in just now settles what the check said; it is not asked again.
	msg := finishMsg{row: r, seq: seq, signIn: SignInState{Possible: r.signIn.Possible}}
	signedIn := r.signedIn
	return func() tea.Msg {
		defer cancel()
		if check != nil && !signedIn {
			if msg.signIn = check(ctx, id); msg.signIn.Needed {
				return msg
			}
		}
		msg.list, msg.err = load(ctx, id)
		return msg
	}
}

func (m *Model) rowFinished(msg finishMsg) tea.Cmd {
	r := msg.row
	if m.finish == nil || r.seq != msg.seq || r.state != finStarting {
		return nil
	}
	r.cancel, r.signIn = nil, msg.signIn
	var failure *output.Error
	if msg.err != nil {
		failure = m.failure(msg.err)
	}
	switch {
	case msg.signIn.Needed:
		m.needSignIn(r)
	case failure == nil:
		r.state, r.tools = finStarted, m.keepTools(r.id, msg.list)
	case !r.signedIn && (failure.Code == "auth_required" || failure.Code == "auth_expired"):
		// The server asked for a sign-in its stored state did not predict:
		// an unmarked OAuth server, or an expired session.
		m.needSignIn(r)
	default:
		r.state, r.reason = finFailed, failure.Message
		m.keepFailure(r.id, msg.err)
	}
	return m.pump()
}

// keepFailure keeps why a connection could not start for this session, as
// a failed Connect does: the list and details show it after the finish step.
func (m *Model) keepFailure(id string, err error) {
	if m.loadErr == nil {
		m.loadErr = map[string][]string{}
	}
	m.loadErr[id] = m.connectFailure(id, err)
}

// needSignIn lines a connection up for a sign-in, or, where setup cannot
// sign in (headless, or not an OAuth sign-in), points at mcparcel auth.
func (m *Model) needSignIn(r *finishRow) {
	if !r.signIn.Possible || m.opts.SignIn == nil {
		r.state = finManual
		return
	}
	r.state = finQueued
	m.finish.queue = append(m.finish.queue, r)
}

// signIn is Enter on the connection asked for: Options.SignIn runs in its
// own goroutine, so its URL can reach the screen while it waits.
func (m *Model) signIn(r *finishRow) tea.Cmd {
	ctx, cancel := context.WithCancel(m.finish.ctx)
	r.seq++
	r.state, r.cancel, r.url = finSigning, cancel, ""
	row, id, seq, sign := r, r.id, r.seq, m.opts.SignIn
	urls := make(chan signInURLMsg, 1)
	done := make(chan error, 1)
	return tea.Batch(func() tea.Msg {
		go func() {
			done <- sign(ctx, id, func(u string, browser bool) {
				select { // only the first URL is shown; never block the sign-in
				case urls <- signInURLMsg{row: row, seq: seq, url: u, browser: browser}:
				default:
				}
			})
			cancel()
		}()
		return waitSignIn(row, seq, urls, done)()
	}, m.spin())
}

// waitSignIn delivers the sign-in URL, then waits again, or the result.
func waitSignIn(row *finishRow, seq int, urls chan signInURLMsg, done chan error) tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-urls:
			msg.next = waitSignIn(row, seq, urls, done)
			return msg
		case err := <-done:
			return signInMsg{row: row, seq: seq, err: err}
		}
	}
}

func (m *Model) signInURL(msg signInURLMsg) tea.Cmd {
	if m.finish != nil && msg.row.seq == msg.seq && msg.row.state == finSigning {
		msg.row.url, msg.row.browser = output.DisplayMetadata(msg.url), msg.browser
	}
	return msg.next // the wait ends with the sign-in, stale or not
}

func (m *Model) signedIn(msg signInMsg) tea.Cmd {
	r := msg.row
	if m.finish == nil || r.seq != msg.seq || r.state != finSigning {
		return nil
	}
	r.cancel, r.url = nil, ""
	if msg.err != nil {
		r.state, r.reason = finFailed, m.failure(msg.err).Message
		m.keepFailure(r.id, msg.err)
	} else {
		r.state, r.signedIn = finWaiting, true
	}
	return m.pump()
}

// skip leaves the current sign-in for later, canceling it if it runs.
func (m *Model) skip() tea.Cmd {
	r := m.finish.current()
	if r == nil {
		return nil
	}
	if r.cancel != nil {
		r.cancel()
	}
	r.seq++
	r.state, r.cancel, r.url = finSkipped, nil, ""
	return m.pump()
}

// retry starts the failed connections again.
func (m *Model) retry() tea.Cmd {
	for _, r := range m.finish.rows {
		if r.state == finFailed {
			r.state, r.signedIn = finWaiting, false
		}
	}
	return m.pump()
}

// stopFinish cancels everything still running and settles each connection:
// one in line for or in a sign-in is skipped, the rest stopped.
func (m *Model) stopFinish() {
	f := m.finish
	if f == nil || !f.started {
		return
	}
	f.cancel()
	for _, r := range f.rows {
		switch r.state {
		case finQueued, finAsk, finSigning:
			r.state = finSkipped
		case finWaiting, finStarting:
			r.state = finStopped
		}
		r.seq++
		r.cancel, r.url = nil, ""
	}
	f.queue = nil
}

// leaveFinish ends the finish screen: it keeps each outcome for the exit
// summary and returns where the offer came from. A connection stopped before
// it settled has none, whatever an earlier run left.
func (m *Model) leaveFinish() {
	f := m.finish
	m.stopFinish()
	if f.started {
		if m.outcomes == nil {
			m.outcomes = finishOutcomes{}
		}
		for _, r := range f.rows {
			switch r.state {
			case finStarted:
				m.outcomes[r.id] = finStarted
			case finFailed:
				m.outcomes[r.id] = finFailed
			case finSkipped, finManual:
				m.outcomes[r.id] = finSkipped
			case finStopped:
				delete(m.outcomes, r.id)
			}
		}
		m.status = fmt.Sprintf("%d of %d ready.", f.count(finStarted), len(f.rows))
		if f.count(finStarted) < len(f.rows) {
			m.status += " What is left is listed when setup ends."
		}
	}
	m.finish, m.screen = nil, f.back
	m.clamp()
}

// finishOutcomes is the latest outcome of each connection the finish step
// tried: finStarted, finFailed or finSkipped (still needs a sign-in).
type finishOutcomes map[string]finishState

func (o finishOutcomes) lists() (started, signIn, failed []string) {
	for _, id := range slices.Sorted(maps.Keys(o)) {
		switch o[id] {
		case finStarted:
			started = append(started, id)
		case finFailed:
			failed = append(failed, id)
		default:
			signIn = append(signIn, id)
		}
	}
	return started, signIn, failed
}

func (m *Model) finishKey(k string) tea.Cmd {
	f := m.finish
	if !f.started {
		switch k {
		case "enter":
			return m.startFinish()
		case "esc", "q":
			m.leaveFinish()
		}
		return nil
	}
	switch k {
	case "enter":
		if r := f.current(); r != nil && r.state == finAsk {
			return m.signIn(r)
		}
		if f.settled() {
			m.leaveFinish()
		}
	case "s":
		return m.skip()
	case "r":
		return m.retry()
	case "up":
		f.offset--
	case "down":
		f.offset++
	case "esc", "q":
		m.leaveFinish()
	}
	return nil
}
