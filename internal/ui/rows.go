package ui

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

type tab struct{ id, label string }

// tabs lists the effective domains sorted by label then ID, with Other last.
func tabs(effective config.EffectiveConfig) []tab {
	out := make([]tab, 0, len(effective.Domains))
	for id, domain := range effective.Domains {
		if id == "other" {
			continue
		}
		label := domain.Label
		if label == "" {
			label = id
		}
		out = append(out, tab{id: id, label: output.DisplayMetadata(label)})
	}
	slices.SortFunc(out, func(a, b tab) int {
		if c := strings.Compare(a.label, b.label); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	return append(out, tab{id: "other", label: "Other"})
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
	slices.SortFunc(out, func(a, b config.EffectiveConnection) int {
		if c := strings.Compare(rowLabel(a), rowLabel(b)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
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

func runsOn(row config.EffectiveConnection) string {
	if !row.Available || row.Connection == nil {
		return "-"
	}
	h := row.Connection.Transport.HTTP
	if h == nil {
		return "This device"
	}
	if h.URL.Literal != nil {
		if u, err := url.Parse(*h.URL.Literal); err == nil && isLoopback(u.Hostname()) {
			return "This device"
		}
	}
	return "Remote"
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

// missingConfig names each declared input without a value or default, then the
// credential profile when it is unbound or the bound profile is gone. A profile
// the next enable links by itself (edit.AutoProfile) is left out.
func missingConfig(state config.State, row config.EffectiveConnection) []string {
	inputs, profile := config.MissingConfig(state, row)
	var out []string
	for _, name := range inputs {
		out = append(out, "input "+output.DisplayMetadata(name))
	}
	if profile && (edit.AutoProfile(state, row) == "" || !enabling(row)) {
		out = append(out, "credential profile")
	}
	return out
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
	return "Linked credential profile " + output.DisplayMetadata(profile) + "."
}
