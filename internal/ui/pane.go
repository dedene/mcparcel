package ui

import (
	"net/url"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// The details pane shows the selected row beside the wide list, so Enter is
// needed only to edit. Like the details page it shows only non-secret
// metadata. Below the wide size the list keeps its summary line instead.

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
	state := m.draft.State()
	look := lookOf(state, row)
	field := func(name, value string) string {
		return t.paint(roleDim, pad(name, 8)) + fit(value, n-8)
	}
	lines := []string{t.paint(roleTitle, fit(rowLabel(row), n))}
	if row.Definition != nil && row.Definition.Description != "" {
		for _, line := range wrapAtMost(output.DisplayMetadata(firstLine(row.Definition.Description)), n, 3) {
			lines = append(lines, fit(line, n))
		}
	}
	glyph, r := look.mark(t.glyph)
	lines = append(lines, "", field("State", t.paint(r, glyph)+" "+look.label()))
	if checklist := look.checklist(); len(checklist) > 0 {
		lines = append(lines, "", t.paint(roleDim, "Needs"))
		for _, nd := range checklist {
			mark, r := t.glyph.todo, nd.role
			if nd.done {
				mark, r = t.glyph.done, roleOK
			}
			name := nd.text
			if nd.name != "" {
				name = nd.name
			}
			text := fit(name+t.glyph.sep+nd.note, n-1-textWidth(mark)-1)
			lines = append(lines, " "+t.paint(r, mark)+" "+text)
		}
	}
	lines = append(lines, "")
	if row.Available && row.Definition != nil {
		lines = append(lines, field("Runs", runsOn(row)+t.glyph.sep+runsWhat(row)), field("Tools", m.policyText(row)))
	}
	if m.showSource() {
		lines = append(lines, field("Source", m.paneSource(row)))
	}
	if note := m.paneNote(row, look); note != "" {
		lines = append(lines, "")
		for _, line := range wrap(note, n) {
			lines = append(lines, fit(line, n))
		}
	}
	return lines
}

// paneNote is what the state line adds to State and Needs: why a row is
// unavailable, how to accept a review, that Space links a profile, or what
// starting it found this session.
func (m *Model) paneNote(row config.EffectiveConnection, look rowLook) string {
	switch {
	case look.state == rowUnavailable || look.state == rowReview:
		return m.stateText(row)
	case look.state != rowNeedsConfig && autoLinks(m.draft.State(), row) != "":
		return m.stateText(row)
	}
	return m.sessionText(row)
}

// paneSource is the row's source. A catalog row adds its commit and how long
// ago it synced, unless the header already shows them for the only catalog.
func (m *Model) paneSource(row config.EffectiveConnection) string {
	state := m.draft.State()
	label := sourceLabel(state, row)
	if len(state.Local.Sources) == 1 {
		return label
	}
	for _, s := range m.sources {
		if s.Source.ID == row.SourceID && row.SourceID != "personal" {
			commit := output.DisplayMetadata(s.Source.Commit[:min(7, len(s.Source.Commit))])
			return label + m.theme.glyph.sep + commit + m.theme.glyph.sep + "synced " + ages(s.CacheAgeSeconds) + " ago"
		}
	}
	return label
}

// runsWhat is the command a row runs or the host it connects to: never an
// argument, a URL path or a query.
func runsWhat(row config.EffectiveConnection) string {
	c := row.Connection
	if c == nil {
		c = row.Definition
	}
	switch {
	case c == nil:
		return "-"
	case c.Transport.Stdio != nil:
		return valueText(c.Transport.Stdio.Command)
	case c.Transport.HTTP.URL.Literal == nil:
		return valueText(c.Transport.HTTP.URL)
	}
	u, err := url.Parse(*c.Transport.HTTP.URL.Literal)
	if err != nil {
		return "(invalid URL)"
	}
	return output.DisplayMetadata(u.Host)
}
