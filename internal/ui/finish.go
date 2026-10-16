package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// Setting up: s on the list starts every server that is on, waits on
// nothing from the user and has not loaded its tools this session; s on a
// server page starts that one. Nothing else starts a server or signs in. It
// runs in place: each row's Status shows its phase while the list stays
// usable. Each server checks its stored sign-in, starts and loads its
// tools, finishLimit at a time, through Options.StoredSignIn and
// Options.LoadTools. Those that need a browser sign-in line up and sign in
// one at a time through Options.SignIn; a slow or failing server never
// holds up the others. Esc skips the sign-in that runs, q stops it all.

// finishLimit is how many servers start at once.
const finishLimit = 3

type finishState int

const (
	finWaiting  finishState = iota // waiting for a free slot
	finStarting                    // checking its sign-in, starting, loading tools
	finQueued                      // needs a sign-in, behind another one
	finSigning                     // waiting for the browser sign-in
	finStarted                     // started, tools loaded
	finFailed
	finSkipped // still needs a sign-in: skipped, or stopped in line for one
	finManual  // needs a sign-in setup cannot do here: mcparcel auth <mcp>
	finStopped // stopped before it settled
)

// finishRow is one server setting up. Its seq changes with every attempt,
// so a result from an older one is ignored.
type finishRow struct {
	id, label string
	state     finishState
	signIn    SignInState
	signedIn  bool   // signed in during this run; its next load follows it
	url       string // the sign-in URL, display-safe
	browser   bool   // a browser is opening the URL
	seq       int
	cancel    context.CancelFunc
}

// finishRun is a set-up run, while it runs.
type finishRun struct {
	rows   []*finishRow
	ctx    context.Context
	cancel context.CancelFunc
	queue  []*finishRow // sign-ins in line, in the order they were found
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

// row is the run's row for a connection, or nil.
func (f *finishRun) row(id string) *finishRow {
	if f == nil {
		return nil
	}
	for _, r := range f.rows {
		if r.id == id {
			return r
		}
	}
	return nil
}

// working reports whether a server is starting, waiting to or signing in.
func (f *finishRun) working() bool {
	return f != nil && slices.ContainsFunc(f.rows, func(r *finishRow) bool {
		return r.state == finWaiting || r.state == finStarting || r.state == finSigning
	})
}

// settled reports whether every server has an outcome.
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

// current is the server whose sign-in runs, or nil.
func (f *finishRun) current() *finishRow {
	if f == nil {
		return nil
	}
	for _, r := range f.rows {
		if r.state == finSigning {
			return r
		}
	}
	return nil
}

// setUpAll is s on the list: it sets up every server that is on and only
// waits on setup.
func (m *Model) setUpAll() tea.Cmd {
	if !m.canSetUp() {
		return nil
	}
	m.commands = nil // look for commands again: one may be installed now
	var ids []string
	on, blocked := 0, 0
	for _, row := range m.live.Effective().Connections {
		if !row.Enabled {
			continue
		}
		on++
		switch s := m.statusOf(row); {
		case s.blocked():
			blocked++
		case s.kind != stTools:
			ids = append(ids, row.ID)
		}
	}
	switch {
	case len(ids) > 0:
		return m.startRun(ids)
	case on == 0:
		m.status = "Nothing to set up: turn a server on with Space first."
	case blocked > 0:
		m.status = "Nothing setup can do on its own: open a server to see what it needs."
	default:
		m.status = "Everything that is on is set up."
	}
	return nil
}

// setUpOne is s on a server page: it sets up that server, even one that
// loaded its tools already.
func (m *Model) setUpOne(row config.EffectiveConnection) tea.Cmd {
	if !m.canSetUp() {
		return nil
	}
	m.lookAgain(row)
	switch s := m.statusOf(row); {
	case !row.Enabled:
		m.status = rowLabel(row) + " is off. Space turns it on."
	case s.blocked():
		m.status = rowLabel(row) + ": " + s.text + " first."
	default:
		return m.startRun([]string{row.ID})
	}
	return nil
}

func (m *Model) canSetUp() bool {
	switch {
	case m.opts.LoadTools == nil:
		m.status = "Setting up is not available here."
	case m.finish != nil:
		m.status = "Setting up already. q stops it."
	default:
		return true
	}
	return false
}

// startRun sets up ids in label order. What earlier runs found for them no
// longer holds.
func (m *Model) startRun(ids []string) tea.Cmd {
	run := &finishRun{}
	effective := m.live.Effective()
	for _, id := range ids {
		run.rows = append(run.rows, &finishRow{id: id, label: rowLabel(effective.Connections[id])})
		delete(m.loadErr, id)
		delete(m.failed, id)
		delete(m.outcomes, id)
		delete(m.loaded, id)
	}
	slices.SortStableFunc(run.rows, func(a, b *finishRow) int {
		return compareRows(effective.Connections[a.id], effective.Connections[b.id])
	})
	run.ctx, run.cancel = context.WithCancel(m.ctx)
	m.finish, m.status, m.spinFrame = run, "", 0
	return m.pump()
}

// pump starts waiting servers while fewer than finishLimit run (those just
// signed in first), starts the next sign-in when none runs, keeps the
// spinner going, and ends the run once every server settled. A server
// turned off, undone or removed meanwhile is dropped first, so nothing
// starts or signs in for it. Every written change pumps a run.
func (m *Model) pump() tea.Cmd {
	f := m.finish
	m.dropTurnedOff()
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
		next := f.queue[0]
		f.queue = f.queue[1:]
		cmds = append(cmds, m.signIn(next))
	}
	if f.settled() {
		m.endRun()
	}
	return tea.Batch(append(cmds, m.spin())...)
}

