package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// Wide list layout: header, tabs, search, then the column header and the
// list on the left with the details pane beside them, then a blank line,
// the bottom lines (one key line; two when a server needs a step or while
// searching; more while a sign-in shows its URL) and the status.
const listChrome = 6

// Wide list columns, after the focus prefix and the glyph: MCP, Status and,
// with two or more sources, Source. Each column ends in the gutter. Where a
// row runs is in the details pane.
const (
	colMinName   = 14
	colMinStatus = 21
	colMaxName   = 28 // MCP gives way to Status past this, when Status needs it
	colMinSource = 12
	colMaxSource = 30
)

func (m *Model) size() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 80, 24 // until the first WindowSizeMsg
	}
	return m.width, m.height
}

func (m *Model) listHeight() int {
	w, h := m.size()
	if m.compact() {
		return max(h-compactChrome-len(m.bottomLines(w)), 1)
	}
	return max(h-listChrome-len(m.bottomLines(w)), 1)
}

func (m *Model) wideList() []string {
	w, _ := m.size()
	lw := listWidth(w)
	left := append([]string{m.theme.paint(roleDim, m.columnHeader(lw))}, m.listBody(lw, m.wideRow(lw))...)
	pane := m.pane(w-lw-3, len(left))
	rule := m.theme.paint(roleFaint, m.theme.border.Left)
	lines := []string{m.header(w), m.scrollTabs(w), m.searchLine(w)}
	for i, line := range left {
		lines = append(lines, pad(line, lw)+" "+rule+" "+pane[i])
	}
	lines = append(lines, "")
	return append(append(lines, m.bottomLines(w)...), m.statusLine(w))
}

// bottomLines are the lines under the list, above the status. While a
// sign-in waits they say where to sign in, with its URL, and how to skip
// it; while setting up runs, how to stop it. Otherwise, when a server that
// is on has a step left, the first line says so and offers s when s takes
// a step, and Right when the cursor is on one, else names the next step;
// the other is the key hints. With no step left the wide list has one key
// line (the arrow keys are only in ? help); the compact list always has one.
func (m *Model) bottomLines(w int) []string {
	_, h := m.size()
	compact := m.compact()
	switch {
	case m.screen == screenSearch && compact:
		return []string{m.keys(searchHelp, w)}
	case m.screen == screenSearch:
		return []string{m.keys(searchHelp, w), fit("Typing filters label, ID and description in every domain.", w)}
	case m.finish != nil:
		// The sign-in leaves the list a row at least.
		room := max(h-listChrome-1, 2)
		if compact {
			room = max(h-compactChrome-1, 2)
		}
		text := wrap(m.signInText(), w)
		if m.signInText() == "" {
			text = []string{fit(m.runText(), w)}
		}
		if len(text) > room-1 {
			text = append(text[:room-2], fit(strings.Join(text[room-2:], " "), w))
		}
		return append(text, m.keys(m.runHelp(), w))
	}
	_, steps := m.steps()
	if len(steps) > 0 {
		count := plural(len(steps), "server needs a step.", "servers need a step.") + "  "
		if compact {
			count = plural(len(steps), "needs a step.", "need a step.") + " "
		}
		// s only when it takes a step; Right only on a server with one.
		// With neither, the line names the next step.
		lead := count
		var hints []string
		if m.setUpTakes() {
			hints = append(hints, "s: set up all")
		}
		if row, ok := m.selected(); ok && m.statusOf(row).step() {
			hints = append(hints, m.theme.glyph.right+": open "+rowLabel(row))
		}
		if len(hints) == 0 {
			next := steps[0]
			lead += "Next: " + next.Label + ", " + m.statusOf(m.live.Effective().Connections[next.ID]).text + ".  "
		}
		if compact {
			hints = append(hints, "?: help")
		}
		// The keys get the room after the count; the next step the rest.
		hint := ""
		if len(hints) > 0 {
			hint = m.keys(strings.Join(hints, "   "), max(w-textWidth(count), 1))
		}
		text := strings.TrimRight(lead, " ")
		gap := strings.Repeat(" ", len(lead)-len(text))
		first := fit(text, w)
		if hint != "" {
			first = fit(text, max(w-textWidth(hint)-len(gap), 0)) + gap + hint
		}
		if compact {
			return []string{fit(first, w)}
		}
		return []string{fit(first, w), m.keys(stepKeys, w)}
	}
	return []string{m.keys(mainKeys, w)}
}

