package output

import (
	"encoding/json"
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
}
type InspectData struct {
	Revision        uint64                     `json:"revision"`
	Item            config.EffectiveConnection `json:"item"`
	Selection       config.Selection           `json:"selection"`
	Source          *SourceMetadata            `json:"source"`
	SourceRevisions map[string]string          `json:"sourceRevisions"`
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

func humanMetadata(data MetadataData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Revision: %d\n", data.Revision)
	for _, row := range data.Items {
		label := ""
		if row.Definition != nil {
			label = row.Definition.Label
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", DisplayMetadata(row.ID), DisplayMetadata(label), metadataState(row))
	}
	if len(data.Items) == 0 {
		b.WriteString("No connections.\n")
	}
	for _, s := range data.Sources {
		fmt.Fprintf(&b, "Source: %s %s @ %s (snapshot age %.0fs)\n", DisplayMetadata(s.Source.Owner+"/"+s.Source.Repo), DisplayMetadata(s.Source.Path), DisplayMetadata(s.Source.Commit), float64(int64(s.CacheAgeSeconds)))
	}
	return b.String()
}

func humanInspect(data InspectData) string {
	var b strings.Builder
	source := "Personal"
	if data.Source != nil {
		source = data.Source.Source.Owner + "/" + data.Source.Source.Repo
	} else if repository, github := strings.CutPrefix(data.Item.ID, "github:"); github {
		source, _, _ = strings.Cut(repository, "#")
	}
	fmt.Fprintf(&b, "ID: %s\nSource: %s\nRevision: %d\nState: %s\n", DisplayMetadata(data.Item.ID), DisplayMetadata(source), data.Revision, metadataState(data.Item))
	raw, _ := json.Marshal(struct {
		Definition *config.Connection `json:"definition"`
		Connection *config.Connection `json:"connection"`
		Selection  config.Selection   `json:"selection"`
		Blockers   []string           `json:"blockers"`
	}{data.Item.Definition, data.Item.Connection, data.Selection, data.Item.Blockers})
	// Escape string values before indenting so terminal-safe text retains JSON layout.
	var value any
	_ = json.Unmarshal(raw, &value)
	escapeMetadataStrings(value)
	indented, _ := json.MarshalIndent(value, "", "  ")
	b.Write(indented)
	b.WriteByte('\n')
	return b.String()
}

func escapeMetadataStrings(value any) {
	switch v := value.(type) {
	case map[string]any:
		for k, item := range v {
			if text, ok := item.(string); ok {
				v[k] = DisplayMetadata(text)
			} else {
				escapeMetadataStrings(item)
			}
			safe := DisplayMetadata(k)
			if safe != k {
				escaped := v[k]
				delete(v, k)
				v[safe] = escaped
			}
		}
	case []any:
		for i, item := range v {
			if text, ok := item.(string); ok {
				v[i] = DisplayMetadata(text)
			} else {
				escapeMetadataStrings(item)
			}
		}
	}
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

func humanSourceMutation(data SourceMutationData) string {
	status := "Already registered."
	if data.Changed {
		status = "Registered."
	}
	return fmt.Sprintf("Source: %s %s @ %s\nRevision: %d\n%s\n", DisplayMetadata(data.Source.Owner+"/"+data.Source.Repo), DisplayMetadata(data.Source.Path), DisplayMetadata(data.Source.Commit), data.Revision, status)
}

func humanSync(data SyncData) string {
	if len(data.Results) == 0 {
		return "No catalogs are registered. Run 'mcparcel add <owner/repo>' to register one.\n"
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
