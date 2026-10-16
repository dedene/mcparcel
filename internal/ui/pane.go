package ui

import (
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// The details pane shows the selected row beside the wide list, in short
// what its server page shows: the label, the description, the state, the
// To do, then Runs and From, and why it is unavailable or could not start.
// Below the wide size the list keeps its summary line instead.

// Pane width, including the rule and a space on either side of it.
const (
	paneMin = 40
	paneMax = 60
)

func paneWidth(w int) int { return min(max(w*3/8, paneMin), paneMax) }

// listWidth is the wide list's width left of the pane.
func listWidth(w int) int { return w - paneWidth(w) }

// pane renders height lines of n cells for the selected row; lines past the
// height are cut, and the pane is blank when no row is selected.
func (m *Model) pane(n, height int) []string {
	lines := make([]string, 0, height)
	if row, ok := m.selected(); ok {
		lines = m.paneLines(row, n)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func (m *Model) paneLines(row config.EffectiveConnection, n int) []string {
	t := m.theme
	lines := []string{t.paint(roleTitle, fit(rowLabel(row), n))}
	if row.Definition != nil && row.Definition.Description != "" {
		for _, line := range wrapAtMost(output.DisplayMetadata(firstLine(row.Definition.Description)), n, 3) {
			lines = append(lines, fit(line, n))
		}
	}
	st := m.statusOf(row)
	glyph, r := m.mark(st)
	words, wr := m.stateWords(row, st)
	lines = append(lines, "", t.paint(r, glyph)+" "+t.paint(wr, fit(words, n-textWidth(glyph)-1)))
	if todo := m.todo(row); len(todo) > 0 {
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
