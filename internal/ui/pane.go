package ui

import (
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// The details pane shows the selected row beside the wide list, in short
// what its server page shows: the label (with why it matched a search when
// the description does not show it, see match.go), the description, the
// state, the To do (without what the state already says), then Runs and
// From, and why it is unavailable or could not start.
// Below the wide size the list keeps its summary line instead.

// Pane width, including the rule and a space on either side of it.
const (
	paneMin = 40
	paneMax = 60
)

func paneWidth(w int) int { return min(max(w*3/8, paneMin), paneMax) }

// listWidth is the wide list's width left of the pane.
func listWidth(w int) int { return w - paneWidth(w) }

// paneDescription is the most lines the pane gives the description.
const paneDescription = 3

// pane renders height lines of n cells for the selected row, and is blank
// when no row is selected. Content taller than the pane first gives up
// description lines, down to one; what still does not fit is cut, with a
// last line that says how much and, outside the search, where Enter
// leaves the search instead, that Enter opens the server's page.
func (m *Model) pane(n, height int) []string {
	lines := make([]string, 0, height)
	if row, ok := m.selected(); ok {
		for most := paneDescription; most > 0; most-- {
			if lines = m.paneLines(row, n, most); len(lines) <= height {
				break
			}
		}
	}
	if len(lines) > height && height > 1 {
		cut := len(lines) - height + 1
		note := plural(cut, "more line", "more lines") + ": Enter shows all."
		if m.screen == screenSearch {
			note = plural(cut, "more line.", "more lines.") // Enter leaves the search here; it does not open the page
		}
		lines = append(lines[:height-1], m.theme.paint(roleDim, fit(note, n)))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

// paneLines is the pane's content for row, n cells wide, with at most most
// description lines.
func (m *Model) paneLines(row config.EffectiveConnection, n, most int) []string {
	t := m.theme
	lines := []string{t.paint(roleTitle, fit(rowLabel(row), n))}
	var description []string
	if row.Definition != nil && row.Definition.Description != "" {
		for _, line := range wrapAtMost(output.DisplayMetadata(firstLine(row.Definition.Description)), n, most) {
			description = append(description, fit(line, n))
		}
	}
	// Why the row matched the search, unless the description shows it.
	if !strings.Contains(strings.ToLower(strings.Join(description, " ")), strings.ToLower(m.search.String())) {
		if match := m.matchLine(row, n); match != "" {
			lines = append(lines, match)
		}
	}
	lines = append(lines, description...)
	st := m.statusOf(row)
	glyph, r := m.mark(st)
	words, wr := m.stateWords(row, st)
	lines = append(lines, "", t.paint(r, glyph)+" "+t.paint(wr, fit(words, n-textWidth(glyph)-1)))
	if todo := paneTodo(m.todo(row)); len(todo) > 0 {
		lines = append(lines, "")
		for _, item := range todo {
			lines = append(lines, m.todoLine(item, "", n, true))
		}
	}
	lines = append(append(lines, ""), m.facts(row, n)...)
	note := ""
	switch {
	case !row.Available:
		note = unavailableText(m.live.State(), row)
	case st.kind == stFailed:
		note = m.sessionText(row)
	}
	if note != "" {
		lines = append(lines, "")
		for _, line := range wrap(note, n) {
			lines = append(lines, fit(line, n))
		}
	}
	return lines
}
