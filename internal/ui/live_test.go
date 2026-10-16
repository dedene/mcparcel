package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// selections is the stored selections without the revision, to compare a
// state with how setup opened.
func selections(s config.State) map[string]config.Selection { return s.Selections.Connections }

func TestChangeIsWrittenAtOnce(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	before := h.disk().Selections.Revision
	h.selectRow("Figma")
	h.press("space")
	disk := h.disk()
	if disk.Selections.Revision != before+1 || !disk.Selections.Connections[idFigma].Enabled {
		t.Fatal(disk.Selections.Revision, disk.Selections.Connections[idFigma])
	}
	h.focused("[x] Figma")
	h.press("space")
	if disk := h.disk(); disk.Selections.Revision != before+2 || disk.Selections.Connections[idFigma].Enabled {
		t.Fatal(disk.Selections.Revision, disk.Selections.Connections[idFigma])
	}
	h.lacks("unsaved", "Save", "revision")
}

// A change made elsewhere since setup read the state is kept: the change
// is applied to the fresh state, which then shows.
func TestChangeAppliesToFreshState(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	mustUpdate(t, h.store, func(s *config.State) {
		s.Local.Aliases = map[string]string{"p": idPaper}
		sel := s.Selections.Connections[idExcali]
		sel.Enabled = false
		s.Selections.Connections[idExcali] = sel
	})
	h.selectRow("Figma")
	h.press("space")
	disk := h.disk()
	if disk.Local.Aliases["p"] != idPaper || !disk.Selections.Connections[idFigma].Enabled || disk.Selections.Connections[idExcali].Enabled {
		t.Fatal(disk.Local.Aliases, disk.Selections.Connections)
	}
	if h.m.live.State().Selections.Connections[idExcali].Enabled {
		t.Fatal("the change from elsewhere does not show")
	}
	h.contains("Other 1/2")
}

// A change that no longer applies to the fresh state is not made, and the
// status says so.
func TestChangeThatNoLongerAppliesSaysSo(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	mustUpdate(t, h.store, func(s *config.State) {
		catalog := s.Catalogs[fixSource]
		delete(catalog.Connections, "figma")
		s.Catalogs[fixSource] = catalog
		s.Local.Sources[0].Commit = strings.Repeat("b", 40)
	})
	revision := h.disk().Selections.Revision
	h.selectRow("Figma")
	h.press("space")
	h.contains("This changed elsewhere meanwhile, so the change no longer applies.")
	if disk := h.disk(); disk.Selections.Revision != revision || disk.Selections.Connections[idFigma].Enabled {
		t.Fatal(disk.Selections.Revision, disk.Selections.Connections[idFigma])
	}
	h.lacks("Figma")
}

func TestUndoGoesBackToHowSetupOpened(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	opened := h.disk()
	h.selectRow("Figma")
	h.press("space")
	h.selectRow("Paper")
	h.press("space")
	h.selectTab("Other")
	h.selectRow("Excalidraw")
	h.press("space")
	h.press("u")
	h.contains("Excalidraw is on again.")
	h.press("u")
	h.contains("Paper is off again.")
	h.press("u")
	h.contains("Figma is off again.")
	disk := h.disk()
	if disk.Selections.Revision != opened.Selections.Revision+6 || !reflect.DeepEqual(selections(disk), selections(opened)) {
		t.Fatalf("%d %+v", disk.Selections.Revision, selections(disk))
	}
	h.press("u")
	h.contains("Nothing to undo.")
	if h.disk().Selections.Revision != disk.Selections.Revision {
		t.Fatal("an empty undo wrote")
	}
}

// An enable that linked a profile on its own undoes as one step.
func TestUndoEnableWithLinkedProfile(t *testing.T) {
	h := newHarness(t, func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Local.CredentialProfiles["team"] = config.Profile{Mode: "desktop", Account: "Team account"}
			s.Selections.Connections[idNotes] = config.Selection{Inputs: map[string]string{"workspace": "docs"}}
		})
	}, 120, 35)
	opened := h.disk()
	h.selectTab("Research")
	h.press("space")
	h.contains("Linked 1Password profile team.")
	if sel := h.disk().Selections.Connections[idNotes]; !sel.Enabled || sel.CredentialProfile != "team" {
		t.Fatal(sel)
	}
	h.press("u")
	h.contains("Notes is off again.")
	if !reflect.DeepEqual(selections(h.disk()), selections(opened)) {
		t.Fatal(h.disk().Selections.Connections[idNotes])
	}
}

// Undoing an accepted review asks for it again; undoing a value on a
// connection that is on keeps it accepted, as it was.
func TestUndoKeepsReviewAsItWas(t *testing.T) {
	h := newHarness(t, func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Selections.Connections[idNotes] = config.Selection{Enabled: true, Inputs: map[string]string{"workspace": "docs"}, CredentialProfile: "work"}
		})
	}, 120, 35)
	opened := h.disk()
	h.selectTab("Development")
	h.selectRow("Reviewer")
	h.press("space")
	if sel := h.disk().Selections.Connections[idReview]; !sel.Enabled || sel.ReviewRequired {
		t.Fatal(sel)
	}
	h.press("u")
	h.contains("Reviewer needs a review again.")
	if sel := h.disk().Selections.Connections[idReview]; !sel.Enabled || !sel.ReviewRequired {
		t.Fatal(sel)
	}
	h.details("Research", "Notes")
	h.do("Change workspace")
	h.press("ctrl+u")
	h.typeText("wiki")
	h.press("enter")
	if sel := h.disk().Selections.Connections[idNotes]; sel.Inputs["workspace"] != "wiki" {
		t.Fatal(sel)
	}
	h.press("u")
	h.contains("Input workspace of Notes is back as it was.")
	if !reflect.DeepEqual(selections(h.disk()), selections(opened)) {
		t.Fatal(h.disk().Selections.Connections[idNotes])
	}
}

