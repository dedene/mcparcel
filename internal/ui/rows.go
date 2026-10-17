package ui

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

type tab struct{ id, label string }

// tabs lists the effective domains sorted by label then ID, with Other last.
func tabs(effective config.EffectiveConfig) []tab {
	out := make([]tab, 0, len(effective.Domains))
	for id := range effective.Domains {
		if id == "other" {
			continue
		}
		out = append(out, tab{id: id, label: domainName(effective, id)})
	}
	slices.SortFunc(out, func(a, b tab) int {
		if c := strings.Compare(a.label, b.label); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	return append(out, tab{id: "other", label: "Other"})
}

// domainName is the domain's label, display-safe. A domain without a label
// of its own, none or just its ID (as local add declares the domains a
// personal connection names), reads as its ID humanized: "dev-tools" shows
// as "Dev tools".
func domainName(effective config.EffectiveConfig, id string) string {
	if label := effective.Domains[id].Label; label != "" && label != id {
		return output.DisplayMetadata(label)
	}
	return output.DisplayMetadata(humanize(id))
}

// humanize turns an ID into words: '-' and '_' become spaces and the first
// letter is upper case.
func humanize(id string) string {
	text := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' {
			return ' '
		}
		return r
	}, id)
	r, size := utf8.DecodeRuneInString(text)
	if size == 0 {
		return text
	}
	return string(unicode.ToUpper(r)) + text[size:]
}

func inDomain(row config.EffectiveConnection, domain string) bool {
	if row.Definition == nil {
		return domain == "other"
	}
	return slices.Contains(row.Definition.Domains, domain)
}

func matches(row config.EffectiveConnection, query string) bool {
	if query == "" {
		return true
	}
	query = strings.ToLower(query)
	texts := []string{row.ID}
	if row.Definition != nil {
		texts = append(texts, row.Definition.Label, row.Definition.Description)
	}
	return slices.ContainsFunc(texts, func(text string) bool { return strings.Contains(strings.ToLower(text), query) })
}

// rowsFor returns the domain's rows matching query, sorted by label then ID.
func rowsFor(effective config.EffectiveConfig, domain, query string) []config.EffectiveConnection {
	var out []config.EffectiveConnection
	for _, row := range effective.Connections {
		if inDomain(row, domain) && matches(row, query) {
			out = append(out, row)
		}
	}
	slices.SortFunc(out, compareRows)
	return out
}

// compareRows orders rows by label, then ID.
func compareRows(a, b config.EffectiveConnection) int {
	if c := strings.Compare(rowLabel(a), rowLabel(b)); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// tabCount reads "2/3" (enabled/total), or "(2)" while searching.
func tabCount(effective config.EffectiveConfig, t tab, query string) string {
	if query != "" {
		return fmt.Sprintf("(%d)", len(rowsFor(effective, t.id, query)))
	}
	rows := rowsFor(effective, t.id, "")
	enabled := 0
	for _, row := range rows {
		if row.Enabled {
			enabled++
		}
	}
	return fmt.Sprintf("%d/%d", enabled, len(rows))
}

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

func sourceLabel(state config.State, row config.EffectiveConnection) string {
	if row.SourceID == "personal" {
		return "Personal"
	}
	for _, source := range state.Local.Sources {
		if source.ID == row.SourceID {
			return output.DisplayMetadata(source.Owner + "/" + source.Repo)
		}
	}
	return "-"
}

// missingConfig names each declared input without a value or default, then
// profileNeed when the 1Password profile is unbound or the bound profile is
// gone. A profile the next enable links by itself (edit.AutoProfile) is left
// out.
func missingConfig(state config.State, row config.EffectiveConnection) []string {
	inputs, profile := config.MissingConfig(state, row)
	var out []string
	for _, name := range inputs {
		out = append(out, output.DisplayMetadata(name))
	}
	if profile && (edit.AutoProfile(state, row) == "" || !enabling(row)) {
		out = append(out, profileNeed)
	}
	return out
}

// profileNeed is what missingConfig lists for a profile to link.
const profileNeed = "a 1Password profile"

// needsText joins what a row misses: "workspace", "workspace and a
// 1Password profile", "a, b and c".
func needsText(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// autoLinks returns the profile Space links when that link is all the row
// misses (edit.AutoProfile), else "".
func autoLinks(state config.State, row config.EffectiveConnection) string {
	if !enabling(row) || len(missingConfig(state, row)) > 0 {
		return ""
	}
	return edit.AutoProfile(state, row)
}

// enabling reports whether Space on the row enables it: it is off, or on with
// a review to accept.
func enabling(row config.EffectiveConnection) bool {
	return row.Available && (!row.Enabled || row.ReviewRequired)
}

func linkedText(profile string) string {
	return "Linked 1Password profile " + output.DisplayMetadata(profile) + "."
}

// authName is the name mcparcel auth accepts for id, display-safe: a
// personal connection's base name when it resolves back to id, else id.
func authName(effective config.EffectiveConfig, id string) string {
	name := id
	if base := config.ConnectionBase(id); base != "" {
		ids := slices.Collect(maps.Keys(effective.Connections))
		if resolved, err := config.ResolveID(base, effective.Aliases, ids); err == nil && resolved == id {
			name = base
		}
	}
	return output.DisplayMetadata(output.AuthTarget(name, id))
}

func rowLabel(row config.EffectiveConnection) string {
	if row.Definition != nil && row.Definition.Label != "" {
		return output.DisplayMetadata(row.Definition.Label)
	}
	return output.DisplayMetadata(row.ID)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
