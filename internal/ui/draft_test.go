package ui

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func fixtureDraft(t *testing.T) (*Draft, *config.Store, config.Paths) {
	t.Helper()
	store, paths, state := fixtureStore(t, seedFixture)
	d, err := NewDraft(state)
	if err != nil {
		t.Fatal(err)
	}
	return d, store, paths
}

func TestDraftToggleBackClearsJournal(t *testing.T) {
	d, _, _ := fixtureDraft(t)
	if err := d.SetEnabled(idPaper, true); err != nil {
		t.Fatal(err)
	}
	if d.Changes() != 1 || !d.Pending(idPaper) {
		t.Fatal(d.Changes(), d.Pending(idPaper))
	}
	if err := d.SetEnabled(idPaper, false); err != nil {
		t.Fatal(err)
	}
	if d.Changes() != 0 || d.Pending(idPaper) || len(d.ops) != 0 {
		t.Fatal(d.Changes(), d.ops)
	}
	if !reflect.DeepEqual(d.State(), d.Base()) {
		t.Fatal("draft state differs from base after toggling back")
	}
}

func TestDraftCountsChangedFields(t *testing.T) {
	d, _, _ := fixtureDraft(t)
	for _, step := range []struct {
		id string
		on bool
	}{{idPaper, true}, {idFigma, true}, {idPaper, false}, {idPaper, true}, {idExcali, false}} {
		if err := d.SetEnabled(step.id, step.on); err != nil {
			t.Fatal(step, err)
		}
	}
	if d.Changes() != 3 {
		t.Fatal(d.Changes())
	}
}

