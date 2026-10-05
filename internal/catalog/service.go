package catalog

import (
	"context"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
)

type Service struct {
	Store   *config.Store `json:"-"`
	Fetcher Fetcher       `json:"-"`
}

func SnapshotFromState(state config.State, sourceID string) (Snapshot, error) {
	for _, source := range state.Local.Sources {
		if source.ID != sourceID {
			continue
		}
		catalog, exists := state.Catalogs[sourceID]
		if !exists {
			return Snapshot{}, ErrRepositoryMissing
		}
		return validateSnapshot(Snapshot{Source: source, Catalog: catalog})
	}
	return Snapshot{}, ErrRepositoryMissing
}

func (s *Service) Register(ctx context.Context, candidate Snapshot, expectedRevision uint64) (config.State, error) {
	if s == nil || s.Store == nil {
		return config.State{}, config.ErrConfig
	}
	candidate, err := validateSnapshot(candidate)
	if err != nil {
		return config.State{}, err
	}
	return s.Store.Update(ctx, expectedRevision, func(draft *config.State) error {
		checked, err := validateSnapshot(candidate)
		if err != nil {
			return err
		}
		for _, source := range draft.Local.Sources {
			sameName := source.Owner == checked.Source.Owner && source.Repo == checked.Source.Repo
			if source.RepositoryID != checked.Source.RepositoryID {
				if sameName {
					return ErrRepositoryReused
				}
				continue
			}
			if !sameName {
				return ErrSourceRenamed
			}
			if source.Path != checked.Source.Path || source.Ref != checked.Source.Ref || source.Pinned != checked.Source.Pinned {
				return ErrSourceConflict
			}
			return nil
		}
		draft.Local.Sources = append(draft.Local.Sources, checked.Source)
		draft.Catalogs[checked.Source.ID] = checked.Catalog
		for id := range checked.Catalog.Connections {
			canonical, err := config.CanonicalGitHub(checked.Source.Owner, checked.Source.Repo, id)
			if err != nil {
				return ErrContentInvalid
			}
			selection := draft.Selections.Connections[canonical]
			if selection.Enabled {
				selection.ReviewRequired = true
			}
			selection.Enabled = false
			draft.Selections.Connections[canonical] = selection
		}
		if err := config.ValidateState(*draft); err != nil {
			return ErrLocalBindings
		}
		if _, err := config.Resolve(*draft); err != nil {
			return ErrLocalBindings
		}
		return nil
	})
}

func (s *Service) Remove(ctx context.Context, repository string, expectedRevision uint64) (config.State, error) {
	if s == nil || s.Store == nil {
		return config.State{}, config.ErrConfig
	}
	owner, repo, err := ValidateRepositoryName(repository)
	if err != nil {
		return config.State{}, err
	}
	return s.Store.Update(ctx, expectedRevision, func(draft *config.State) error {
		for i, source := range draft.Local.Sources {
			if source.Owner != owner || source.Repo != repo {
				continue
			}
			draft.Local.Sources = slices.Delete(draft.Local.Sources, i, i+1)
			delete(draft.Catalogs, source.ID)
			return nil
		}
		return ErrRepositoryMissing
	})
}

func (s *Service) ApplyUpdate(ctx context.Context, plan UpdatePlan, accept []string, expectedRevision uint64) error {
	_, err := s.ApplyUpdateState(ctx, plan, accept, expectedRevision)
	return err
}

func (s *Service) ApplyUpdateState(ctx context.Context, plan UpdatePlan, accept []string, expectedRevision uint64) (config.State, error) {
	if s == nil || s.Store == nil {
		return config.State{}, config.ErrConfig
	}
	checked, err := PlanUpdate(plan.Current, plan.Candidate)
	if err != nil {
		return config.State{}, err
	}
	accept = slices.Clone(accept)
	seen := map[string]bool{}
	prefix := "github:" + checked.Current.Source.Owner + "/" + checked.Current.Source.Repo + "#"
	for _, id := range accept {
		if config.ValidateCanonicalID(id) != nil || seen[id] {
			return config.State{}, config.ErrConfig
		}
		seen[id] = true
		name, belongs := strings.CutPrefix(id, prefix)
		_, before := checked.Current.Catalog.Connections[name]
		_, after := checked.Candidate.Catalog.Connections[name]
		if !belongs || !before || !after {
			return config.State{}, config.ErrNotFound
		}
	}
	return s.Store.UpdateAccepted(ctx, expectedRevision, accept, func(draft *config.State) error {
		return stageCandidate(draft, checked)
	})
}
