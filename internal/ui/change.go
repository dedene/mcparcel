package ui

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
)

// Writing changes and undoing them. Store access runs inside the returned
// commands; the messages they return update the model. Keys pressed and
// text pasted while a change is written wait for it, then run in order.

type writtenMsg struct{ writeResult }

// elsewhereError is a change that no longer applies once setup picked up
// what changed elsewhere.
type elsewhereError struct{ err error }

func (e elsewhereError) Error() string { return "changed elsewhere: " + e.err.Error() }
func (e elsewhereError) Unwrap() error { return e.err }

// apply tries c on the state shown, then writes it. then runs with the
// outcome once the write returns, or at once when the trial fails, so a
// change that cannot apply never reaches the store.
func (m *Model) apply(c change, then func(error) tea.Cmd) tea.Cmd {
	if then == nil {
		then = func(error) tea.Cmd { return nil }
	}
	if err := m.live.trial(c); err != nil {
		return then(err)
	}
	m.busy, m.writing, m.then = true, &c, then
	ctx, store, revision := m.ctx, m.store, m.live.State().Selections.Revision
	return func() tea.Msg { return writtenMsg{writeChange(ctx, store, revision, c)} }
}

func (m *Model) written(msg writtenMsg) tea.Cmd {
	c, then := m.writing, m.then
	m.busy, m.writing, m.then = false, nil, nil
	err := msg.err
	if msg.fresh {
		if setErr := m.live.set(msg.state); setErr != nil && err == nil {
			err = setErr
		}
		m.refreshSources()
		m.clamp()
	}
	// A write that failed part way may have landed; with no fresh read the
	// exit summary says so.
	m.unconfirmed = errors.Is(msg.err, config.ErrConfigWrite) && !msg.fresh
	if err == nil && c != nil {
		m.live.record(*c, msg.before, msg.state)
	}
	if m.interrupted {
		return m.quit()
	}
	if err != nil && msg.moved {
		err = elsewhereError{err}
	}
	var cmds []tea.Cmd
	if then != nil {
		cmds = append(cmds, then(err))
	}
	if m.finish != nil {
		cmds = append(cmds, m.pump())
	}
	for len(m.queued) > 0 && !m.busy {
		next := m.queued[0]
		m.queued = m.queued[1:]
		switch next := next.(type) {
		case tea.KeyPressMsg:
			cmds = append(cmds, m.key(next))
		case tea.PasteMsg:
			m.paste(next.Content)
		}
	}
	return tea.Batch(cmds...)
}

// undo reverts the last change still kept, as a new change.
func (m *Model) undo() tea.Cmd {
	step := m.live.last()
	if step == nil {
		m.status = "Nothing to undo."
		return nil
	}
	return m.apply(change{ops: []op{{kind: opUndo, undo: step}}, accept: step.accept()}, func(err error) tea.Cmd {
		switch {
		case err == nil:
			m.live.drop(step)
			m.status = step.done
			if m.screen == screenList && len(step.ids) == 1 {
				m.flash(step.ids[0])
			}
			if _, ok := m.live.Effective().Connections[m.detailID]; !ok && (m.screen == screenDetails || m.screen == screenTools) {
				m.screen = screenList // the undo removed it
			}
		case errors.Is(err, errChangedElsewhere):
			m.live.drop(step)
			m.status = "That changed elsewhere since, so it cannot be undone."
		default:
			m.status = m.inline(err)
		}
		return nil
	})
}

func (m *Model) refreshSources() {
	if m.opts.SourceAges == nil {
		return
	}
	if sources, err := m.opts.SourceAges(m.live.State()); err == nil {
		m.sources = sources
	}
}
