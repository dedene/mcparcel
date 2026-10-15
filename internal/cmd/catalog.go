package cmd

import (
	"context"
	"fmt"

	"github.com/dedene/mcparcel/internal/args"
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
	next, source, err := registerSource(ctx, deps, store, state, request)
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
		service := catalog.Service{Store: store}
		next, err := service.Remove(ctx, c.Repository, state.Selections.Revision)
		if err != nil {
			return err
		}
		if !opts.JSON {
			return writeSuccess(s, opts, fmt.Sprintf("Removed %s.\nRevision: %d\n", output.DisplayMetadata(owner+"/"+repo), next.Selections.Revision))
		}
		return writeSuccess(s, opts, output.SourceMutationData{Revision: next.Selections.Revision, Source: source, Changed: true})
	}
	return output.SourceNotRegisteredError()
}

func (c *SyncCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions, deps *CatalogDependencies) error {
	if !c.Apply && len(c.Accept) > 0 {
		return args.ErrInvalidArgs
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	data, err := runSync(ctx, &catalog.Service{Store: store, Fetcher: deps.Fetcher}, state, c.Repository, c.Apply, c.Accept)
	if err != nil {
		return err
	}
	return writeSuccess(s, opts, data)
}
