package ui

import (
	"fmt"
	"strings"

	"github.com/dedene/mcparcel/internal/output"
)

// The list's chrome: the header, the domain tabs and the key hints. Color
// only adds styling here: with it stripped, the text is the colorless text.

// header shows MCParcel and the catalog (or the source summary) on the left,
// and on the right how many rows are on and how many of those need
// attention: on but not ready.
func (m *Model) header(w int) string {
	t := m.theme
	on, attention := m.counts()
	ready := roleDim // green only when some row is on and ready
	if on > attention {
		ready = roleOK
	}
	right := t.paint(ready, fmt.Sprintf("%d on", on))
	if attention > 0 {
		right += t.glyph.sep + t.paint(roleWarn, plural(attention, "needs attention", "need attention"))
	}
	room := w - textWidth(right) - 1
	if room < len("MCParcel") {
		return fit(stripSGR(right), w)
	}
	left := t.paint(roleTitle, "MCParcel")
	if info := fit(m.catalogInfo(), room-len("MCParcel  ")); info != "" {
		left += "  " + t.paint(roleDim, info)
	}
	return spread(left, right, w)
}

// counts returns the rows that are on, and those of them not ready.
func (m *Model) counts() (on, attention int) {
	state := m.draft.State()
	for _, row := range m.draft.Effective().Connections {
		if !row.Enabled {
			continue
		}
		on++
		if !lookOf(state, row).ready() {
			attention++
		}
	}
	return on, attention
}

// catalogInfo names the only catalog with its commit and snapshot age, or
// summarizes the sources when there are none or several.
func (m *Model) catalogInfo() string {
	state := m.draft.State()
	if len(state.Local.Sources) != 1 {
		return m.sourceSummary()
	}
	source := state.Local.Sources[0]
	name := state.Catalogs[source.ID].Name
	if name == "" {
		name = source.Owner + "/" + source.Repo
	}
	parts := []string{output.DisplayMetadata(name)}
	if source.Commit != "" {
		parts = append(parts, output.DisplayMetadata(source.Commit[:min(7, len(source.Commit))]))
	}
	for _, s := range m.sources {
		if s.Source.ID == source.ID {
			parts = append(parts, "synced "+ages(s.CacheAgeSeconds)+" ago")
		}
	}
	return strings.Join(parts, m.theme.glyph.sep)
}

// sourceSummary is the header's left side without exactly one catalog.
func (m *Model) sourceSummary() string {
	if n := len(m.draft.State().Local.Sources); n > 0 {
		return fmt.Sprintf("%d catalogs + personal", n)
	}
	return "personal"
}

// scrollTabs shows as many tabs as fit around the current one, which is
// always visible, with the theme's prev and next marks for tabs scrolled off
// either side, one space from the label. The current tab is a pill:
// "[Label 1/2]", in reverse with color; the others show their count dim.
func (m *Model) scrollTabs(w int) string {
	effective := m.draft.Effective()
	current := m.currentTab()
	all := tabs(effective)
	query := m.search.String()
	plain := make([]string, len(all))
	styled := make([]string, len(all))
	at := 0
	for i, t := range all {
		count := tabCount(effective, t, query)
		if t.id == current.id {
			at = i
			plain[i] = "[" + t.label + " " + count + "]"
			styled[i] = m.theme.paint(roleActiveTab, plain[i])
			continue
		}
		plain[i] = " " + t.label + " " + count + " "
		styled[i] = " " + t.label + " " + m.theme.paint(roleDim, count) + " "
	}
	// An inactive tab carries its own padding space; the pill needs one.
	prev := func(from int) string {
		if from == at {
			return m.theme.glyph.prev + " "
		}
		return m.theme.glyph.prev
	}
	next := func(to int) string {
		if to == at {
			return " " + m.theme.glyph.next
		}
		return m.theme.glyph.next
	}
	width := func(from, to int) int {
		n := textWidth(strings.Join(plain[from:to+1], " "))
		if from > 0 {
			n += textWidth(prev(from))
		}
		if to < len(plain)-1 {
			n += textWidth(next(to))
		}
		return n
	}
	from, to := at, at
	for from > 0 && width(from-1, to) <= w {
		from--
	}
	for to < len(plain)-1 && width(from, to+1) <= w {
		to++
	}
	if width(from, to) > w { // the current tab alone is too wide: cut its label, keep it marked
		return m.theme.paint(roleActiveTab, fit(plain[at], w))
	}
	line := strings.Join(styled[from:to+1], " ")
	if from > 0 {
		line = m.theme.paint(roleDim, prev(from)) + line
	}
	if to < len(plain)-1 {
		line += m.theme.paint(roleDim, next(to))
	}
	return line
}

// keys renders a help line of "key: label" hints, separated by three spaces,
// as key chips with dim labels. Without color it reads "key label". The last
// hint (the way out, or help) is kept; a hint before it that does not fit n
// cells is dropped whole, and a shorter one after it may still fit.
func (m *Model) keys(help string, n int) string {
	hints := strings.Split(help, "   ")
	chip := func(hint string) (string, int) {
		key, label, _ := strings.Cut(hint, ": ")
		styled := m.theme.paint(roleKey, key)
		if label != "" {
			styled += " " + m.theme.paint(roleDim, label)
		}
		return styled, textWidth(strings.TrimSpace(key + " " + label))
	}
	last, used := chip(hints[len(hints)-1])
	if used > n {
		key, label, _ := strings.Cut(hints[0], ": ")
		return fit(strings.TrimSpace(key+" "+label), n)
	}
	var parts []string
	for _, hint := range hints[:len(hints)-1] {
		if styled, width := chip(hint); used+len("   ")+width <= n {
			parts, used = append(parts, styled), used+len("   ")+width
		}
	}
	return strings.Join(append(parts, last), "   ")
}