func TestDraftReplaysInUserOrder(t *testing.T) {
	_, store, _ := fixtureDraft(t)
	// The journal replays in the order the user made the changes, so the last
	// toggle of Notes wins. Task B extends this with input changes.
	fresh := mustUpdate(t, store, func(s *config.State) {
		s.Selections.Connections[idNotes] = config.Selection{Enabled: true, Inputs: map[string]string{"workspace": "docs"}, CredentialProfile: "work"}
	})
	d, err := NewDraft(fresh)
	if err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{false, true, false} {
		if err := d.SetEnabled(idNotes, on); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetEnabled(idPaper, true); err != nil {
		t.Fatal(err)
	}
	next, err := d.Save(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if next.Selections.Connections[idNotes].Enabled || !next.Selections.Connections[idPaper].Enabled {
		t.Fatal(next.Selections.Connections)
	}
}

func TestDraftAcceptReviewIsAChange(t *testing.T) {
	d, store, _ := fixtureDraft(t)
	if err := d.SetEnabled(idReview, true); err != nil {
		t.Fatal(err)
	}
	if d.Changes() != 1 {
		t.Fatal(d.Changes())
	}
	next, err := d.Save(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	sel := next.Selections.Connections[idReview]
	if !sel.Enabled || sel.ReviewRequired {
		t.Fatal(sel)
	}
}

func TestDraftSaveIsOneAtomicUpdate(t *testing.T) {
	d, store, _ := fixtureDraft(t)
	before := d.Base().Selections.Revision
	for _, id := range []string{idPaper, idFigma} {
		if err := d.SetEnabled(id, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetEnabled(idExcali, false); err != nil {
		t.Fatal(err)
	}
	next, err := d.Save(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if next.Selections.Revision != before+1 || d.Changes() != 0 || d.Base().Selections.Revision != before+1 {
		t.Fatal(next.Selections.Revision, before, d.Changes())
	}
	disk, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !disk.Selections.Connections[idPaper].Enabled || !disk.Selections.Connections[idFigma].Enabled || disk.Selections.Connections[idExcali].Enabled {
		t.Fatal(disk.Selections.Connections)
	}
}

func TestDraftNothingToSaveTouchesNoFile(t *testing.T) {
	store, paths, state := fixtureStore(t, nil)
	d, err := NewDraft(state)
	if err != nil {
		t.Fatal(err)
	}
	next, err := d.Save(context.Background(), store)
	if err != nil || next.Selections.Revision != 0 {
		t.Fatal(next.Selections.Revision, err)
	}
	for _, path := range []string{paths.LockFile, paths.SelectionsFile, paths.ConfigFile, paths.PersonalFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal(path, err)
		}
	}
}

func TestDraftEnableConfigRequiredRejected(t *testing.T) {
	d, _, _ := fixtureDraft(t)
	if err := d.SetEnabled(idNotes, true); !errors.Is(err, config.ErrConfigRequired) {
		t.Fatal(err)
	}
	if d.Changes() != 0 || len(d.ops) != 0 || d.State().Selections.Connections[idNotes].Enabled {
		t.Fatal("rejected enable was recorded")
	}
}

func TestDraftUnavailableCanOnlyDisable(t *testing.T) {
	d, _, _ := fixtureDraft(t)
	if err := d.SetEnabled(idGone, false); err != nil {
		t.Fatal(err)
	}
	if d.Changes() != 1 {
		t.Fatal(d.Changes())
	}
	if err := d.SetEnabled(idGone, true); !errors.Is(err, config.ErrNotFound) {
		t.Fatal(err)
	}
	if d.Changes() != 1 || d.State().Selections.Connections[idGone].Enabled {
		t.Fatal("unavailable connection re-enabled")
	}
}

func TestDraftSaveConflictKeepsDraft(t *testing.T) {
	d, store, _ := fixtureDraft(t)
	if err := d.SetEnabled(idPaper, true); err != nil {
		t.Fatal(err)
	}
	mustUpdate(t, store, func(s *config.State) { s.Local.Aliases = map[string]string{"p": idPaper} })
	if _, err := d.Save(context.Background(), store); !errors.Is(err, config.ErrConfigConflict) {
		t.Fatal(err)
	}
	if d.Changes() != 1 || !d.State().Selections.Connections[idPaper].Enabled {
		t.Fatal("conflict lost the draft")
	}
}

func TestDraftRebaseReappliesUntouchedOps(t *testing.T) {
	d, store, _ := fixtureDraft(t)
	if err := d.SetEnabled(idPaper, true); err != nil {
		t.Fatal(err)
	}
	fresh := mustUpdate(t, store, func(s *config.State) {
		sel := s.Selections.Connections[idFigma]
		sel.Enabled = true
		s.Selections.Connections[idFigma] = sel
	})
	dropped, err := d.Rebase(fresh)
	if err != nil || len(dropped) != 0 {
		t.Fatal(dropped, err)
	}
	if d.Changes() != 1 || d.Base().Selections.Revision != fresh.Selections.Revision {
		t.Fatal(d.Changes())
	}
	next, err := d.Save(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Selections.Connections[idPaper].Enabled || !next.Selections.Connections[idFigma].Enabled {
		t.Fatal(next.Selections.Connections)
	}
}

func TestDraftRebaseDropsOpsChangedElsewhere(t *testing.T) {
	d, store, _ := fixtureDraft(t)
	if err := d.SetEnabled(idExcali, false); err != nil {
		t.Fatal(err)
	}
	if err := d.SetEnabled(idFigma, true); err != nil {
		t.Fatal(err)
	}
	// A newer CLI edit touched Excalidraw's enabled field.
	fresh := mustUpdate(t, store, func(s *config.State) {
		sel := s.Selections.Connections[idExcali]
		sel.ReviewRequired = true
		s.Selections.Connections[idExcali] = sel
	})
	dropped, err := d.Rebase(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != (Dropped{Change: "disable Excalidraw", Reason: reasonChanged}) {
		t.Fatal(dropped)
	}
	if d.Changes() != 1 || !d.State().Selections.Connections[idFigma].Enabled {
		t.Fatal(d.Changes())
	}
	if sel := d.State().Selections.Connections[idExcali]; !sel.Enabled || !sel.ReviewRequired {
		t.Fatal("newer edit overwritten", sel)
	}
}

func TestDraftRebaseDropsEnableWhenDefinitionChanged(t *testing.T) {
	d, store, _ := fixtureDraft(t)
	if err := d.SetEnabled(idPaper, true); err != nil {
		t.Fatal(err)
	}
	fresh := mustUpdate(t, store, func(s *config.State) {
		catalog := s.Catalogs[fixSource]
		paper := catalog.Connections["paper"]
		paper.Transport = stdio("other-command")
		catalog.Connections["paper"] = paper
		s.Catalogs[fixSource] = catalog
		s.Local.Sources[0].Commit = "b" + s.Local.Sources[0].Commit[1:]
	})
	dropped, err := d.Rebase(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != (Dropped{Change: "enable Paper", Reason: reasonChanged}) {
		t.Fatal(dropped)
	}
	if d.Changes() != 0 || d.State().Selections.Connections[idPaper].Enabled {
		t.Fatal("enabled a definition the user has not seen")
	}
}

func TestDraftRebaseKeysEachField(t *testing.T) {
	d, store, _ := fixtureDraft(t)
	if err := d.SetInput(idNotes, "workspace", "docs"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetTool(idExcali, "draw", false); err != nil {
		t.Fatal(err)
	}
	if d.Pending(idExcali) || !d.Pending(idNotes) {
		t.Fatal("tool toggles must not count as pending execution changes")
	}
	// A newer edit bound Notes' profile (another field) and changed the input.
	fresh := mustUpdate(t, store, func(s *config.State) {
		sel := s.Selections.Connections[idNotes]
		sel.CredentialProfile = "work"
		s.Selections.Connections[idNotes] = sel
	})
	if dropped, err := d.Rebase(fresh); err != nil || len(dropped) != 0 || d.Changes() != 2 {
		t.Fatal(dropped, err, d.Changes())
	}
	fresh = mustUpdate(t, store, func(s *config.State) {
		sel := s.Selections.Connections[idNotes]
		sel.Inputs = map[string]string{"workspace": "other"}
		s.Selections.Connections[idNotes] = sel
	})
	dropped, err := d.Rebase(fresh)
	if err != nil || len(dropped) != 1 || dropped[0] != (Dropped{Change: "set input workspace on Notes", Reason: reasonChanged}) {
		t.Fatal(dropped, err)
	}
	if d.State().Selections.Connections[idNotes].Inputs["workspace"] != "other" || !slices.Contains(d.State().Selections.Connections[idExcali].DisabledTools, "draw") {
		t.Fatal(d.State().Selections.Connections)
	}
}

func TestDraftPendingIgnoresChangesThatCancelOut(t *testing.T) {
	d, _, _ := fixtureDraft(t)
	for _, step := range []struct {
		id string
		on bool
	}{{idExcali, false}, {idFigma, true}, {idExcali, true}} {
		if err := d.SetEnabled(step.id, step.on); err != nil {
			t.Fatal(step, err)
		}
	}
	if d.Changes() != 1 || d.Pending(idExcali) || !d.Pending(idFigma) {
		t.Fatal(d.Changes(), d.Pending(idExcali), d.Pending(idFigma))
	}
}

func TestDraftRebaseReportsChangesAlreadyPresent(t *testing.T) {
	d, store, _ := fixtureDraft(t)
	if err := d.SetEnabled(idFigma, true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetEnabled(idPaper, true); err != nil {
		t.Fatal(err)
	}
	// Figma's change is already in the newer configuration, as after a save
	// whose durable write landed but could not be confirmed.
	fresh := mustUpdate(t, store, func(s *config.State) {
		sel := s.Selections.Connections[idFigma]
		sel.Enabled = true
		s.Selections.Connections[idFigma] = sel
	})
	dropped, err := d.Rebase(fresh)
	if err != nil || len(dropped) != 1 || dropped[0] != (Dropped{Change: "enable Figma", Reason: reasonPresent}) {
		t.Fatal(dropped, err)
	}
	if d.Changes() != 1 || !d.Pending(idPaper) || d.Pending(idFigma) {
		t.Fatal(d.Changes())
	}
}

func TestDraftRebaseDropsChangesThatNoLongerApply(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Selections.Connections[idNotes] = config.Selection{Inputs: map[string]string{"workspace": "docs"}, CredentialProfile: "work"}
		})
	}
	store, _, state := fixtureStore(t, seed)
	d, err := NewDraft(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetEnabled(idNotes, true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetEnabled(idFigma, true); err != nil {
		t.Fatal(err)
	}
	// A newer edit removed the input Notes needs; its enabled field and
	// definition are unchanged, but enabling it no longer applies.
	fresh := mustUpdate(t, store, func(s *config.State) {
		sel := s.Selections.Connections[idNotes]
		sel.Inputs = nil
		s.Selections.Connections[idNotes] = sel
	})
	dropped, err := d.Rebase(fresh)
	if err != nil || len(dropped) != 1 || dropped[0] != (Dropped{Change: "enable Notes", Reason: reasonStale}) {
		t.Fatal(dropped, err)
	}
	if d.Changes() != 1 || d.State().Selections.Connections[idNotes].Enabled || !d.State().Selections.Connections[idFigma].Enabled {
		t.Fatal(d.Changes(), d.State().Selections.Connections)
	}
	if _, err := d.Save(context.Background(), store); err != nil {
		t.Fatal(err)
	}
}
