package output

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
)

type SourceMetadata struct {
	Source          config.Source `json:"source"`
	CacheAgeSeconds float64       `json:"cacheAgeSeconds"`
}
type MetadataData struct {
	Revision        uint64                       `json:"revision"`
	Items           []config.EffectiveConnection `json:"items"`
	Domains         map[string]config.Domain     `json:"domains"`
	SourceRevisions map[string]string            `json:"sourceRevisions"`
	Sources         []SourceMetadata             `json:"sources"`
	// Human output only. Table prints a header row, aligned columns and the
	// catalog sources (stdout is a terminal); otherwise rows stay
	// tab-separated for pipes. List, Domain and Available pick the empty
	// state: Available counts every available connection. Width is the
	// terminal's columns, 0 when unknown: a table wider than it cuts labels.
	Table     bool   `json:"-"`
	Width     int    `json:"-"`
	List      bool   `json:"-"`
	Domain    string `json:"-"`
	Available int    `json:"-"`
}

func DisplayMetadata(text string) string {
	for _, r := range text {
		if r < 32 || r > 126 {
			return strconv.QuoteToASCII(text)
		}
	}
	return text
}

func metadataState(row config.EffectiveConnection) string {
	switch {
	case !row.Available:
		return "Unavailable"
	case !row.Enabled:
		return "Disabled"
	case row.ReviewRequired:
		return "Review required"
	case slices.Contains(row.Blockers, "config_required"):
		return "Configuration required"
	default:
		return "Ready"
	}
}

// humanMetadata prints one row per connection: ID, label and state. On a
// terminal the rows get a header and aligned columns, then the catalog
// sources; in a pipe they are tab-separated with no header and nothing else.
// Cells are terminal-safe ASCII (DisplayMetadata), so width is length.
func humanMetadata(data MetadataData) string {
	var b strings.Builder
	if len(data.Items) == 0 && data.Table {
		b.WriteString(EmptyMetadata(data))
	}
	rows := make([][3]string, 0, len(data.Items))
	for _, row := range data.Items {
		label := ""
		if row.Definition != nil {
			label = row.Definition.Label
		}
		rows = append(rows, [3]string{DisplayMetadata(row.ID), DisplayMetadata(label), metadataState(row)})
	}
	if !data.Table {
		for _, r := range rows {
			fmt.Fprintf(&b, "%s\t%s\t%s\n", r[0], r[1], r[2])
		}
		return b.String()
	}
	if len(rows) > 0 {
		idWidth, labelWidth, stateWidth := len("ID"), len("Label"), len("State")
		for _, r := range rows {
			idWidth, labelWidth, stateWidth = max(idWidth, len(r[0])), max(labelWidth, len(r[1])), max(stateWidth, len(r[2]))
		}
		// Too wide for the terminal: the label gives way, so ID and State
		// stay on one line.
		if data.Width > 0 && idWidth+2+labelWidth+2+stateWidth > data.Width {
			labelWidth = max(len("Label"), data.Width-idWidth-4-stateWidth)
		}
		for i, r := range append([][3]string{{"ID", "Label", "State"}}, rows...) {
			if i > 0 && len(r[1]) > labelWidth {
				r[1] = r[1][:labelWidth-3] + "..."
			}
			fmt.Fprintf(&b, "%-*s  %-*s  %s\n", idWidth, r[0], labelWidth, r[1], r[2])
		}
	}
	if len(data.Sources) > 0 {
		b.WriteByte('\n')
	}
	for _, s := range data.Sources {
		fmt.Fprintf(&b, "Source: %s %s @ %s (snapshot age %.0fs)\n", DisplayMetadata(s.Source.Owner+"/"+s.Source.Repo), DisplayMetadata(s.Source.Path), DisplayMetadata(s.Source.Commit), float64(int64(s.CacheAgeSeconds)))
	}
	return b.String()
}