// dropTurnedOff stops the rows of the run whose server is no longer on.
func (m *Model) dropTurnedOff() {
	f := m.finish
	for _, r := range f.rows {
		if row, ok := m.live.Effective().Connections[r.id]; r.state >= finStarted || ok && row.Enabled {
			continue
		}
		if r.cancel != nil {
			r.cancel()
		}
		r.seq++
		r.state, r.cancel, r.url = finStopped, nil, ""
	}
	f.queue = slices.DeleteFunc(f.queue, func(r *finishRow) bool { return r.state == finStopped })
}

// startRow checks the server's stored sign-in, unless it just signed in,
// then starts it and loads its tools.
func (m *Model) startRow(r *finishRow) tea.Cmd {
	ctx, cancel := context.WithCancel(m.finish.ctx)
	r.seq++
	r.state, r.cancel = finStarting, cancel
	id, seq, check, load := r.id, r.seq, m.opts.StoredSignIn, m.opts.LoadTools
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
	if m.finish == nil || m.finish.row(r.id) != r || r.seq != msg.seq || r.state != finStarting {
		return nil
	}
	r.cancel, r.signIn = nil, msg.signIn
	if !r.signedIn {
		m.keepSignIn(r.id, msg.signIn)
	}
	var failure *output.Error
	if msg.err != nil {
		failure = m.failure(msg.err)
	}
	switch {
	case msg.signIn.Needed:
		m.needSignIn(r)
	case failure == nil:
		r.state = finStarted
		m.keepTools(r.id, msg.list)
		m.keepSignIn(r.id, SignInState{Possible: r.signIn.Possible})
	case !r.signedIn && (failure.Code == "auth_required" || failure.Code == "auth_expired"):
		// The server asked for a sign-in its stored state did not predict:
		// an unmarked OAuth server, or an expired session.
		m.needSignIn(r)
	default:
		r.state = finFailed
		m.keepFailure(r.id, msg.err)
	}
	return m.pump()
}

// keepFailure keeps why a server could not start for this session, as a
// failed tool load does: its Status, the pane and the tools page show it.
func (m *Model) keepFailure(id string, err error) {
	if m.loadErr == nil {
		m.loadErr = map[string][]string{}
	}
	if m.failed == nil {
		m.failed = map[string]string{}
	}
	m.loadErr[id] = m.connectFailure(id, err)
	m.failed[id] = shortReason(m.failure(err).Code, err)
}