// mainKeys are the list's key hints, in one order at every width: keys()
// drops what does not fit, keeping "? help" last. With a step left the wide
// list's first line offers s and Enter, so its key line, stepKeys, leaves
// them out.
const (
	mainKeys = "Space: on/off   Tab: domain   Enter: open   s: set up   u: undo   /: search   n: new   q: quit   ?: help"
	stepKeys = "Space: on/off   Tab: domain   u: undo   /: search   q: quit   ?: help"
)

// signInText says where the running sign-in waits, with its URL once the
// sign-in passed it; "" when none runs.
func (m *Model) signInText() string {
	r := m.finish.current()
	switch {
	case r == nil:
		return ""
	case r.url == "":
		return "Sign in to " + r.label + " in your browser."
	case r.browser:
		return "Sign in to " + r.label + " in your browser. Didn't open? " + r.url
	}
	return "Open this to sign in to " + r.label + ": " + r.url
}

// runText is the line under the list while setting up runs without a
// sign-in: how far it got.
func (m *Model) runText() string {
	f := m.finish
	return fmt.Sprintf("Setting up: %d of %d ready.", f.count(finStarted), len(f.rows))
}

// runHelp is the key line while setting up runs.
func (m *Model) runHelp() string {
	if m.finish.current() != nil {
		return "Esc: skip this one   q: stop"
	}
	return "Space: on/off   Tab: domain   Right/Enter: open   q: stop"
}

// searchHelp replaces the list keys while typing a search: Space and letters
// type into the query.
const searchHelp = "Enter/Down: list   Backspace/Ctrl+U: edit   Esc: clear"

func (m *Model) searchLine(w int) string {
	switch {
	case m.screen == screenSearch:
		return fit("Search: "+m.search.render(w-8), w)
	case m.search.String() != "":
		return fit("Search: "+output.DisplayMetadata(m.search.String())+"   (Esc clears)", w)
	}
	return ""
}

// showSource reports whether the rows come from two or more sources (a
// catalog or Personal). With a single source the Source column is hidden and
// MCP and Status share its width.
func (m *Model) showSource() bool {
	seen := map[string]bool{}
	for _, row := range m.live.Effective().Connections {
		seen[row.SourceID] = true
	}
	return len(seen) > 1
}

// wideColumns are the list's column widths for a list lw cells wide.
type wideColumns struct{ lead, name, status, source int }

func (m *Model) wideColumns(lw int) wideColumns {
	c := wideColumns{lead: 2 + textWidth(m.theme.glyph.on) + 1}
	if m.showSource() {
		c.source = min(max(lw/4, colMinSource), colMaxSource)
	}
	rest := max(lw-c.lead-c.source, 2*colMinName)
	// MCP as wide as the longest label when Status keeps room for its
	// longest text (at least "starts on first use"); when both cannot fit,
	// MCP keeps colMaxName and Status cuts the command or input it names.
	// Both measure every server, so the columns stay put across domains.
	need := colMinStatus
	for _, row := range m.live.Effective().Connections {
		need = max(need, textWidth(m.statusOf(row).text)+gutter)
	}
	label := max(m.labelWidth(), colMinName)
	c.name = max(min(label, max(rest-need, min(label, colMaxName)), rest-colMinStatus), colMinName)

	c.status = rest - c.name
	return c
}

func (m *Model) columnHeader(lw int) string {
	c := m.wideColumns(lw)
	line := strings.Repeat(" ", c.lead) + cell("MCP", c.name)
	if c.source > 0 {
		return fit(line+cell("Status", c.status)+"Source", lw)
	}
	return fit(line+"Status", lw)
}

// wideRow renders a row in the wide columns. Styled, each part is painted;
// plain, the caller paints the whole line (focus or flash), so no inner
// reset ends that style early.
func (m *Model) wideRow(lw int) rowFormat {
	c := m.wideColumns(lw)
	state := m.live.State()
	return func(row config.EffectiveConnection, prefix string, styled bool) string {
		st := m.statusOf(row)
		line := prefix + m.markText(st, styled) + " " + cell(rowLabel(row), c.name) + m.statusText(st, c.status, styled)
		if c.source > 0 {
			return fit(line+sourceName(state, row), lw)
		}
		return fit(line, lw)
	}
}

