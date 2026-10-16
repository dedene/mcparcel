package ui

import (
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// The server page's look: "<Domain> / <Label>" with the state on the right,
// the description, the actions (the focus line), then the To do (without
// the steps an action above takes) or, once they are known and that To do
// has no step left, the tools, then
// Runs and From, and with i the technical facts. The page opens on the first
// action, the next step.

// crumb is "<Domain> / <Label>": the domain the list shows, when the row is
// in it, else its first domain.
func (m *Model) crumb(row config.EffectiveConnection) string {
	domain := m.currentTab()
	label := domain.label
	if !inDomain(row, domain.id) && row.Definition != nil && len(row.Definition.Domains) > 0 {
		label = m.domainLabel(row.Definition.Domains[0])
	}
	return label + " / " + rowLabel(row)
}

func (m *Model) detailsPage() []string {
	row := m.detailRow()
	w, _ := m.size()
	t := m.theme
	st := m.statusOf(row)
	glyph, r := m.mark(st)
	words, wr := m.stateWords(row, st)
	title := spread(t.paint(roleTitle, m.crumb(row)), t.paint(r, glyph)+" "+t.paint(wr, words), w)

	var head []string
	if d := row.Definition; d != nil && d.Description != "" {
		for _, line := range wrapAtMost(output.DisplayMetadata(firstLine(d.Description)), w, 2) {
			head = append(head, t.paint(roleDim, line))
		}
	}
	if !row.Available {
		head = append(append(head, ""), wrap(unavailableText(m.live.State(), row), w)...)
	}
	head = append(head, "")
	actions := m.actions(row)
	m.detailCursor = min(m.detailCursor, max(len(actions)-1, 0))
	// Notes line up after the labels; a label longer than labelMax has its
	// note after it instead.
	const labelMax = 24
	labelWidth := 0
	for _, a := range actions {
		labelWidth = max(labelWidth, min(textWidth(a.label), labelMax)+gutter)
	}
	focus := -1
	for i, a := range actions {
		// A label shows whole where the line allows; its note gets the rest.
		width := min(max(labelWidth, textWidth(a.label)+gutter), max(w-2, 1))
		line := "  " + cell(a.label, width)
		rest := w - textWidth(line)
		switch {
		case i != m.detailCursor:
			line += t.paint(roleDim, fit(a.note, rest))
		case m.editing:
			line = "> " + cell(a.label, width) + m.input.value.render(rest)
		default:
			line = "> " + cell(a.label, width) + fit(a.note, rest)
		}
		if i == m.detailCursor {
			focus = len(head)
		}
		head = append(head, line)
		if i == m.detailCursor && m.editing {
			head = append(head, m.editLines(row, w)...)
		}
	}

	tools := m.toolRows(row.ID)
	if m.loaded[row.ID].tools == nil {
		tools = nil
	}
	// The tools take the place of a To do with no step left to list.
	if page := pageTodo(m.todo(row), actions); len(page) > 0 && (tools == nil || !allDone(page)) {
		head = append(head, "", t.paint(roleDim, "To do"))
		for _, item := range page {
			head = append(head, m.todoLine(item, "  ", w, true))
		}
	}
	if st.kind == stFailed {
		head = append(head, "")
		for _, line := range m.loadErr[row.ID] {
			head = append(head, wrap(line, w)...)
		}
	}

	tail := append([]string{""}, m.facts(row, w)...)
	if m.technical {
		tail = append(append(tail, ""), m.technicalLines(row, w)...)
	}
	if text := m.signInText(); text != "" {
		tail = append(append(tail, ""), wrap(text, w)...)
	}
	_, room := m.pageStatus()
	body := append(head, m.toolLines(tools, w, room-len(head)-len(tail))...)
	body = append(body, tail...)

	help := "Enter: do it   Space: on/off   s: set up   u: undo   i: technical   ?: help   " + t.glyph.left + ": back"
	switch {
	case m.editing:
		help = "Enter: set it   Ctrl+U: clear   Esc: cancel"
		if m.input.current != "" {
			help = "Enter: set it (empty keeps it)   Esc: cancel"
		}
	case m.finish.current() != nil:
		help = m.runHelp()
	case m.finish != nil:
		help = "Enter: do it   Space: on/off   q: stop setting up   " + t.glyph.left + ": back"
	}
	return m.titledPage(title, body, focus, help)
}

// editLines follow the input being edited: its description, the current
// host of a url input, and an error.
func (m *Model) editLines(row config.EffectiveConnection, w int) []string {
	var lines []string
	indent := "    "
	if row.Definition != nil {
		if d := row.Definition.Inputs[m.input.name].Description; d != "" {
			lines = append(lines, m.theme.paint(roleDim, fit(indent+output.DisplayMetadata(firstLine(d)), w)))
		}
	}
	if m.input.current != "" {
		lines = append(lines, fit(indent+"Now "+m.input.current+"; an empty Enter keeps it.", w))
	}
	if m.input.err != "" {
		lines = append(lines, fit(indent+m.input.err, w))
	}
	return lines
}

func allDone(items []todoItem) bool {
	for _, item := range items {
		if item.mark == todoStep {
			return false
		}
	}
	return true
}

// toolLines lists the loaded tools, a mark and the name, then the first
// line of the description, in at most room lines; the rest are counted.
func (m *Model) toolLines(rows []toolRow, w, room int) []string {
	if len(rows) == 0 || room < 3 {
		return nil
	}
	nameWidth := 0
	for _, r := range rows {
		nameWidth = max(nameWidth, textWidth(r.display)+gutter)
	}
	nameWidth = min(nameWidth, max(w/3, 14))
	g := m.theme.glyph
	lines := []string{"", m.theme.paint(roleDim, "Tools")}
	shown := rows
	if len(rows) > room-2 {
		shown = rows[:room-3]
	}
	for _, r := range shown {
		mark, role := g.done, roleOK
		if !r.enabled {
			mark, role = g.off, roleDim
		}
		lead := "  " + mark + " " + cell(r.display, nameWidth)
		lines = append(lines, "  "+m.theme.paint(role, mark)+" "+cell(r.display, nameWidth)+m.theme.paint(roleDim, fit(r.description, w-textWidth(lead))))
	}
	if more := len(rows) - len(shown); more > 0 {
		lines = append(lines, m.theme.paint(roleDim, fit("  "+plural(more, "more tool", "more tools")+": Choose tools lists them all.", w)))
	}
	return lines
}