// shortReason is the Status of a server that could not start.
func shortReason(code string, err error) string {
	switch {
	case code == "timeout" || errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case code == "canceled":
		return "stopped"
	case code == "auth_failed" || code == "auth_rate_limited" || code == "auth_account_conflict":
		return "1Password failed"
	case code == "keychain_unavailable":
		return "sign-in store locked"
	case code == "auth_callback_unavailable":
		return "sign-in port in use"
	case code == "auth_required" || code == "auth_expired":
		return "needs a sign-in"
	}
	return "could not start"
}

// needSignIn lines a server up for a sign-in, or, where setup cannot sign
// in (headless, or not an OAuth sign-in), points at mcparcel auth.
func (m *Model) needSignIn(r *finishRow) {
	if !r.signIn.Possible || m.opts.SignIn == nil {
		r.state = finManual
		m.keepOutcome(r.id, finManual)
		return
	}
	r.state = finQueued
	m.finish.queue = append(m.finish.queue, r)
}

func (m *Model) keepOutcome(id string, state finishState) {
	if m.outcomes == nil {
		m.outcomes = map[string]finishState{}
	}
	m.outcomes[id] = state
}

// signIn starts the next sign-in: Options.SignIn runs in its own goroutine,
// so its URL can reach the screen while it waits.
func (m *Model) signIn(r *finishRow) tea.Cmd {
	ctx, cancel := context.WithCancel(m.finish.ctx)
	r.seq++
	r.state, r.cancel, r.url = finSigning, cancel, ""
	row, id, seq, sign := r, r.id, r.seq, m.opts.SignIn
	urls := make(chan signInURLMsg, 1)
	done := make(chan error, 1)
	return func() tea.Msg {
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
	}
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
	if m.finish == nil || m.finish.row(r.id) != r || r.seq != msg.seq || r.state != finSigning {
		return nil
	}
	r.cancel, r.url = nil, ""
	if msg.err != nil {
		r.state = finFailed
		m.keepFailure(r.id, msg.err)
	} else {
		r.state, r.signedIn = finWaiting, true
		m.keepSignIn(r.id, SignInState{Possible: true})
	}
	return m.pump()
}

// skip leaves the running sign-in for later, canceling it.
func (m *Model) skip() tea.Cmd {
	r := m.finish.current()
	if r == nil {
		return nil
	}
	r.cancel()
	r.seq++
	r.state, r.cancel, r.url = finSkipped, nil, ""
	m.keepOutcome(r.id, finSkipped)
	m.status = r.label + " still needs a sign-in."
	return m.pump()
}

// stopRun is q while setting up: it cancels everything still running and
// settles each server. One in line for or in a sign-in still needs it;
// the rest are as they were before the run.
func (m *Model) stopRun() {
	f := m.finish
	if f == nil {
		return
	}
	f.cancel()
	for _, r := range f.rows {
		switch r.state {
		case finQueued, finSigning:
			r.state = finSkipped
			m.keepOutcome(r.id, finSkipped)
		case finWaiting, finStarting:
			r.state = finStopped
		}
		r.seq++
		r.cancel, r.url = nil, ""
	}
	f.queue = nil
	m.endRun()
	m.status = "Stopped. " + m.status
}

// endRun ends a settled run; the rows keep what it found.
func (m *Model) endRun() {
	f := m.finish
	ready := f.count(finStarted)
	switch {
	case len(f.rows) == 1 && ready == 1:
		m.status = f.rows[0].label + " is ready."
	case ready == len(f.rows):
		m.status = fmt.Sprintf("All %d are ready.", ready)
	default:
		m.status = fmt.Sprintf("%d of %d ready.", ready, len(f.rows))
	}
	m.finish = nil
}

// runKey handles the keys setting up takes over: Esc skips the sign-in
// that runs, q stops the run. It reports whether it used the key.
func (m *Model) runKey(k string) (tea.Cmd, bool) {
	switch {
	case m.finish == nil:
		return nil, false
	case k == "esc" && m.finish.current() != nil:
		return m.skip(), true
	case k == "q":
		m.stopRun()
		return nil, true
	}
	return nil, false
}
