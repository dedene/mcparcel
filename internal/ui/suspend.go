package ui

import (
	"runtime"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
)

// Ctrl+Z suspends setup to the shell, as in other terminal programs. The
// terminal is in raw mode, so the key arrives as a key press on any screen,
// never as text, and setup asks Bubble Tea to suspend (tea.Suspend). Back in
// the foreground (fg), Bubble Tea restores the terminal and sends
// tea.ResumeMsg: setup clears the screen, reads the store again, since
// another terminal may have changed it meanwhile, and reads again the
// stored sign-ins and local commands it read this session.
//
// A Ctrl+Z pressed while a change is written waits for it, as every key
// does, so setup never stops with a write half way and the store locked.
// Keys read after any Ctrl+Z (typed ahead, pasted, repeated) wait in turn
// until setup resumes: Bubble Tea stops the process only once it handles
// tea.Suspend, and one of them may start a write, which must not run then.
//
// mcparcel builds only for macOS and Linux; the no-job-control branch is
// defensive, for a platform without SIGTSTP.

// canSuspend reports whether the platform can suspend a process; tests set
// it to see both.
var canSuspend = runtime.GOOS != "windows"

// reloadedMsg is the store as read on resume.
type reloadedMsg struct {
	state config.State
	err   error
}

// suspend handles Ctrl+Z when no change is being written.
func (m *Model) suspend() tea.Cmd {
	m.landing, m.landStep = nil, false
	if !canSuspend {
		m.status = "Ctrl+Z cannot suspend setup here; q quits."
		return nil
	}
	m.suspending = true // keys read before Bubble Tea stops the process wait for tea.ResumeMsg
	return tea.Suspend
}

// resumed forgets the stored sign-ins and commands the session read, so
// each is read again once the store is, and drops the sign-in reads still
// running, which started before the suspend. It then reads the store,
// clears the screen, so setup draws in full, and replays the keys that
// waited behind a Ctrl+Z.
func (m *Model) resumed() tea.Cmd {
	m.suspending = false
	clear(m.commands)
	clear(m.signIns)
	clear(m.signInPending)
	cmds := []tea.Cmd{tea.ClearScreen}
	if m.store != nil {
		ctx, store := m.ctx, m.store
		cmds = append(cmds, func() tea.Msg {
			state, err := store.Read(ctx)
			return reloadedMsg{state, err}
		})
	}
	return tea.Batch(append(cmds, m.replay()...)...)
}

// reloaded shows the state read on resume, as setup shows a fresh read
// after a write. A change written since brought a state as fresh, and one
// being written brings its own, so a read older than either is dropped.
// A server page whose connection is gone returns to the list.
func (m *Model) reloaded(msg reloadedMsg) {
	if m.busy || msg.err == nil && msg.state.Selections.Revision < m.live.State().Selections.Revision {
		return
	}
	err := msg.err
	if err == nil {
		was, had := m.live.Effective().Connections[m.detailID]
		if err = m.live.set(msg.state); err == nil {
			m.refreshSources()
			m.clamp()
			m.leaveRemoved(was, had)
			return
		}
	}
	m.status = "Could not read the configuration again: " + m.inline(err)
}

// leaveRemoved returns to the list from a screen of the connection was,
// once it is gone, ending an enable or a tool load it had started; the
// edit form of a personal connection closes too, but a form adding a new
// one stays. Help opened on such a screen closes onto the list.
func (m *Model) leaveRemoved(was config.EffectiveConnection, had bool) {
	if _, ok := m.live.Effective().Connections[m.detailID]; ok || !had {
		return
	}
	at := &m.screen
	if m.screen == screenHelp {
		at = &m.back
	}
	switch {
	case *at == screenDetails, *at == screenTools, *at == screenField, *at == screenProfile:
	case *at == screenForm && m.form != nil && m.form.edit == m.detailID:
		m.form = nil
	default:
		return
	}
	if m.loading == m.detailID {
		m.cancelLoad()
	}
	m.pendingEnable, m.enableOps, m.editing = "", nil, false
	*at = screenList
	m.clamp()
	m.status = rowLabel(was) + " is gone: it changed elsewhere."
}
