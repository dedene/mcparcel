package ui

import (
	"path"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// A row's Status is one short phrase: what a server that is on needs next,
// or that it works. It comes from the state shown (the definition's
// metadata and the selection, never a credential), what setting up (s) and
// the tools page found this session, the stored sign-in
// (Options.StoredSignIn) and whether its command is found
// (Options.CommandAvailable): never the runtime. The list, the details pane,
// the header counts and the exit summary all read it through statusOf.

// statusKind orders what a row can need: the first that applies wins.
type statusKind int

const (
	stNone          statusKind = iota // off, nothing missing
	stReview                          // on, changed upstream
	stUnavailable                     // its source no longer defines it
	stInput                           // an input to fill in
	stProfile                         // a 1Password profile to link
	stCommand                         // its command is not installed: not found on PATH
	stFailed                          // it could not start this session
	stManual                          // needs a sign-in setup cannot do here
	stSignIn                          // needs a browser sign-in
	stSigning                         // setting up: in line for or in its sign-in
	stWorking                         // setting up: waiting for a slot or starting
	stChecking                        // its stored sign-in is being read
	stSignInUnknown                   // its stored sign-in cannot be read without a prompt
	stTools                           // its tools loaded this session
	stFirstUse                        // on, nothing known to be left
)

// status is a row's Status.
type status struct {
	kind   statusKind
	on     bool
	text   string // the list's phrase
	role   role
	detail string // the input or command it misses, display-safe
	spin   bool   // a spinner replaces its mark
}

// blocked reports whether the row waits on the user: s skips it.
func (s status) blocked() bool { return s.kind >= stReview && s.kind <= stCommand }

// step reports whether a server that is on has a step left.
func (s status) step() bool { return s.on && s.kind != stNone && s.kind <= stSigning }

// statusOf is the row's Status. A row that is off shows only what would
// keep it from running, dimmed.
func (m *Model) statusOf(row config.EffectiveConnection) status {
	if s, ok := m.blocking(row); ok {
		if !row.Enabled {
			s.role = roleDim
		}
		return s
	}
	if !row.Enabled {
		return status{}
	}
	s := status{on: true}
	if r := m.finish.row(row.ID); r != nil && r.state < finStarted {
		return m.runStatus(r)
	}
	auth := authName(m.live.Effective(), row.ID)
	signIn, known := m.storedSignIn(row)
	switch outcome := m.outcomes[row.ID]; {
	case len(m.loadErr[row.ID]) > 0:
		s.kind, s.text, s.role = stFailed, m.failed[row.ID], roleBad
		if s.text == "" {
			s.text = "could not start"
		}
	case outcome == finManual:
		s.kind, s.text, s.role = stManual, "run mcparcel auth "+auth, roleWarn
	case outcome == finSkipped || known && signIn.Needed:
		s.kind, s.text, s.role = stSignIn, "sign in", roleAccent
	case m.loaded[row.ID].tools != nil:
		s.kind, s.text, s.role = stTools, plural(len(m.loaded[row.ID].tools), "tool", "tools"), roleOK
	case m.signInPending[signInKey(row)]:
		s.kind, s.text, s.role = stChecking, "checking sign-in", roleDim
	case known && signIn.Unknown:
		s.kind, s.text, s.role = stSignInUnknown, "sign-in unknown", roleDim
	default:
		s.kind, s.text, s.role = stFirstUse, "starts on first use", roleDim
	}
	return s
}

// blocking is what keeps the row from running until the user acts: a
// review, an unavailable source, an input, a profile or its command.
func (m *Model) blocking(row config.EffectiveConnection) (status, bool) {
	state := m.live.State()
	s := status{on: row.Enabled}
	switch {
	case !row.Available:
		s.kind, s.text, s.role = stUnavailable, "unavailable", roleBad
		return s, true
	case row.Enabled && row.ReviewRequired:
		s.kind, s.text, s.role = stReview, "review change", roleWarn
		return s, true
	}
	inputs, _ := config.MissingConfig(state, row)
	switch missing := missingConfig(state, row); {
	case len(inputs) > 0:
		s.kind, s.detail, s.role = stInput, output.DisplayMetadata(inputs[0]), roleWarn
		s.text = "needs " + s.detail
		return s, true
	case slices.Contains(missing, profileNeed):
		s.kind, s.text, s.role = stProfile, "link 1Password", roleWarn
		return s, true
	}
	if command := m.missingCommand(row); command != "" {
		s.kind, s.detail, s.role = stCommand, command, roleBad
		s.text = command + " not installed"
		return s, true
	}
	return s, false
}

// runStatus is the phase of a row setting up runs.
func (m *Model) runStatus(r *finishRow) status {
	s := status{on: true, kind: stWorking, role: roleDim}
	switch r.state {
	case finWaiting:
		s.text = "waiting"
		if r.signedIn {
			s.text = "signed in" + m.theme.glyph.sep + "waiting"
		}
	case finStarting:
		s.text, s.role, s.spin = "starting...", roleInfo, true
		if r.signedIn {
			s.text = "signed in" + m.theme.glyph.sep + "loading tools..."
		}
	case finQueued:
		s.kind, s.text, s.role = stSigning, "next sign-in", roleAccent
	case finSigning:
		s.kind, s.text, s.role, s.spin = stSigning, "waiting for your browser...", roleAccent, true
	}
	return s
}

// mark is the glyph a status shows and the role that colors it: on with
// nothing to do, on with a step left, on but it cannot run, or off. A row
// setting up spins.
func (m *Model) mark(s status) (string, role) {
	g := m.theme.glyph
	frame := func(f string) string {
		if textWidth(g.on) == 3 { // ASCII marks are bracketed
			return "[" + f + "]"
		}
		return f
	}
	switch {
	case s.spin:
		return frame(m.finishSpinner()), s.role
	case s.kind == stWorking:
		return frame(m.theme.spinner.Frames[0]), roleDim // waits for a slot: static
	case !s.on:
		return g.off, roleDim
	case s.kind == stUnavailable || s.kind == stCommand || s.kind == stFailed:
		return g.unavailable, roleBad
	case s.step():
		return g.pending, roleWarn
	}
	return g.on, roleOK
}

// finishSpinner is a running row's mark: the spinner's frame, or its first
// frame, static, without motion.
func (m *Model) finishSpinner() string {
	frames := m.theme.spinner.Frames
	if !m.theme.motion {
		return frames[0]
	}
	return frames[m.spinFrame%len(frames)]
}

// missingCommand is the literal command a stdio row runs when
// Options.CommandAvailable does not find it, else "". Each command is
// looked up once a session, on the PATH the row runs with; setting up (s)
// looks again.
func (m *Model) missingCommand(row config.EffectiveConnection) string {
	command, search, ok := commandLookup(row)
	if m.opts.CommandAvailable == nil || !ok {
		return ""
	}
	key := commandKey(command, search)
	found, seen := m.commands[key]
	if !seen {
		found = m.opts.CommandAvailable(command, search)
		if m.commands == nil {
			m.commands = map[string]bool{}
		}
		m.commands[key] = found
	}
	if found {
		return ""
	}
	return output.DisplayMetadata(path.Base(command))
}

// commandLookup is the literal command a stdio row runs and the PATH the
// runtime looks for it on: the connection's own PATH when it sets one, else
// "" for the PATH setup runs with. ok is false when setup cannot check it:
// no literal command, or a PATH that comes from an input.
func commandLookup(row config.EffectiveConnection) (command, search string, ok bool) {
	c := row.Connection
	if c == nil {
		c = row.Definition
	}
	if c == nil || c.Transport.Stdio == nil || c.Transport.Stdio.Command.Literal == nil {
		return "", "", false
	}
	if v, set := c.Transport.Stdio.Env["PATH"]; set {
		if v.Literal == nil {
			return "", "", false
		}
		search = *v.Literal
	}
	return *c.Transport.Stdio.Command.Literal, search, true
}

// commandKey is what m.commands keeps a lookup under.
func commandKey(command, search string) string { return command + "\x00" + search }

// lookAgain forgets the row's command lookup, so it is looked up again.
func (m *Model) lookAgain(row config.EffectiveConnection) {
	if command, search, ok := commandLookup(row); ok {
		delete(m.commands, commandKey(command, search))
	}
}

// signInKey identifies a stored sign-in: the connection and the URL it
// signs in to, so a changed URL is read again.
func signInKey(row config.EffectiveConnection) string {
	c := row.Connection
	if c == nil {
		c = row.Definition
	}
	key := row.ID
	if c != nil && c.Transport.HTTP != nil && c.Transport.HTTP.URL.Literal != nil {
		key += " " + *c.Transport.HTTP.URL.Literal
	}
	return key
}

// readsSignIn reports whether the row's Status depends on its stored
// sign-in: it is on, not blocked and signs in through a browser.
func (m *Model) readsSignIn(row config.EffectiveConnection) bool {
	c := row.Connection
	if m.opts.StoredSignIn == nil || !row.Enabled || c == nil || !signIn(c) {
		return false
	}
	_, blocked := m.blocking(row)
	return !blocked
}

// storedSignIn is what the session read of the row's stored sign-in.
func (m *Model) storedSignIn(row config.EffectiveConnection) (SignInState, bool) {
	if !m.readsSignIn(row) {
		return SignInState{}, false
	}
	s, ok := m.signIns[signInKey(row)]
	return s, ok
}

// keepSignIn keeps what a sign-in or a start found for the row.
func (m *Model) keepSignIn(id string, s SignInState) {
	row, ok := m.live.Effective().Connections[id]
	if !ok || !m.readsSignIn(row) {
		return
	}
	if m.signIns == nil {
		m.signIns = map[string]SignInState{}
	}
	m.signIns[signInKey(row)] = s
	delete(m.signInPending, signInKey(row)) // a read still running is older
}

type signInsMsg struct{ states map[string]SignInState }

// readSignIns reads, in one command, the stored sign-in of every row that
// is on and whose Status depends on it, unless the session read it or a
// read runs. It reads local state only and never prompts, so it runs on
// open and whenever a server turns on.
func (m *Model) readSignIns() tea.Cmd {
	var rows []config.EffectiveConnection
	for _, row := range m.live.Effective().Connections {
		key := signInKey(row)
		if _, ok := m.signIns[key]; !ok && !m.signInPending[key] && m.readsSignIn(row) {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	if m.signInPending == nil {
		m.signInPending = map[string]bool{}
	}
	for _, row := range rows {
		m.signInPending[signInKey(row)] = true
	}
	ctx, read := m.ctx, m.opts.StoredSignIn
	return func() tea.Msg {
		states := map[string]SignInState{}
		for _, row := range rows {
			states[signInKey(row)] = read(ctx, row.ID)
		}
		return signInsMsg{states}
	}
}

func (m *Model) signInsRead(msg signInsMsg) {
	if m.signIns == nil {
		m.signIns = map[string]SignInState{}
	}
	for key, s := range msg.states {
		if m.signInPending[key] { // else a sign-in since settled it
			delete(m.signInPending, key)
			m.signIns[key] = s
		}
	}
	if m.landing != nil && m.screen == screenList {
		m.land(m.landing)
	}
	if m.landStep && m.screen == screenList {
		m.landOnStep()
	}
}

// statusText is a status in a column of n cells that ends in the gutter,
// colored when styled.
func (m *Model) statusText(s status, n int, styled bool) string {
	text := fit(s.text, n-gutter)
	width := textWidth(text)
	if styled {
		text = m.theme.paint(s.role, text)
	}
	return text + strings.Repeat(" ", max(n-width, 0))
}

// steps lists, by label, the servers that are on with a step left, and
// counts those that are on.
func (m *Model) steps() (on int, steps []Step) {
	var rows []config.EffectiveConnection
	for _, row := range m.live.Effective().Connections {
		if row.Enabled {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, compareRows)
	for _, row := range rows {
		on++
		s := m.statusOf(row)
		if !s.step() {
			continue
		}
		step := Step{ID: row.ID, Label: rowLabel(row), Detail: s.detail}
		switch s.kind {
		case stReview:
			step.Need = NeedReview
		case stUnavailable:
			step.Need = NeedUnavailable
		case stInput:
			step.Need = NeedInput
		case stProfile:
			step.Need = NeedProfile
		case stCommand:
			step.Need = NeedCommand
		case stFailed:
			step.Need = NeedFailed
		default:
			step.Need = NeedSignIn
			if s.kind == stManual || m.opts.SignIn == nil {
				step.Command = "mcparcel auth " + authName(m.live.Effective(), row.ID)
			}
		}
		steps = append(steps, step)
	}
	return on, steps
}

// setUpTakes reports whether s on the list takes a step of a server that
// is on: a sign-in setup can do here, or a start that failed.
func (m *Model) setUpTakes() bool {
	if m.opts.LoadTools == nil {
		return false
	}
	for _, row := range m.live.Effective().Connections {
		switch st := m.statusOf(row); {
		case !st.on:
		case st.kind == stFailed, st.kind == stSignIn && m.opts.SignIn != nil:
			return true
		}
	}
	return false
}

// readAllSignIns reads, right away, the stored sign-ins the session has
// not read yet, so the exit summary is complete. A read still running is
// read again: setup ends before it returns.
func (m *Model) readAllSignIns() {
	clear(m.signInPending)
	if cmd := m.readSignIns(); cmd != nil {
		if msg, ok := cmd().(signInsMsg); ok {
			m.signInsRead(msg)
		}
	}
}
