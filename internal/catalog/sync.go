package catalog

import (
	"encoding/json"
	"reflect"
	"slices"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/jsonutil"
)

type ConnectionChange struct {
	ID              string   `json:"id"`
	Fields          []string `json:"fields"`
	ExecutionOrAuth bool     `json:"executionOrAuth"`
}
type UpdatePlan struct {
	SourceID        string             `json:"sourceId"`
	Repository      string             `json:"repository"`
	CurrentCommit   string             `json:"currentCommit"`
	CandidateCommit string             `json:"candidateCommit"`
	Pinned          bool               `json:"pinned"`
	Added           []string           `json:"added"`
	Removed         []string           `json:"removed"`
	Changed         []ConnectionChange `json:"changed"`
	CatalogFields   []string           `json:"catalogFields"`
	Current         Snapshot           `json:"-"`
	Candidate       Snapshot           `json:"-"`
}

func stageCandidate(draft *config.State, plan UpdatePlan) error {
	current, err := SnapshotFromState(*draft, plan.SourceID)
	if err != nil {
		return config.ErrConfigConflict
	}
	if !reflect.DeepEqual(current.Source, plan.Current.Source) ||
		!reflect.DeepEqual(current.Catalog, plan.Current.Catalog) {
		return config.ErrConfigConflict
	}
	checked, err := PlanUpdate(current, plan.Candidate)
	if err != nil {
		return err
	}
	for i := range draft.Local.Sources {
		if draft.Local.Sources[i].ID == checked.SourceID {
			draft.Local.Sources[i] = checked.Candidate.Source
		}
	}
	draft.Catalogs[checked.SourceID] = checked.Candidate.Catalog
	for _, id := range checked.Added {
		selection, existed := draft.Selections.Connections[id]
		if existed && selection.Enabled {
			selection.ReviewRequired = true
		}
		selection.Enabled = false
		draft.Selections.Connections[id] = selection
	}
	for _, change := range checked.Changed {
		selection := draft.Selections.Connections[change.ID]
		if selection.Enabled && change.ExecutionOrAuth {
			selection.ReviewRequired = true
			draft.Selections.Connections[change.ID] = selection
		}
	}
	if err := config.ValidateState(*draft); err != nil {
		return ErrLocalBindings
	}
	if _, err := config.Resolve(*draft); err != nil {
		return ErrLocalBindings
	}
	return nil
}

func PlanUpdate(current, candidate Snapshot) (UpdatePlan, error) {
	current, err := validateSnapshot(current)
	if err != nil {
		return UpdatePlan{}, err
	}
	candidate, err = validateSnapshot(candidate)
	if err != nil {
		return UpdatePlan{}, err
	}
	a, b := current.Source, candidate.Source
	if a.RepositoryID != b.RepositoryID || a.ID != b.ID {
		return UpdatePlan{}, ErrRepositoryReused
	}
	if a.Owner != b.Owner || a.Repo != b.Repo {
		return UpdatePlan{}, ErrSourceRenamed
	}
	if a.Path != b.Path || a.Ref != b.Ref || a.Pinned != b.Pinned || a.Pinned && a.Commit != b.Commit {
		return UpdatePlan{}, ErrSourceConflict
	}
	if _, err = sameRevision(current, candidate); err != nil {
		return UpdatePlan{}, err
	}
	plan := UpdatePlan{
		SourceID: a.ID, Repository: a.Owner + "/" + a.Repo, CurrentCommit: a.Commit, CandidateCommit: b.Commit, Pinned: b.Pinned,
		Added: []string{}, Removed: []string{}, Changed: []ConnectionChange{}, CatalogFields: []string{}, Current: current, Candidate: candidate,
	}
	keys := []string{}
	for id, before := range current.Catalog.Connections {
		canonical, e := config.CanonicalGitHub(a.Owner, a.Repo, id)
		if e != nil {
			return UpdatePlan{}, ErrContentInvalid
		}
		after, exists := candidate.Catalog.Connections[id]
		if !exists {
			plan.Removed = append(plan.Removed, canonical)
			continue
		}
		change, e := connectionChange(canonical, before, after)
		if e != nil {
			return UpdatePlan{}, e
		}
		if len(change.Fields) > 0 {
			plan.Changed = append(plan.Changed, change)
		}
	}
	for id := range candidate.Catalog.Connections {
		if _, exists := current.Catalog.Connections[id]; !exists {
			keys = append(keys, id)
		}
	}
	for _, id := range keys {
		canonical, e := config.CanonicalGitHub(a.Owner, a.Repo, id)
		if e != nil {
			return UpdatePlan{}, ErrContentInvalid
		}
		plan.Added = append(plan.Added, canonical)
	}
	metadata := func(c config.Catalog) (any, error) {
		raw, e := json.Marshal(c)
		if e != nil {
			return nil, ErrContentInvalid
		}
		v, e := jsonutil.Decode(raw)
		if e != nil {
			return nil, ErrContentInvalid
		}
		obj := v.(map[string]any)
		delete(obj, "connections")
		return obj, nil
	}
	before, err := metadata(current.Catalog)
	if err != nil {
		return UpdatePlan{}, err
	}
	after, err := metadata(candidate.Catalog)
	if err != nil {
		return UpdatePlan{}, err
	}
	plan.CatalogFields = changedPaths(before, after, "")
	slices.Sort(plan.Added)
	slices.Sort(plan.Removed)
	slices.SortFunc(plan.Changed, func(a, b ConnectionChange) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return plan, nil
}