// Undoing a value on a connection that is on but missed it puts it back as
// it was: on, still missing it.
func TestUndoInputOnRowThatMissedIt(t *testing.T) {
	h := newHarness(t, seedMarks, 120, 35)
	opened := h.disk()
	h.details("Research", "Notes")
	h.press("enter")
	h.typeText("docs")
	h.press("enter")
	h.press("u")
	h.contains("Input workspace of Notes is back as it was.")
	if !reflect.DeepEqual(selections(h.disk()), selections(opened)) {
		t.Fatal(h.disk().Selections.Connections[idNotes])
	}
}

// A connection changed elsewhere after a change is not undone over it;
// that step is dropped.
func TestUndoRefusesWhatChangedElsewhere(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectRow("Figma")
	h.press("space")
	mustUpdate(t, h.store, func(s *config.State) {
		sel := s.Selections.Connections[idFigma]
		sel.DisabledTools = []string{"export"}
		s.Selections.Connections[idFigma] = sel
	})
	revision := h.disk().Selections.Revision
	h.press("u")
	h.contains("That changed elsewhere since, so it cannot be undone.")
	if disk := h.disk(); disk.Selections.Revision != revision || !disk.Selections.Connections[idFigma].Enabled {
		t.Fatal(disk.Selections.Revision, disk.Selections.Connections[idFigma])
	}
	h.press("u")
	h.contains("Nothing to undo.")
}

// Undoing an added personal connection removes it, with the domain it
// declared.
func TestUndoAddedPersonalConnection(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	opened := h.disk()
	h.press("n")
	h.typeText("desk")
	h.formTo("domains")
	h.typeText("workbench")
	h.formTo("command")
	h.typeText("desk-fixture")
	h.press("enter")
	if _, ok := h.disk().Personal.Connections["desk"]; !ok {
		t.Fatal("not added")
	}
	h.press("u")
	h.contains("desk is removed again.")
	disk := h.disk()
	if !reflect.DeepEqual(disk.Personal, opened.Personal) || !reflect.DeepEqual(selections(disk), selections(opened)) {
		t.Fatalf("%+v", disk.Personal)
	}
}

// Keys pressed while a change is written wait for it, then run in order.
func TestKeysWaitForTheWrite(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectRow("Figma")
	h.hold = true
	h.press("space", "down", "space")
	if len(h.held) != 1 || len(h.m.queued) != 2 || h.m.cursor != 0 {
		t.Fatal(len(h.held), len(h.m.queued), h.m.cursor)
	}
	h.flush()
	h.flush()
	disk := h.disk()
	if !disk.Selections.Connections[idFigma].Enabled || !disk.Selections.Connections[idPaper].Enabled || h.m.busy || len(h.m.queued) != 0 {
		t.Fatal(disk.Selections.Connections, h.m.busy, h.m.queued)
	}
}

// Text pasted while a change is written waits too, after the keys pressed
// before it.
func TestPasteWaitsForTheWrite(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectRow("Figma")
	h.hold = true
	h.press("space", "/")
	h.paste("pap")
	h.flush()
	if h.m.screen != screenSearch || h.m.search.String() != "pap" {
		t.Fatal(h.m.screen, h.m.search.String())
	}
}

func TestCtrlCDuringWriteWaitsForIt(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectRow("Figma")
	h.hold = true
	h.press("space", "ctrl+c")
	if h.quit || len(h.held) != 1 {
		t.Fatal(h.quit, len(h.held))
	}
	h.flush()
	if !h.quit || !h.m.Result().Interrupted || !h.disk().Selections.Connections[idFigma].Enabled {
		t.Fatal(h.quit, h.m.Result())
	}
}

// A write that failed part way, with no read to settle it, is reported
// when setup ends; a later change that lands settles it.
func TestUnconfirmedWriteIsReported(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.m.busy = true
	h.send(writtenMsg{writeResult{err: config.ErrConfigWrite}})
	if !h.m.Result().Unconfirmed {
		t.Fatal("unconfirmed write not reported")
	}
	h.press("space")
	if h.m.Result().Unconfirmed {
		t.Fatal("a later change did not settle it")
	}
}

func TestUnavailableRowCanOnlyTurnOff(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectTab("Other")
	h.selectRow("local:gone")
	h.press("space")
	if sel, ok := h.disk().Selections.Connections[idGone]; !ok || sel.Enabled {
		t.Fatal(sel, ok)
	}
	revision := h.disk().Selections.Revision
	h.press("space")
	h.contains("Personal no longer has this server.")
	if h.disk().Selections.Revision != revision {
		t.Fatal("turning an unavailable row on wrote")
	}
}
