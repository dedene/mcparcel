package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/output"

	"github.com/dedene/mcparcel/internal/config"
)

type CatalogCmd struct {
	Domain string `name:"domain" json:"-"`
}
type (
	ListCmd    struct{}
	InspectCmd struct {
		MCP string `arg:"" required:"" json:"-"`
	}
)

func (c *CatalogCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	return writeMetadata(ctx, s, opts, false, c.Domain)
}

func (c *ListCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	return writeMetadata(ctx, s, opts, true, "")
}

func (c *InspectCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	state, effective, sources, err := readMetadata(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(effective.Connections))
	for id := range effective.Connections {
		ids = append(ids, id)
	}
	id, err := config.ResolveID(c.MCP, effective.Aliases, ids)
	if err != nil {
		return err
	}
	data := inspectMetadata(state, effective, sources, id)
	return writeSuccess(s, opts, data)
}

type AddCmd struct {
	Repository string `arg:"" required:"" json:"-"`
	Path       string `name:"path" json:"-"`
	Ref        string `name:"ref" json:"-"`
}
type RemoveCmd struct {
	Repository string `arg:"" required:"" json:"-"`
	DryRun     bool   `name:"dry-run" json:"-" help:"Show what would change without saving."`
}
type SyncCmd struct {
	Repository string   `arg:"" optional:"" json:"-"`
	Apply      bool     `name:"apply" json:"-"`
	Accept     []string `name:"accept" sep:"none" json:"-"`
}
type CatalogDependencies struct {
	Fetcher catalog.Fetcher `json:"-"`
}

func (c *AddCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions, deps *CatalogDependencies) error {
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	request, _, err := sourceRequest(state, c.Repository, c.Path, c.Ref)
	if err != nil {
		return err
	}
	waiting := startProgress(s, opts, fetchLabel(request))
	next, source, err := registerSource(ctx, deps, store, state, request)
	waiting.stop()
	if err != nil {
		return err
	}
	return writeSuccess(s, opts, output.SourceMutationData{Revision: next.Selections.Revision, Source: source, Changed: next.Selections.Revision != state.Selections.Revision})
}

// sourceRequest is the source add and join ask for: the registered one for
// owner/repo, when there is one, else a new one with the default path.
// --path or --ref that differ from the registered source conflict.
func sourceRequest(state config.State, repository, path, ref string) (config.Source, bool, error) {
	owner, repo, err := catalog.ValidateRepositoryName(repository)
	if err != nil {
		return config.Source{}, false, output.NewError("invalid_repository", nil)
	}
	for _, source := range state.Local.Sources {
		if source.Owner == owner && source.Repo == repo {
			if path != "" && path != source.Path || ref != "" && ref != source.Ref {
				return config.Source{}, false, catalog.ErrSourceConflict
			}
			return source, true, nil
		}
	}
	request := config.Source{Owner: owner, Repo: repo, Path: path, Ref: ref}
	if request.Path == "" {
		request.Path = "mcparcel.json"
	}
	return request, false, nil
}

// fetchLabel is the progress line while source's catalog is fetched: its
// owner/repo, never its path or ref.
func fetchLabel(source config.Source) string {
	return "Fetching " + output.DisplayMetadata(source.Owner+"/"+source.Repo) + ellipsis()
}

// registerSource fetches request and registers it, as add does. A source
// that is already registered stays as it is (no new snapshot). It returns
// the state after and the registered source.
func registerSource(ctx context.Context, deps *CatalogDependencies, store *config.Store, state config.State, request config.Source) (config.State, config.Source, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, catalog.FetchTimeout)
	defer cancel()
	candidate, err := deps.Fetcher.Fetch(fetchCtx, request)
	if err != nil {
		return config.State{}, config.Source{}, err
	}
	service := catalog.Service{Store: store, Fetcher: deps.Fetcher}
	next, err := service.Register(ctx, candidate, state.Selections.Revision)
	if err != nil {
		return config.State{}, config.Source{}, err
	}
	for _, source := range next.Local.Sources {
		if source.ID == candidate.Source.ID {
			return next, source, nil
		}
	}
	return config.State{}, config.Source{}, catalog.ErrRepositoryMissing
}

func (c *RemoveCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	if c.DryRun {
		if err := refuseReadOnly(); err != nil {
			return err
		}
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	owner, repo, err := catalog.ValidateRepositoryName(c.Repository)
	if err != nil {
		return output.NewError("invalid_repository", nil)
	}
	for _, source := range state.Local.Sources {
		if source.Owner != owner || source.Repo != repo {
			continue
		}
		repository := output.DisplayMetadata(owner + "/" + repo)
		changes, on := sourceChanges(state, source)
		if c.DryRun {
			summary := "Would remove " + repository + ". None of its connections are on.\n"
			if len(on) > 0 {
				summary = fmt.Sprintf("Would remove %s. %s %s on and would become unavailable.\n", repository, joinNames(on), isAre(on))
			}
			return writePlan(s, opts, output.PlanData{Revision: state.Selections.Revision, Source: &source, Changes: changes}, summary)
		}
		service := catalog.Service{Store: store}
		next, err := service.Remove(ctx, c.Repository, state.Selections.Revision)
		if err != nil {
			return err
		}
		if !opts.JSON {
			summary := "Removed " + repository + ".\n"
			if len(on) > 0 {
				summary += fmt.Sprintf("%s %s on and %s unavailable now.\n", joinNames(on), wasWere(on), isAre(on))
			}
			return writeSuccess(s, opts, summary)
		}
		return writeSuccess(s, opts, output.SourceMutationData{Revision: next.Selections.Revision, Source: source, Changed: true})
	}
	return output.SourceNotRegisteredError()
}

// sourceChanges is what removing source does to its connections, sorted by
// ID: one that is on becomes unavailable (its selection stays), any other
// goes. on names the ones that are on, for the human output.
func sourceChanges(state config.State, source config.Source) ([]output.PlanChange, []string) {
	effective, err := config.Resolve(state)
	if err != nil {
		return nil, nil
	}
	name := connectionNamer(state)
	var changes []output.PlanChange
	var on []string
	for _, row := range effective.Connections {
		if row.SourceID != source.ID {
			continue
		}
		change := output.PlanChange{Connection: row.ID, Change: "remove", Enabled: row.Enabled}
		if row.Enabled {
			change.Change = "unavailable"
		}
		changes = append(changes, change)
	}
	slices.SortFunc(changes, func(a, b output.PlanChange) int { return strings.Compare(a.Connection, b.Connection) })
	for _, change := range changes {
		if change.Enabled {
			on = append(on, name(change.Connection, ""))
		}
	}
	return changes, on
}

func isAre(items []string) string {
	if len(items) == 1 {
		return "is"
	}
	return "are"
}

func (c *SyncCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions, deps *CatalogDependencies) error {
	if !c.Apply && len(c.Accept) > 0 {
		return output.UsageError("sync", "The --accept flag needs --apply.", "--accept", "")
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	waiting := startProgress(s, opts, "")
	fetching := func(source config.Source) { waiting.set(fetchLabel(source)) }
	data, err := runSync(ctx, &catalog.Service{Store: store, Fetcher: deps.Fetcher}, state, c.Repository, c.Apply, c.Accept, fetching)
	waiting.stop()
	if err != nil {
		return err
	}
	return writeSuccess(s, opts, data)
}