// markText is the row's glyph, colored when styled, padded to the width of
// the on glyph (a spinner frame may be narrower).
func (m *Model) markText(st status, styled bool) string {
	glyph, r := m.mark(st)
	glyph = pad(glyph, textWidth(m.theme.glyph.on))
	if !styled {
		return glyph
	}
	return m.theme.paint(r, glyph)
}

// rowFormat renders one list row after its focus prefix; styled is false
// when the caller paints the whole line.
type rowFormat func(row config.EffectiveConnection, prefix string, styled bool) string

// listBody renders the visible rows with format, or the empty state, padded
// to the list height. The focused row is reverse video; a row just toggled
// flashes first (see motion.go).
func (m *Model) listBody(w int, format rowFormat) []string {
	rows, page := m.rows(), m.listHeight()
	lines := make([]string, 0, page)
	if len(rows) == 0 {
		name := m.currentTab().label
		state := m.live.State()
		switch q := m.search.String(); {
		case q != "":
			lines = append(lines, fit(fmt.Sprintf("  No MCPs match %s in %s. Esc clears the search.", quoted(q), name), w))
		case len(state.Local.Sources) == 0 && len(state.Personal.Connections) == 0:
			lines = append(lines, fit("  No catalogs yet. Add one from a terminal: mcparcel add <owner/repo>.", w), fit("  Press n to add a personal connection.", w))
		default:
			lines = append(lines, fit("  No MCPs in "+name+".", w))
		}
	}
	for i := m.offset; i < len(rows) && i < m.offset+page; i++ {
		row := rows[i]
		prefix := "  "
		focused := i == m.cursor
		if focused {
			prefix = "> "
		}
		flash := m.flashID != "" && row.ID == m.flashID
		line := format(row, prefix, !focused && !flash)
		switch {
		case flash && row.Enabled:
			line = m.theme.paint(roleFlashOn, pad(line, w))
		case flash:
			line = m.theme.paint(roleFlashOff, pad(line, w))
		case focused:
			line = m.theme.paint(roleSelected, pad(line, w))
		}
		lines = append(lines, line)
	}
	for len(lines) < page {
		lines = append(lines, "")
	}
	return lines
}

// quoted shows text as an ASCII Go string literal, like DisplayMetadata
// does for text it has to escape.
func quoted(text string) string { return strconv.QuoteToASCII(text) }

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func (m *Model) stateText(row config.EffectiveConnection) string {
	state := m.live.State()
	switch m.statusOf(row).kind {
	case stUnavailable:
		return unavailableText(state, row)
	case stReview:
		return "Review required. Space accepts it (same as mcparcel enable " + output.DisplayMetadata(row.ID) + ")."
	case stInput, stProfile:
		return "Needs " + needsText(missingConfig(state, row)) + "."
	}
	if profile := autoLinks(state, row); profile != "" {
		return "Space turns it on and links 1Password profile " + output.DisplayMetadata(profile) + "."
	}
	if text := m.sessionText(row); text != "" {
		return text
	}
	if row.Enabled {
		return "On. It starts on first use; s sets it up now."
	}
	return "Off. Space turns it on."
}

// sessionText is what the Status of a row knows beyond its definition:
// that its command is not installed, that setting up runs or what it found, or
// what its stored sign-in says. Empty when there is nothing to add.
func (m *Model) sessionText(row config.EffectiveConnection) string {
	st := m.statusOf(row)
	auth := authName(m.live.Effective(), row.ID)
	switch st.kind {
	case stCommand:
		return st.detail + " is not installed: it is not found on PATH. Install it, then press s."
	case stFailed:
		return m.loadErr[row.ID][0]
	case stManual:
		return "Needs a sign-in: run mcparcel auth " + auth + "."
	case stSignIn:
		if m.opts.SignIn == nil {
			return "Needs a sign-in: run mcparcel auth " + auth + "."
		}
		return "Needs a sign-in: s signs you in, in your browser."
	case stSigning, stWorking:
		return "Setting up: " + st.text
	case stSignInUnknown:
		return "Its sign-in cannot be read without a prompt; s starts it anyway."
	case stTools:
		return "Started this session, " + st.text + "."
	}
	return ""
}

// statusLine is the status fitted to w; a toast that is fading out is dim
// (see motion.go).
func (m *Model) statusLine(w int) string {
	line := fit(m.status, w)
	if m.toastFading {
		return m.theme.paint(roleDim, line)
	}
	return line
}
