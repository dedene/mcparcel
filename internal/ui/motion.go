package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// Motion follows the setup redesign's rules: nothing moves while the screen
// is idle, and Init schedules nothing. Each animation is one tick through
// Model.tick, identified by a sequence number so a stale tick does nothing.
// Without motion (no color, or MCPARCEL_REDUCE_MOTION) a row never flashes
// and the spinner is a static "Loading...", but toasts still clear.

const (
	toastLife = 3 * time.Second        // a status message shows this long
	toastFade = 300 * time.Millisecond // then dims this long before it clears (motion only)
	flashLife = 180 * time.Millisecond // a toggled row's highlight
)

type (
	toastMsg struct{ seq int }
	flashMsg struct{ seq int }
	spinMsg  struct{}
)

// motion is the animation state the model keeps.
type motion struct {
	toastSeq    int  // identifies the current status; a new one restarts its clock
	toastFading bool // the status is dimmed, about to clear
	flashID     string
	flashSeq    int
	spinFrame   int
	spinning    bool // a spinner tick is pending
}

// animate schedules the ticks an update calls for: a new status message
// clears after toastLife, and a new flash settles after flashLife. status and
// flash are their values before the update. A busy status (Saving...) stays
// until its result replaces it, and so does the prompt of a pending enable
// (what the field or profile screen still needs).
func (m *Model) animate(status string, flash int) tea.Cmd {
	var cmds []tea.Cmd
	if m.status != status {
		m.toastSeq++
		m.toastFading = false
		if m.status != "" && !m.busy && !m.pending() {
			seq := m.toastSeq
			cmds = append(cmds, m.tick(toastLife, func(time.Time) tea.Msg { return toastMsg{seq} }))
		}
	}
	if m.flashSeq != flash && m.flashID != "" {
		seq := m.flashSeq
		cmds = append(cmds, m.tick(flashLife, func(time.Time) tea.Msg { return flashMsg{seq} }))
	}
	return tea.Batch(cmds...)
}

// toastExpired dims the status, then clears it; without motion it clears at
// once.
func (m *Model) toastExpired(msg toastMsg) tea.Cmd {
	if msg.seq != m.toastSeq {
		return nil
	}
	if m.theme.motion && !m.toastFading {
		m.toastFading = true
		return m.tick(toastFade, func(time.Time) tea.Msg { return msg })
	}
	m.status = ""
	return nil
}

// flash highlights a row the user just turned on or off.
func (m *Model) flash(id string) {
	if !m.theme.motion {
		return
	}
	m.flashID = id
	m.flashSeq++
}

func (m *Model) flashEnded(msg flashMsg) {
	if msg.seq == m.flashSeq {
		m.flashID = ""
	}
}

// spin keeps one spinner tick pending while work runs: a tool load or the
// finish screen. The chain ends at the first tick after the work does.
func (m *Model) spin() tea.Cmd {
	if !m.theme.motion || !m.working() || m.spinning {
		return nil
	}
	m.spinning = true
	return m.tick(m.theme.spinner.FPS, func(time.Time) tea.Msg { return spinMsg{} })
}

func (m *Model) spun() tea.Cmd {
	m.spinning = false
	if !m.working() {
		return nil
	}
	m.spinFrame++
	return m.spin()
}

// working reports whether anything the spinner shows is running.
func (m *Model) working() bool { return m.loading != "" || m.finish.working() }

// spinner is the loading mark: a frame of the theme's spinner, or a static
// "Loading..." without motion.
func (m *Model) spinner() string {
	if !m.theme.motion {
		return "Loading..."
	}
	frames := m.theme.spinner.Frames
	return frames[m.spinFrame%len(frames)]
}
