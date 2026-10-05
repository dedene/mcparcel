package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

const (
	servicePaper   = "github:fixture-owner/fixture.repo#paper"
	serviceRetired = "github:fixture-owner/fixture.repo#retired"
)

type forbiddenFetcher struct{}

func (forbiddenFetcher) Fetch(context.Context, config.Source) (catalog.Snapshot, error) {
	panic("offline service called Fetch")
}

func serviceRig(t *testing.T) (*catalog.Service, config.Paths) {
	t.Helper()
	p, _ := testutil.IsolatedPaths(t)
	return &catalog.Service{Store: config.NewStore(p), Fetcher: forbiddenFetcher{}}, p
}

func serviceRead(t *testing.T, s *catalog.Service) config.State {
	t.Helper()
	state, err := s.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func serviceSeed(t *testing.T, s *catalog.Service, base catalog.Snapshot) config.State {
	t.Helper()
	state, err := s.Register(context.Background(), base, 0)
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.Store.Update(context.Background(), state.Selections.Revision, func(draft *config.State) error {
		for _, id := range []string{servicePaper, serviceRetired} {
			selection := draft.Selections.Connections[id]
			selection.Enabled = true
			draft.Selections.Connections[id] = selection
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func servicePlan(t *testing.T, state config.State, candidate catalog.Snapshot) catalog.UpdatePlan {
	t.Helper()
	current, err := catalog.SnapshotFromState(state, candidate.Source.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := catalog.PlanUpdate(current, candidate)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func serviceFiles(t *testing.T, p config.Paths) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, root := range []string{p.ConfigDir, p.DataDir} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] = string(raw)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func TestAddIdempotent(t *testing.T) {
	s, p := serviceRig(t)
	a := fixtureSnapshot(t, "stage4-base", commitA)
	first, err := s.Register(context.Background(), a, 0)
	if err != nil || first.Selections.Revision != 1 || len(first.Local.Sources) != 1 {
		t.Fatal(first, err)
	}
	before := serviceFiles(t, p)
	for _, candidate := range []catalog.Snapshot{a, fixtureSnapshot(t, "stage4-next", commitB)} {
		state, err := s.Register(context.Background(), candidate, 1)
		if err != nil || !reflect.DeepEqual(first, state) || !reflect.DeepEqual(before, serviceFiles(t, p)) {
			t.Fatal(state, err)
		}
	}
	if first.Local.Sources[0].Commit != commitA || len(first.Local.Aliases) != 0 {
		t.Fatal(first)
	}
}

func TestServiceStoreRequiredAndRegistrationOrder(t *testing.T) {
	a := fixtureSnapshot(t, "stage4-base", commitA)
	missing := &catalog.Service{}
	if _, err := missing.Register(context.Background(), a, 0); !errors.Is(err, config.ErrConfig) {
		t.Fatal(err)
	}
	if _, err := missing.Remove(context.Background(), "fixture-owner/fixture.repo", 0); !errors.Is(err, config.ErrConfig) {
		t.Fatal(err)
	}
	if err := missing.ApplyUpdate(context.Background(), catalog.UpdatePlan{}, nil, 0); !errors.Is(err, config.ErrConfig) {
		t.Fatal(err)
	}
	s, _ := serviceRig(t)
	s.Fetcher = nil
	state, err := s.Register(context.Background(), a, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := fixtureSnapshot(t, "stage4-base", commitA)
	b.Source.ID, b.Source.RepositoryID, b.Source.Repo = "github-43", 43, "other"
	state, err = s.Register(context.Background(), b, state.Selections.Revision)
	if err != nil || len(state.Local.Sources) != 2 || state.Local.Sources[0].ID != "github-42" || state.Local.Sources[1].ID != "github-43" {
		t.Fatal(state, err)
	}
}

func TestRegisterSourceConflict(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*config.Source)
		want error
	}{
		{"path", func(s *config.Source) { s.Path = "other.json" }, catalog.ErrSourceConflict},
		{"ref", func(s *config.Source) { s.Ref = "other" }, catalog.ErrSourceConflict},
		{"pin", func(s *config.Source) { s.Pinned = true }, catalog.ErrSourceConflict},
		{"reused", func(s *config.Source) { s.ID = "github-43"; s.RepositoryID = 43 }, catalog.ErrRepositoryReused},
		{"renamed", func(s *config.Source) { s.Repo = "other" }, catalog.ErrSourceRenamed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := serviceRig(t)
			a := fixtureSnapshot(t, "stage4-base", commitA)
			state, err := s.Register(context.Background(), a, 0)
			if err != nil {
				t.Fatal(err)
			}
			before := serviceFiles(t, p)
			b := fixtureSnapshot(t, "stage4-next", commitB)
			tc.edit(&b.Source)
			_, err = s.Register(context.Background(), b, state.Selections.Revision)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(before, serviceFiles(t, p)) || !reflect.DeepEqual(state, serviceRead(t, s)) {
				t.Fatal(err)
			}
		})
	}
}

func TestDisabledNewEntry(t *testing.T) {
	s, _ := serviceRig(t)
	state := serviceSeed(t, s, fixtureSnapshot(t, "stage4-base", commitA))
	plan := servicePlan(t, state, fixtureSnapshot(t, "stage4-next", commitB))
	if err := s.ApplyUpdate(context.Background(), plan, nil, state.Selections.Revision); err != nil {
		t.Fatal(err)
	}
	state = serviceRead(t, s)
	effective, err := config.Resolve(state)
	newID := "github:fixture-owner/fixture.repo#newcomer"
	if err != nil || !effective.Connections[newID].Available || effective.Connections[newID].Enabled || !effective.Connections[servicePaper].Enabled || !effective.Connections[servicePaper].ReviewRequired || !effective.Connections[serviceRetired].Enabled || effective.Connections[serviceRetired].Available {
		t.Fatal(effective, err)
	}
}

func TestRemovedEntryUnavailable(t *testing.T) {
	s, p := serviceRig(t)
	state := serviceSeed(t, s, fixtureSnapshot(t, "stage4-base", commitA))
	state, err := s.Store.Update(context.Background(), state.Selections.Revision, func(draft *config.State) error {
		draft.Local.Aliases["paper"] = servicePaper
		draft.Local.Runtime = &config.RuntimeDefaults{KeepAlive: true}
		selection := draft.Selections.Connections[serviceRetired]
		selection.Enabled = false
		selection.DisabledTools = []string{"write"}
		draft.Selections.Connections[serviceRetired] = selection
		draft.Personal.Connections["own"] = config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.DataDir, "catalogs", "github-42", commitA+".json")
	snapshotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := s.Remove(context.Background(), "Fixture-Owner/Fixture.Repo", state.Selections.Revision)
	if err != nil || len(removed.Local.Sources) != 0 || len(removed.Catalogs) != 0 || !reflect.DeepEqual(removed.Selections.Connections, state.Selections.Connections) || !reflect.DeepEqual(removed.Local.Aliases, state.Local.Aliases) || !reflect.DeepEqual(removed.Personal, state.Personal) || !reflect.DeepEqual(removed.Local.CredentialProfiles, state.Local.CredentialProfiles) || !reflect.DeepEqual(removed.Local.Runtime, state.Local.Runtime) {
		t.Fatal(removed, err)
	}
	afterBytes, err := os.ReadFile(path)
	if err != nil || string(snapshotBytes) != string(afterBytes) {
		t.Fatal(err)
	}
	loaded, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loaded.RuntimeConnection("paper"); !errors.Is(err, config.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Remove(context.Background(), "fixture-owner/fixture.repo", removed.Selections.Revision); !errors.Is(err, catalog.ErrRepositoryMissing) {
		t.Fatal(err)
	}
}

func TestChangedExecutionNeedsAcceptance(t *testing.T) {
	s, p := serviceRig(t)
	state := serviceSeed(t, s, fixtureSnapshot(t, "stage4-base", commitA))
	b := fixtureSnapshot(t, "stage4-next", commitB)
	state, err := s.ApplyUpdateState(context.Background(), servicePlan(t, state, b), nil, state.Selections.Revision)
	if err != nil || state.Selections.Revision != 3 || !state.Selections.Connections[servicePaper].ReviewRequired {
		t.Fatal(state, err)
	}
	loaded, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loaded.Connection(servicePaper); !errors.Is(err, config.ErrReviewRequired) {
		t.Fatal(err)
	}
	state, err = s.ApplyUpdateState(context.Background(), servicePlan(t, state, b), []string{servicePaper}, 3)
	if err != nil || state.Selections.Revision != 4 || state.Selections.Connections[servicePaper].ReviewRequired || !state.Selections.Connections[servicePaper].Enabled {
		t.Fatal(state, err)
	}
	loaded, err = config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loaded.Connection(servicePaper); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptionOnlyApply(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(map[bool]string{false: "clear", true: "marked"}[marked], func(t *testing.T) {
			s, _ := serviceRig(t)
			state := serviceSeed(t, s, fixtureSnapshot(t, "stage4-base", commitA))
			if marked {
				var err error
				state, err = s.Store.Update(context.Background(), state.Selections.Revision, func(draft *config.State) error {
					selection := draft.Selections.Connections[servicePaper]
					selection.ReviewRequired = true
					draft.Selections.Connections[servicePaper] = selection
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			state, err := s.ApplyUpdateState(context.Background(), servicePlan(t, state, fixtureSnapshot(t, "stage4-description", commitB)), nil, state.Selections.Revision)
			if err != nil || !state.Selections.Connections[servicePaper].Enabled || state.Selections.Connections[servicePaper].ReviewRequired != marked || state.Catalogs["github-42"].Connections["paper"].Description != "Revised" {
				t.Fatal(state, err)
			}
		})
	}
}

func boundServiceSnapshot(t *testing.T) catalog.Snapshot {
	t.Helper()
	a := fixtureSnapshot(t, "stage4-base", commitA)
	a.Catalog.CredentialProfiles["team"] = config.ProfileRequirement{}
	c := a.Catalog.Connections["paper"]
	c.CredentialProfile = "team"
	c.Inputs = map[string]config.Input{"argument": {Kind: "string", Description: "Argument"}}
	c.Transport.HTTP.Headers = map[string]config.Value{"X-Fixture": {Input: &config.InputRef{Input: "argument"}}}
	a.Catalog.Connections["paper"] = c
	return a
}

func seedBoundService(t *testing.T, s *catalog.Service, base catalog.Snapshot) config.State {
	t.Helper()
	state, err := s.Register(context.Background(), base, 0)
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.Store.Update(context.Background(), state.Selections.Revision, func(draft *config.State) error {
		draft.Local.CredentialProfiles["work"] = config.Profile{Mode: "desktop", Account: "Fixture"}
		draft.Local.Aliases["mine"] = "local:own"
		draft.Personal.Connections["own"] = config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}}}
		draft.Selections.Connections["local:own"] = config.Selection{Enabled: true}
		draft.Selections.Connections[servicePaper] = config.Selection{Enabled: true, Inputs: map[string]string{"argument": "relative"}, CredentialProfile: "work", DisabledTools: []string{"write"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestSyncPreservesBindings(t *testing.T) {
	s, _ := serviceRig(t)
	a := boundServiceSnapshot(t)
	state := seedBoundService(t, s, a)
	b := editedSnapshot(t, a, []string{"connections", "paper", "transport", "url"}, "https://changed.example.invalid/mcp", false)
	next, err := s.ApplyUpdateState(context.Background(), servicePlan(t, state, b), nil, state.Selections.Revision)
	if err != nil {
		t.Fatal(err)
	}
	beforeSelection, afterSelection := state.Selections.Connections[servicePaper], next.Selections.Connections[servicePaper]
	beforeSelection.ReviewRequired = true
	if !reflect.DeepEqual(beforeSelection, afterSelection) || !reflect.DeepEqual(state.Personal, next.Personal) || !reflect.DeepEqual(state.Local, nextLocalAtCommit(next, commitA)) || !reflect.DeepEqual(state.Selections.Connections["local:own"], next.Selections.Connections["local:own"]) {
		t.Fatal(next)
	}
}

func nextLocalAtCommit(state config.State, commit string) config.Local {
	state.Local.Sources = append([]config.Source(nil), state.Local.Sources...)
	state.Local.Sources[0].Commit = commit
	return state.Local
}

func TestSyncIncompatibleBindings(t *testing.T) {
	for _, kind := range []string{"input", "requirement", "kind"} {
		t.Run(kind, func(t *testing.T) {
			s, p := serviceRig(t)
			a := boundServiceSnapshot(t)
			state := seedBoundService(t, s, a)
			other := fixtureSnapshot(t, "stage4-base", commitA)
			other.Source.ID, other.Source.RepositoryID, other.Source.Repo = "github-43", 43, "other"
			var err error
			state, err = s.Register(context.Background(), other, state.Selections.Revision)
			if err != nil {
				t.Fatal(err)
			}
			b, err := cloneServiceSnapshot(a)
			if err != nil {
				t.Fatal(err)
			}
			b.Source.Commit = commitB
			c := b.Catalog.Connections["paper"]
			switch kind {
			case "input":
				delete(c.Inputs, "argument")
				c.Transport.HTTP.Headers = nil
			case "requirement":
				c.CredentialProfile = ""
				delete(b.Catalog.CredentialProfiles, "team")
			case "kind":
				c.Inputs["argument"] = config.Input{Kind: "path", Description: "Argument"}
			}
			b.Catalog.Connections["paper"] = c
			before := serviceFiles(t, p)
			_, err = s.ApplyUpdateState(context.Background(), servicePlan(t, state, b), nil, state.Selections.Revision)
			if !errors.Is(err, catalog.ErrLocalBindings) || !reflect.DeepEqual(before, serviceFiles(t, p)) || !reflect.DeepEqual(state, serviceRead(t, s)) {
				t.Fatal(err)
			}
		})
	}
}

func TestReaddDoesNotInheritSelection(t *testing.T) {
	s, _ := serviceRig(t)
	a := boundServiceSnapshot(t)
	state := seedBoundService(t, s, a)
	removed, err := s.Remove(context.Background(), "fixture-owner/fixture.repo", state.Selections.Revision)
	if err != nil {
		t.Fatal(err)
	}
	a.Source.RepositoryID, a.Source.ID = 43, "github-43"
	next, err := s.Register(context.Background(), a, removed.Selections.Revision)
	if err != nil {
		t.Fatal(err)
	}
	want := state.Selections.Connections[servicePaper]
	want.Enabled, want.ReviewRequired = false, true
	if !reflect.DeepEqual(want, next.Selections.Connections[servicePaper]) || next.Local.Sources[0].ID != "github-43" {
		t.Fatal(next)
	}
}

func TestConcurrentSourceUpdates(t *testing.T) {
	s, p := serviceRig(t)
	state := serviceSeed(t, s, fixtureSnapshot(t, "stage4-base", commitA))
	b := fixtureSnapshot(t, "stage4-next", commitB)
	c := fixtureSnapshot(t, "stage4-description", strings.Repeat("c", 40))
	type result struct {
		state config.State
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, candidate := range []catalog.Snapshot{b, c} {
		plan := servicePlan(t, state, candidate)
		go func() {
			<-start
			service := &catalog.Service{Store: config.NewStore(p)}
			state, err := service.ApplyUpdateState(context.Background(), plan, nil, 2)
			results <- result{state, err}
		}()
	}
	close(start)
	winner, loser := <-results, <-results
	if winner.err != nil {
		winner, loser = loser, winner
	}
	if winner.err != nil || winner.state.Selections.Revision != 3 || !errors.Is(loser.err, config.ErrConfigConflict) || !reflect.DeepEqual(winner.state, serviceRead(t, s)) {
		t.Fatal(winner, loser)
	}
	commit := winner.state.Local.Sources[0].Commit
	raw, err := os.ReadFile(filepath.Join(p.DataDir, "catalogs", "github-42", commit+".json"))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := config.DecodeCatalog(raw)
	if err != nil || !reflect.DeepEqual(cat, winner.state.Catalogs["github-42"]) {
		t.Fatal(cat, err)
	}
}

func TestApplyRevalidatesPlan(t *testing.T) {
	for _, kind := range []string{"forged-diff", "forged-current", "hand-edit", "tampered-candidate"} {
		t.Run(kind, func(t *testing.T) {
			s, p := serviceRig(t)
			state := serviceSeed(t, s, fixtureSnapshot(t, "stage4-base", commitA))
			plan := servicePlan(t, state, fixtureSnapshot(t, "stage4-next", commitB))
			want := error(nil)
			switch kind {
			case "forged-diff":
				plan.Changed, plan.Added, plan.Removed = nil, nil, nil
			case "forged-current":
				c := plan.Current.Catalog.Connections["paper"]
				c.Description = "forged"
				plan.Current.Catalog.Connections["paper"] = c
				want = config.ErrConfigConflict
			case "hand-edit":
				cat := state.Catalogs["github-42"]
				c := cat.Connections["paper"]
				c.Description = "hand edited"
				cat.Connections["paper"] = c
				if err := os.WriteFile(filepath.Join(p.DataDir, "catalogs", "github-42", commitA+".json"), encoded(t, cat), 0o600); err != nil {
					t.Fatal(err)
				}
				want = config.ErrConfigConflict
			case "tampered-candidate":
				plan.Candidate.Source.Commit = commitA
				want = catalog.ErrContentInvalid
			}
			before := serviceFiles(t, p)
			next, err := s.ApplyUpdateState(context.Background(), plan, nil, 2)
			if want != nil {
				if !errors.Is(err, want) || !reflect.DeepEqual(before, serviceFiles(t, p)) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || !next.Selections.Connections[servicePaper].ReviewRequired || next.Selections.Connections["github:fixture-owner/fixture.repo#newcomer"].Enabled {
				t.Fatal(next, err)
			}
		})
	}
}

func TestApplyAcceptanceScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []string
		want error
	}{
		{"alias", []string{"paper"}, config.ErrConfig},
		{"duplicate", []string{servicePaper, servicePaper}, config.ErrConfig},
		{"absent", []string{"github:fixture-owner/fixture.repo#missing"}, config.ErrNotFound},
		{"other-source", []string{"local:own"}, config.ErrNotFound},
		{"removed", []string{serviceRetired}, config.ErrNotFound},
		{"added", []string{"github:fixture-owner/fixture.repo#newcomer"}, config.ErrNotFound},
		{"disabled", []string{servicePaper}, config.ErrDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := serviceRig(t)
			state := serviceSeed(t, s, fixtureSnapshot(t, "stage4-base", commitA))
			if tc.name == "disabled" {
				var err error
				state, err = s.Store.Update(context.Background(), state.Selections.Revision, func(draft *config.State) error {
					selection := draft.Selections.Connections[servicePaper]
					selection.Enabled = false
					draft.Selections.Connections[servicePaper] = selection
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := serviceFiles(t, p)
			err := s.ApplyUpdate(context.Background(), servicePlan(t, state, fixtureSnapshot(t, "stage4-next", commitB)), tc.ids, state.Selections.Revision)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(before, serviceFiles(t, p)) {
				t.Fatal(err)
			}
		})
	}
}

func TestApplyNoopAndCommit(t *testing.T) {
	s, p := serviceRig(t)
	s.Fetcher = nil
	a := fixtureSnapshot(t, "stage4-base", commitA)
	state := serviceSeed(t, s, a)
	before := serviceFiles(t, p)
	info, err := os.Stat(p.SelectionsFile)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.ApplyUpdateState(context.Background(), servicePlan(t, state, a), nil, state.Selections.Revision)
	after, statErr := os.Stat(p.SelectionsFile)
	if err != nil || statErr != nil || !os.SameFile(info, after) || !reflect.DeepEqual(state, next) || !reflect.DeepEqual(before, serviceFiles(t, p)) {
		t.Fatal(next, err, statErr)
	}
	a.Source.Commit = commitB
	next, err = s.ApplyUpdateState(context.Background(), servicePlan(t, state, a), nil, state.Selections.Revision)
	if err != nil || next.Selections.Revision != 3 || next.Local.Sources[0].Commit != commitB || !reflect.DeepEqual(state.Selections.Connections, next.Selections.Connections) {
		t.Fatal(next, err)
	}
}

func TestApplyRetainedSelection(t *testing.T) {
	s, _ := serviceRig(t)
	a := fixtureSnapshot(t, "stage4-base", commitA)
	a.Catalog.Connections["newcomer"] = config.Connection{
		Inputs:    map[string]config.Input{"argument": {Kind: "string", Description: "Argument"}},
		Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}},
	}
	state := serviceSeed(t, s, a)
	newID := "github:fixture-owner/fixture.repo#newcomer"
	state, err := s.Store.Update(context.Background(), state.Selections.Revision, func(draft *config.State) error {
		draft.Selections.Connections[newID] = config.Selection{Enabled: true, Inputs: map[string]string{"argument": "retained"}, DisabledTools: []string{"write"}}
		selection := draft.Selections.Connections[serviceRetired]
		selection.ReviewRequired = true
		draft.Selections.Connections[serviceRetired] = selection
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.ApplyUpdateState(context.Background(), servicePlan(t, state, fixtureSnapshot(t, "stage4-base", commitB)), nil, state.Selections.Revision)
	if err != nil {
		t.Fatal(err)
	}
	b := fixtureSnapshot(t, "stage4-next", strings.Repeat("c", 40))
	c := b.Catalog.Connections["newcomer"]
	c.Inputs = map[string]config.Input{"argument": {Kind: "string", Description: "Argument"}}
	b.Catalog.Connections["newcomer"] = c
	next, err := s.ApplyUpdateState(context.Background(), servicePlan(t, state, b), []string{servicePaper}, state.Selections.Revision)
	want := state.Selections.Connections[newID]
	want.Enabled, want.ReviewRequired = false, true
	if err != nil || !reflect.DeepEqual(want, next.Selections.Connections[newID]) || !reflect.DeepEqual(state.Selections.Connections[serviceRetired], next.Selections.Connections[serviceRetired]) || next.Selections.Connections[servicePaper].ReviewRequired {
		t.Fatal(next, err)
	}
}

func TestApplyMissingNewInput(t *testing.T) {
	s, p := serviceRig(t)
	state := serviceSeed(t, s, fixtureSnapshot(t, "stage4-base", commitA))
	b := fixtureSnapshot(t, "stage4-description", commitB)
	c := b.Catalog.Connections["paper"]
	c.Inputs = map[string]config.Input{"new": {Kind: "string", Description: "New requirement"}}
	b.Catalog.Connections["paper"] = c
	next, err := s.ApplyUpdateState(context.Background(), servicePlan(t, state, b), nil, state.Selections.Revision)
	if err != nil || !next.Selections.Connections[servicePaper].Enabled || !next.Selections.Connections[servicePaper].ReviewRequired {
		t.Fatal(next, err)
	}
	effective, err := config.Resolve(next)
	if err != nil || !slices.Contains(effective.Connections[servicePaper].Blockers, "config_required") {
		t.Fatal(effective, err)
	}
	before := serviceFiles(t, p)
	err = s.ApplyUpdate(context.Background(), servicePlan(t, next, b), []string{servicePaper}, next.Selections.Revision)
	if !errors.Is(err, config.ErrConfigRequired) || !reflect.DeepEqual(before, serviceFiles(t, p)) {
		t.Fatal(err)
	}
}

func TestSnapshotFromStateCopies(t *testing.T) {
	s, _ := serviceRig(t)
	state := serviceSeed(t, s, fixtureSnapshot(t, "stage4-base", commitA))
	before := encoded(t, state.Catalogs["github-42"])
	snapshot, err := catalog.SnapshotFromState(state, "github-42")
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Catalog.Connections["paper"].Transport.HTTP.URL = config.Literal("https://changed.invalid")
	if string(before) != string(encoded(t, state.Catalogs["github-42"])) {
		t.Fatal("snapshot aliases state")
	}
	if _, err := catalog.SnapshotFromState(state, "missing"); !errors.Is(err, catalog.ErrRepositoryMissing) {
		t.Fatal(err)
	}
}

func TestRegisterIncompatibleOrphan(t *testing.T) {
	s, p := serviceRig(t)
	a := boundServiceSnapshot(t)
	state := seedBoundService(t, s, a)
	state, err := s.Remove(context.Background(), "fixture-owner/fixture.repo", state.Selections.Revision)
	if err != nil {
		t.Fatal(err)
	}
	before := serviceFiles(t, p)
	c := a.Catalog.Connections["paper"]
	c.CredentialProfile = ""
	a.Catalog.Connections["paper"] = c
	a.Source.ID, a.Source.RepositoryID = "github-43", 43
	_, err = s.Register(context.Background(), a, state.Selections.Revision)
	if !errors.Is(err, catalog.ErrLocalBindings) || !reflect.DeepEqual(before, serviceFiles(t, p)) || !reflect.DeepEqual(state, serviceRead(t, s)) {
		t.Fatal(err)
	}
}

const (
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func fixtureSnapshot(t *testing.T, name, commit string) catalog.Snapshot {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/catalogs/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.DecodeCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	source := config.Source{ID: "github-42", RepositoryID: 42, Owner: "fixture-owner", Repo: "fixture.repo", Path: "mcparcel.json", Ref: "main", Commit: commit}
	return catalog.Snapshot{Source: source, Catalog: c}
}

func encoded(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func cloneServiceSnapshot(s catalog.Snapshot) (catalog.Snapshot, error) {
	raw, err := json.Marshal(s.Catalog)
	if err != nil {
		return catalog.Snapshot{}, err
	}
	s.Catalog, err = config.DecodeCatalog(raw)
	return s, err
}

func editedSnapshot(t *testing.T, s catalog.Snapshot, path []string, value any, remove bool) catalog.Snapshot {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(encoded(t, s.Catalog), &obj); err != nil {
		t.Fatal(err)
	}
	m := obj
	for _, key := range path[:len(path)-1] {
		m = m[key].(map[string]any)
	}
	if remove {
		delete(m, path[len(path)-1])
	} else {
		m[path[len(path)-1]] = value
	}
	c, err := config.DecodeCatalog(encoded(t, obj))
	if err != nil {
		t.Fatal(err)
	}
	s.Catalog, s.Source.Commit = c, commitB
	return s
}

type gateAPI func(context.Context, string) (catalog.APIResponse, error)

func (f gateAPI) Get(ctx context.Context, endpoint string) (catalog.APIResponse, error) {
	return f(ctx, endpoint)
}

func TestCatalogFailureRetainsSnapshotMissingGHAndTimeout(t *testing.T) {
	for _, name := range []string{"missing-gh", "timeout"} {
		t.Run(name, func(t *testing.T) {
			s, p := serviceRig(t)
			beforeState := seedBoundService(t, s, boundServiceSnapshot(t))
			beforeFiles := serviceFiles(t, p)
			beforeInfo := map[string]fs.FileInfo{}
			for path := range beforeFiles {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				beforeInfo[path] = info
			}
			publicCalls, privateCalls := 0, 0
			public := gateAPI(func(ctx context.Context, endpoint string) (catalog.APIResponse, error) {
				publicCalls++
				if endpoint != "/repos/fixture-owner/fixture.repo" {
					t.Fatalf("unexpected endpoint %s", endpoint)
				}
				if name == "timeout" {
					<-ctx.Done()
					return catalog.APIResponse{}, ctx.Err()
				}
				return catalog.APIResponse{Status: 404}, nil
			})
			private := gateAPI(func(_ context.Context, endpoint string) (catalog.APIResponse, error) {
				privateCalls++
				if endpoint != "/repos/fixture-owner/fixture.repo" || name != "missing-gh" {
					t.Fatalf("unexpected private endpoint %s", endpoint)
				}
				return catalog.APIResponse{}, catalog.ErrGHRequired
			})
			s.Fetcher = &catalog.GitHub{Public: public, Private: private}
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			candidate, err := s.Fetcher.Fetch(ctx, beforeState.Local.Sources[0])
			want := error(catalog.ErrGHRequired)
			if name == "timeout" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) || !reflect.DeepEqual(candidate, catalog.Snapshot{}) || publicCalls != 1 || privateCalls != map[string]int{"missing-gh": 1, "timeout": 0}[name] {
				t.Fatal(candidate, err, publicCalls, privateCalls)
			}
			if !reflect.DeepEqual(beforeState, serviceRead(t, s)) || !reflect.DeepEqual(beforeFiles, serviceFiles(t, p)) {
				t.Fatal("failed fetch changed saved state")
			}
			for path, before := range beforeInfo {
				after, err := os.Stat(path)
				if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
					t.Fatal("failed fetch changed file", path, err)
				}
			}
			loaded, err := config.Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := loaded.Connection(servicePaper); err != nil {
				t.Fatal("saved catalog unavailable offline", err)
			}
			if _, _, err := loaded.Connection("mine"); err != nil {
				t.Fatal("unrelated local connection unavailable", err)
			}
			for _, path := range []string{p.SocketFile, p.LogFile} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("runtime effect", path, err)
				}
			}
		})
	}
}
