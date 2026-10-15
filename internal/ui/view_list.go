package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// Wide list layout: header, tabs, search, then the column header and the
// list on the left with the details pane beside them, then a blank line, two
// help lines, the status and the action line.
const listChrome = 9

// Wide list columns, after the focus prefix and the glyph: MCP, Needs and,
// with two or more sources, Source. Each column ends in the gutter. Where a
// row runs is in the details pane.
const (
	colMinName   = 14
	colMaxName   = 28
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
	_, h := m.size()
	if m.compact() {
		return max(h-compactChrome, 1)
	}
	return max(h-listChrome, 1)
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
	first := m.keys("Space: on/off   Left/Right: domain   Up/Down: MCP   Enter: details   Ctrl+S: save", w)
	second := m.keys("/: search   Tab: actions   A: add personal connection   q: cancel   ?: help", w)
	switch m.screen {
	case screenSearch:
		first, second = m.keys(searchHelp, w), fit("Typing filters label, ID and description in every tab.", w)
	case screenConflict:
		first, second = fit(m.conflict, w), m.keys(conflictKeys, w)
	}
	return append(lines, "", first, second, m.statusText(w), m.actionLine(w))
}

// searchHelp replaces the list keys while typing a search: Space and letters
// type into the query.
const searchHelp = "Enter/Down: list   Backspace/Ctrl+U: edit   Esc: clear"

// conflictKeys are the choices a save conflict offers.
const conflictKeys = "r: reload and reapply   Esc: back"

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
// MCP and Needs share its width.
func (m *Model) showSource() bool {
	seen := map[string]bool{}
	for _, row := range m.draft.Effective().Connections {
		seen[row.SourceID] = true
	}
	return len(seen) > 1
}

// wideColumns are the list's column widths for a list lw cells wide.
type wideColumns struct{ lead, name, needs, source int }

func (m *Model) wideColumns(lw int) wideColumns {
	c := wideColumns{lead: 2 + textWidth(m.theme.glyph.on) + 1}
	if m.showSource() {
		c.source = min(max(lw/4, colMinSource), colMaxSource)
	}
	rest := max(lw-c.lead-c.source, 2*colMinName)
	c.name = min(max(rest/2, colMinName), colMaxName)
	c.needs = rest - c.name
	return c
}

func (m *Model) columnHeader(lw int) string {
	c := m.wideColumns(lw)
	line := strings.Repeat(" ", c.lead) + cell("MCP", c.name)
	if c.source > 0 {
		return fit(line+cell("Needs", c.needs)+"Source", lw)
	}
	return fit(line+"Needs", lw)
}

// wideRow renders a row in the wide columns. Styled, each part is painted;
// plain, the caller paints the whole line (focus or flash), so no inner
// reset ends that style early.
func (m *Model) wideRow(lw int) rowFormat {
	c := m.wideColumns(lw)
	state := m.draft.State()
	return func(row config.EffectiveConnection, prefix string, styled bool) string {
		look := lookOf(state, row)
		line := prefix + m.markText(look, styled) + " " + cell(rowLabel(row), c.name) + m.needsText(look.listNeeds(), c.needs, styled)
		if c.source > 0 {
			return fit(line+sourceLabel(state, row), lw)
		}
		return fit(line, lw)
	}
}

// markText is the row's glyph, colored when styled.
func (m *Model) markText(look rowLook, styled bool) string {
	glyph, r := look.mark(m.theme.glyph)
	if !styled {
		return glyph
	}
	return m.theme.paint(r, glyph)
}

// needsText fits needs into a column of n cells that ends in the gutter,
// two spaces apart, each colored by its role when styled. A need that does
// not fit whole is cut with "..." and ends the column; a later need is left
// out unless at least minNeed cells (or all of it) fit.
func (m *Model) needsText(needs []need, n int, styled bool) string {
	const minNeed = 6
	room := n - gutter
	var out string
	used := 0
	for i, nd := range needs {
		text := nd.text
		if i > 0 {
			if room-used-2 < min(textWidth(text), minNeed) {
				break
			}
			out += "  "
			used += 2
		}
		text = fit(text, room-used)
		used += textWidth(text)
		if styled {
			text = m.theme.paint(nd.role, text)
		}
		out += text
		if used >= room {
			break
		}
	}
	return out + strings.Repeat(" ", max(n-used, 0))
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
		state := m.draft.State()
		switch q := m.search.String(); {
		case q != "":
			lines = append(lines, fit(fmt.Sprintf("  No MCPs match %s in %s. Esc clears the search.", quoted(q), name), w))
		case len(state.Local.Sources) == 0 && len(state.Personal.Connections) == 0:
			lines = append(lines, fit("  No catalogs yet. Add one from a terminal: mcparcel add <owner/repo>.", w), fit("  Press A to add a personal connection.", w))
		default:
			lines = append(lines, fit("  No MCPs in "+name+".", w))
		}
	}
	for i := m.offset; i < len(rows) && i < m.offset+page; i++ {
		row := rows[i]
		prefix := "  "
		focused := i == m.cursor && m.focus == focusList
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
	state := m.draft.State()
	switch lookOf(state, row).state {
	case rowUnavailable:
		return unavailableText(state, row)
	case rowReview:
		return "Review required. Space accepts it (same as mcparcel enable " + output.DisplayMetadata(row.ID) + ")."
	case rowNeedsConfig:
		return "Configuration required: " + strings.Join(missingConfig(state, row), ", ") + "."
	}
	if profile := autoLinks(state, row); profile != "" {
		return "Space turns it on and links credential profile " + output.DisplayMetadata(profile) + "."
	}
	if text := m.sessionText(row); text != "" {
		return text
	}
	return "Configuration ready. Connection not checked."
}

// sessionText is what the finish step or Connect found this session for a
// connection that is on: why it could not start, that it started, or that
// it still needs a sign-in. Empty when nothing was tried.
func (m *Model) sessionText(row config.EffectiveConnection) string {
	if !row.Enabled {
		return ""
	}
	if failure := m.loadErr[row.ID]; len(failure) > 0 {
		return "Not started. " + failure[0]
	}
	if loaded, ok := m.loaded[row.ID]; ok {
		return "Started this session, " + plural(len(loaded.tools), "tool", "tools") + "."
	}
	if outcome, ok := m.outcomes[row.ID]; ok && outcome == finSkipped {
		return "Needs a sign-in: run mcparcel auth " + output.DisplayMetadata(output.AuthTarget(row.ID, row.ID)) + "."
	}
	return ""
}

func (m *Model) statusLine() string {
	if m.screen == screenConfirm {
		return fmt.Sprintf("Discard %s? y/N", plural(m.draft.Changes(), "unsaved change", "unsaved changes"))
	}
	return m.status
}

// statusText is the status line fitted to w; a toast that is fading out is
// dim (see motion.go).
func (m *Model) statusText(w int) string {
	line := fit(m.statusLine(), w)
	if m.toastFading && m.screen != screenConfirm {
		return m.theme.paint(roleDim, line)
	}
	return line
}

func (m *Model) actionLine(w int) string {
	changes := "No unsaved changes"
	if n := m.draft.Changes(); n > 0 {
		changes = plural(n, "unsaved change", "unsaved changes")
	}
	button := func(label string, focus int) string {
		if m.focus == focus {
			return ">" + m.theme.paint(roleSelected, "["+label+"]")
		}
		return " [" + label + "]"
	}
	right := button("Save", focusSave) + button("Cancel", focusCancel)
	width := textWidth(right)
	if width >= w {
		return fit(stripSGR(right), w)
	}
	return pad(changes, w-width) + right
}
