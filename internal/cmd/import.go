package cmd

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
)

type ImportCmd struct {
	Mcporter ImportMcporterCmd `cmd:"" json:"-" help:"Preview or apply a mcporter file offline."`
}
type ImportMcporterCmd struct {
	File     string   `required:"" name:"file" json:"-"`
	Bindings string   `name:"bindings" json:"-"`
	Only     []string `name:"only" sep:"none" json:"-" help:"Select one ID per flag; repeat as --only paper --only context7."`
	Apply    bool     `name:"apply" json:"-"`
}

func (c *ImportMcporterCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	raw, err := readCommandFile(ctx, c.File)
	if err != nil {
		return err
	}
	var bindings map[string]config.CredentialBinding
	if c.Bindings != "" {
		b, e := readCommandFile(ctx, c.Bindings)
		if e != nil {
			return e
		}
		bindings, err = config.DecodeBindings(b)
		if err != nil {
			return err
		}
	}
	report, err := config.ImportMcporter(raw, bindings)
	if err != nil {
		return err
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	planned, err := config.PlanImport(state, report, c.Only)
	if err != nil {
		return err
	}
	if c.Apply {
		planned, err = config.ApplyImport(ctx, store, state.Selections.Revision, report, c.Only)
		if err != nil {
			return err
		}
	}
	var payload any = planned
	if !opts.JSON {
		payload = renderImport(planned)
	}
	return writeSuccess(s, opts, payload)
}

func importDisplay(text string) string {
	for _, r := range text {
		if r < 32 || r > 126 {
			return strconv.QuoteToASCII(text)
		}
	}
	return text
}

func renderImport(report config.ImportReport) string {
	var b strings.Builder
	title := "Import preview"
	if report.Applied {
		title = "Import applied"
	}
	b.WriteString(title + "\n")
	rows := slices.Clone(report.Entries)
	slices.SortFunc(rows, func(a, c config.ImportEntry) int { return strings.Compare(a.ID, c.ID) })
	selected, applicable, blocked, omitted := 0, 0, 0, 0
	issue := func(label string, i config.ImportIssue) {
		fmt.Fprintf(&b, "  %s %s: %s", label, importDisplay(i.Code), importDisplay(i.Path))
		if i.Variable != "" {
			fmt.Fprintf(&b, " (%s)", importDisplay(i.Variable))
		}
		b.WriteByte('\n')
	}
	for _, row := range rows {
		status := "blocked"
		if !row.Selected {
			omitted++
			continue
		} else {
			selected++
			if row.Applicable {
				applicable++
				status = "applicable"
			} else {
				blocked++
			}
			if row.Applied {
				status = "applied"
			}
		}
		fmt.Fprintf(&b, "%s: %s\n", importDisplay(row.ID), status)
		for _, i := range row.Warnings {
			issue("warning", i)
		}
		for _, i := range row.Unresolved {
			issue("blocked", i)
		}
	}
	for _, i := range report.Issues {
		label := "blocked"
		if i.Code == "unused_binding" {
			label = "warning"
		}
		issue(label, i)
	}
	fmt.Fprintf(&b, "%d selected, %d applicable, %d blocked, %d omitted\n", selected, applicable, blocked, omitted)
	return b.String()
}
