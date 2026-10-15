package ui

import (
	"fmt"
	"strings"

	"github.com/dedene/mcparcel/internal/output"
)

// The finish screen's look: a header with the ready count, the sign-in URL
// while one waits for the browser, then one line per connection that never
// moves: its mark (a spinner only while work runs), its label and what it
// is doing or how it ended. Up/Down scroll the whole page when it does not
// fit, the URL included, so no part of it is out of reach.

// finishDetailWidth is the room a line keeps for its state, such as
// "waiting for browser sign-in...", before a long label is cut.
const finishDetailWidth = 32

func (m *Model) finishPage() []string {
	f := m.finish
	if !f.started {
		return m.offerPage()
	}
	w, _ := m.size()
	var body []string
	if r := f.current(); r != nil && r.url != "" {
		label := fit(r.label, max(w/3, 12)) // the URL matters more
		text := "To sign in to " + label + ", open this URL in a browser:"
		if r.browser {
			text = "Opening your browser to sign in to " + label + ". If it does not open, visit:"
		}
		body = append(append(wrap(text, w), wrap(r.url, w)...), "")
	}
	nameWidth := m.finishNameWidth(w)
	focus := -1
	for _, r := range f.rows {
		if r.state == finAsk {
			focus = len(body)
		}
		body = append(body, m.finishLine(r, nameWidth, w, r.state != finAsk))
	}
	_, room := m.pageStatus()
	scrolls := focus < 0 && len(body) > room // no row asks for the user
	if scrolls {
		f.offset = min(max(f.offset, 0), len(body)-room)
		body = body[f.offset:]
	}
	lines := m.page("", body, focus, m.finishKeys(w, scrolls))
	lines[0] = m.finishHeader(w)
	return lines
}

// finishHeader shows MCParcel and Finishing setup on the left, and how many
// connections are ready on the right.
func (m *Model) finishHeader(w int) string {
	t, f := m.theme, m.finish
	ready := f.count(finStarted)
	r := roleDim
	if ready > 0 {
		r = roleOK
	}
	right := t.paint(r, fmt.Sprintf("%d of %d ready", ready, len(f.rows)))
	return spread(t.paint(roleTitle, "MCParcel")+"  "+t.paint(roleDim, "Finishing setup"), right, w)
}

func (m *Model) finishNameWidth(w int) int {
	n := 8
	for _, r := range m.finish.rows {
		n = max(n, textWidth(r.label)+gutter)
	}
	return min(n, max(w-2-textWidth(m.theme.glyph.on)-1-finishDetailWidth, w/3, 10))
}

// finishLine is a connection's line: "> " on the one asked for, the mark,
// the label and its state. styled is false on the focus line, which the page
// paints whole.
func (m *Model) finishLine(r *finishRow, nameWidth, w int, styled bool) string {
	t, g := m.theme, m.theme.glyph
	paint := func(ro role, text string) string {
		if !styled {
			return text
		}
		return t.paint(ro, text)
	}
	mark, markRole := g.pending, roleWarn
	detail, detailRole := "", roleDim
	tools := g.sep + plural(r.tools, "tool", "tools")
	switch r.state {
	case finWaiting: // the spinner's first frame, static: nothing runs yet
		mark, markRole, detail = m.theme.spinner.Frames[0], roleDim, "waiting"
		if r.signedIn {
			detail = "signed in" + g.sep + "waiting"
		}
	case finStarting:
		mark, markRole, detail = m.finishSpinner(), roleInfo, "starting..."
		if r.signedIn {
			detail = "signed in" + g.sep + "loading tools..."
		}
	case finQueued:
		detail = "queued for sign-in"
	case finAsk:
		detail = "needs sign-in"
	case finSigning:
		mark, markRole, detail, detailRole = m.finishSpinner(), roleAccent, "waiting for browser sign-in...", roleAccent
	case finStarted:
		mark, markRole, detail = g.done, roleOK, "started"+tools
		if r.signedIn {
			detail = paint(roleAccent, "signed in") + paint(roleDim, tools)
		}
	case finFailed:
		mark, markRole, detail, detailRole = g.unavailable, roleBad, r.reason, roleBad
	case finSkipped:
		detail = "skipped"
	case finManual:
		detail, detailRole = "run mcparcel auth "+output.DisplayMetadata(output.AuthTarget(r.id, r.id)), roleWarn
	case finStopped:
		mark, markRole, detail = g.off, roleDim, "stopped"
	}
	prefix := "  "
	if r.state == finAsk {
		prefix = "> "
	}
	lead := prefix + paint(markRole, pad(mark, textWidth(g.on))) + " " + cell(r.label, nameWidth)
	return fit(lead+paint(detailRole, fit(detail, max(w-textWidth(lead), 0))), w)
}

// finishSpinner is a running connection's mark: the spinner's frame, or its
// first frame, static, without motion.
func (m *Model) finishSpinner() string {
	frames := m.theme.spinner.Frames
	if !m.theme.motion {
		return frames[0]
	}
	return frames[m.spinFrame%len(frames)]
}

// finishKeys is the help line: the sign-in asked for, else how to leave,
// with Up/Down while the page scrolls.
func (m *Model) finishKeys(w int, scrolls bool) string {
	f := m.finish
	r := f.current()
	scroll := ""
	if scrolls {
		scroll = "Up/Down: scroll   "
	}
	switch {
	case r != nil && r.state == finAsk:
		help := "Enter: sign in to " + r.label + "   s: skip   Esc: stop"
		if textWidth(strings.ReplaceAll(help, ": ", " ")) > w {
			help = "Enter: sign in   s: skip   Esc: stop"
		}
		return m.keys(help, w)
	case r != nil:
		help := scroll + "s: skip sign-in   Esc: stop"
		if textWidth(strings.ReplaceAll(help, ": ", " ")) > w {
			help = scroll + "s: skip   Esc: stop"
		}
		return m.keys(help, w)
	case !f.settled():
		return m.keys(scroll+"Esc: stop", w)
	case f.count(finFailed) > 0:
		return m.keys(scroll+"r: retry failed   Enter: done", w)
	}
	return m.keys(scroll+"Enter: done", w)
}

// offerPage asks before the finish step contacts anything, and lists what
// each connection involves (its Needs).
func (m *Model) offerPage() []string {
	w, _ := m.size()
	f := m.finish
	body := []string{
		"Start " + plural(len(f.rows), "server", "servers") + " now?",
		"Setup starts each one, signs in where needed and loads its tools. 1Password or a browser may ask you to approve.",
		"",
	}
	nameWidth := m.finishNameWidth(w)
	effective, state := m.draft.Effective(), m.draft.State()
	for _, r := range f.rows {
		var needs []string
		for _, n := range lookOf(state, effective.Connections[r.id]).listNeeds() {
			needs = append(needs, n.text)
		}
		line := "  " + cell(r.label, nameWidth) + m.theme.paint(roleDim, strings.Join(needs, m.theme.glyph.sep))
		body = append(body, fit(line, w))
	}
	return m.page("MCParcel / Setup / Finish", body, -1, "Enter: start   Esc: later")
}