// EmptyMetadata says why list or catalog has no rows and what to run.
func EmptyMetadata(data MetadataData) string {
	switch {
	case data.Available == 0:
		return "No connections yet. Run mcparcel setup, or mcparcel add <owner/repo>.\n"
	case data.List && data.Available == 1:
		return "Nothing is on. 1 connection is available. Run mcparcel catalog to see it.\n"
	case data.List:
		return fmt.Sprintf("Nothing is on. %d connections are available. Run mcparcel catalog to see them.\n", data.Available)
	case data.Domain != "":
		return fmt.Sprintf("No connections in domain %s. Run mcparcel catalog to see all of them.\n", DisplayMetadata(data.Domain))
	}
	return "No connections.\n"
}

type SourceMutationData struct {
	Revision uint64        `json:"revision"`
	Source   config.Source `json:"source"`
	Changed  bool          `json:"changed"`
}
type SourceSyncResult struct {
	Repository string              `json:"repository"`
	SourceID   string              `json:"sourceId"`
	Plan       *catalog.UpdatePlan `json:"plan"`
	Applied    bool                `json:"applied"`
	Accepted   []string            `json:"accepted"`
	Revision   uint64              `json:"revision"`
	Error      *Error              `json:"error"`
}
type SyncData struct {
	Apply   bool               `json:"apply"`
	Results []SourceSyncResult `json:"results"`
}

// humanSourceMutation is add's line, worded as join's: the repository and
// the short commit of its snapshot.
func humanSourceMutation(data SourceMutationData) string {
	repository := DisplayMetadata(data.Source.Owner + "/" + data.Source.Repo)
	commit := DisplayMetadata(data.Source.Commit[:min(7, len(data.Source.Commit))])
	if !data.Changed {
		return fmt.Sprintf("%s is already added (%s). Run mcparcel sync to update it.\n", repository, commit)
	}
	return fmt.Sprintf("Added %s (%s). Run mcparcel setup to choose its connections.\n", repository, commit)
}

func humanSync(data SyncData) string {
	if len(data.Results) == 0 {
		return "No catalogs are registered. Run mcparcel add <owner/repo> to register one.\n"
	}
	var b strings.Builder
	for _, r := range data.Results {
		status := "preview"
		if r.Applied {
			status = "applied"
		}
		if r.Error != nil {
			status = "failed"
		}
		commit := "unavailable"
		if r.Plan != nil {
			commit = r.Plan.CandidateCommit
		}
		fmt.Fprintf(&b, "%s: %s @ %s\n", DisplayMetadata(r.Repository), status, DisplayMetadata(commit))
		if r.Plan != nil {
			for _, id := range r.Plan.Added {
				fmt.Fprintf(&b, "  added %s\n", DisplayMetadata(id))
			}
			for _, id := range r.Plan.Removed {
				fmt.Fprintf(&b, "  removed %s\n", DisplayMetadata(id))
			}
			for _, change := range r.Plan.Changed {
				fmt.Fprintf(&b, "  changed %s", DisplayMetadata(change.ID))
				for _, field := range change.Fields {
					fmt.Fprintf(&b, " %s", DisplayMetadata(field))
				}
				if change.ExecutionOrAuth {
					b.WriteString(" review required")
				}
				b.WriteByte('\n')
			}
		}
		for _, id := range r.Accepted {
			fmt.Fprintf(&b, "  accepted %s\n", DisplayMetadata(id))
		}
	}
	return b.String()
}

// PlanData is what a --dry-run command would change. Nothing is saved, so
// Revision is the current one.
type PlanData struct {
	DryRun   bool           `json:"dryRun"`
	Revision uint64         `json:"revision"`
	Source   *config.Source `json:"source,omitempty"`
	Changes  []PlanChange   `json:"changes"`
}

// PlanChange is one connection in a plan. Change is turn_off, none (disable
// of a connection that is off), remove (its definition goes) or unavailable
// (an enabled connection whose catalog goes). Enabled is its state now.
type PlanChange struct {
	Connection string `json:"connection"`
	Change     string `json:"change"`
	Enabled    bool   `json:"enabled"`
}
