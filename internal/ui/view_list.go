package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// Wide list layout: header, tabs, search, column header, list, blank,
// three summary lines, blank, two help lines, status and the action line.
const listChrome = 13

const (
	colEnabled = 9
	colState   = 24
	colSource  = 30
	colRunsOn  = 12
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
	lines := []string{m.style.bold(spread("MCParcel / Setup", m.sourceSummary(), w)), m.tabLine(w), m.searchLine(w), m.columns("  MCP", "Enabled", "State", "Source", "Runs on", w)}
	state := m.draft.State()
	lines = append(lines, m.listBody(w, func(row config.EffectiveConnection, prefix string) string {
		return m.columns(prefix+rowLabel(row), enabledMark(row), readiness(row), sourceLabel(state, row), runsOn(row), w)
	})...)
	lines = append(lines, "")
	lines = append(lines, m.summary(w)...)
	help := []string{
		"Left/Right: domain   Up/Down: MCP   Space: toggle   Enter: details   Ctrl+S: save",
		"/: search   Tab: actions   A: add personal connection   q: cancel   ?: help",
	}
	if m.screen == screenSearch {
		help = []string{searchHelp, "Typing filters label, ID and description in every tab."}
	}
	return append(lines, "", fit(help[0], w), fit(help[1], w), fit(m.statusLine(), w), m.actionLine(w))
}

// searchHelp replaces the list keys while typing a search: Space and letters
// type into the query.
const searchHelp = "Enter/Down: list   Esc: clear   Backspace/Ctrl+U: edit"

func (m *Model) sourceSummary() string {
	switch n := len(m.draft.State().Local.Sources); n {
	case 0:
		return "personal"
	case 1:
		return "1 catalog + personal"
	default:
		return fmt.Sprintf("%d catalogs + personal", n)
	}
}

func (m *Model) tabLine(w int) string {
	effective := m.draft.Effective()
	current := m.currentTab()
	parts := []string{}
	for _, t := range tabs(effective) {
		label := tabLabel(effective, t, m.search.String())
		if t.id == current.id {
			label = "[" + label + "]"
		} else {
			label = " " + label + " "
		}
		parts = append(parts, label)
	}
	return fit(strings.Join(parts, " "), w)
}

func (m *Model) searchLine(w int) string {
	switch {
	case m.screen == screenSearch:
		return fit("Search: "+m.search.render(w-8), w)
	case m.search.String() != "":
		return fit("Search: "+output.DisplayMetadata(m.search.String())+"   (Esc clears)", w)
	}
	return ""
}

func (m *Model) mcpWidth(w int) int {
	return max(w-2-colEnabled-colState-colSource-colRunsOn, 12)
}

func (m *Model) columns(mcp, enabled, state, source, runs string, w int) string {
	return fit(pad(mcp, 2+m.mcpWidth(w))+pad(enabled, colEnabled)+pad(state, colState)+pad(source, colSource)+runs, w)
}

// listBody renders the visible rows with format, or the empty state, padded
// to the list height.
func (m *Model) listBody(w int, format func(row config.EffectiveConnection, prefix string) string) []string {
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
		line := format(row, prefix)
		if focused {
			line = m.style.reverse(pad(line, w))
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

// summary describes the selected row, or the conflict choices.
func (m *Model) summary(w int) []string {
	if m.screen == screenConflict {
		return []string{fit(m.conflict, w), fit("r: reload and reapply   Esc: back", w), ""}
	}
	row, ok := m.selected()
	if !ok {
		return []string{"", "", ""}
	}
	description := ""
	if row.Definition != nil {
		description = output.DisplayMetadata(firstLine(row.Definition.Description))
	}
	return []string{m.style.bold(fit(rowLabel(row), w)), fit(description, w), fit(m.stateText(row), w)}
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func (m *Model) stateText(row config.EffectiveConnection) string {
	switch readiness(row) {
	case "Unavailable":
		return unavailableText(m.draft.State(), row)
	case "Review required":
		return "Review required. Space accepts it (same as mcparcel enable " + output.DisplayMetadata(row.ID) + ")."
	case "Configuration required":
		return "Configuration required: " + strings.Join(missingConfig(m.draft.State(), row), ", ") + "."
	}
	return "Configuration ready. Connection not checked."
}

func (m *Model) statusLine() string {
	if m.screen == screenConfirm {
		return fmt.Sprintf("Discard %s? y/N", plural(m.draft.Changes(), "unsaved change", "unsaved changes"))
	}
	return m.status
}

func (m *Model) actionLine(w int) string {
	changes := "No unsaved changes"
	if n := m.draft.Changes(); n > 0 {
		changes = plural(n, "unsaved change", "unsaved changes")
	}
	button := func(label string, focus int) string {
		if m.focus == focus {
			return ">" + m.style.reverse("["+label+"]")
		}
		return " [" + label + "]"
	}
	right := button("Save", focusSave) + button("Cancel", focusCancel)
	width := len(stripSGR(right))
	if width >= w {
		return fit(stripSGR(right), w)
	}
	return pad(changes, w-width) + right
}

// stripSGR removes the escape sequences style adds.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
