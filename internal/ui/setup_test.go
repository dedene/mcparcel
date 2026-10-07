package ui

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

func seedMany(n int) func(*testing.T, *config.Store) {
	return func(t *testing.T, store *config.Store) {
		t.Helper()
		if _, err := store.Update(context.Background(), 0, func(s *config.State) error {
			for i := range n {
				s.Personal.Connections[fmt.Sprintf("mcp%02d", i)] = config.Connection{Label: fmt.Sprintf("MCP %02d", i), Transport: stdio("fixture")}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTabsShareOneSelection(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectTab("Design")
	h.selectRow("Paper")
	h.press("space")
	h.contains("[Design 1/2]", "> Paper")
	h.selectTab("Development")
	h.selectRow("Paper")
	h.contains("[Development 2/2]")
	if row, _ := h.m.selected(); row.ID != idPaper || enabledMark(row) != "[x]" {
		t.Fatal(row.ID, enabledMark(row))
	}
	h.press("space")
	h.contains("[Development 1/2]", "Design 0/2")
}

func TestTabsOrderOtherLastAlwaysShown(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	var labels []string
	for _, tb := range tabs(h.m.draft.Effective()) {
		labels = append(labels, tb.label)
	}
	if !reflect.DeepEqual(labels, []string{"Design", "Development", "Research", "Other"}) {
		t.Fatal(labels)
	}
	h.contains("[Design 0/2]", "Development 1/2", "Research 0/1", "Other 2/2")
	empty := newHarness(t, nil, 120, 35)
	empty.contains("[Other 0/0]", "No catalogs yet. Add one from a terminal: mcparcel add <owner/repo>.", "Press A to add a personal connection.")
}

func TestNavigationClampsAndScrolls(t *testing.T) {
	h := newHarness(t, seedMany(30), 120, 24)
	page := h.m.listHeight()
	h.press("up")
	if h.m.cursor != 0 {
		t.Fatal(h.m.cursor)
	}
	h.press("pgdown")
	if h.m.cursor != page {
		t.Fatal(h.m.cursor, page)
	}
	h.press("end")
	h.contains("> MCP 29")
	if h.m.cursor != 29 || strings.Contains(h.plain(), "MCP 00") {
		t.Fatal(h.m.cursor, h.plain())
	}
	h.press("down")
	if h.m.cursor != 29 {
		t.Fatal(h.m.cursor)
	}
	h.press("home")
	h.contains("> MCP 00")
	if strings.Contains(h.plain(), "MCP 29") {
		t.Fatal(h.plain())
	}
	h.press("pgdown", "pgup")
	if h.m.cursor != 0 {
		t.Fatal(h.m.cursor)
	}
}

func TestSearchFiltersAndCountsTabs(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("/")
	h.typeText("PAP")
	h.contains("[Design (1)]", "Development (1)", "Research (0)", "Other (0)", "Search: PAP_", "> Paper")
	if strings.Contains(h.plain(), "Figma") {
		t.Fatal(h.plain())
	}
	h.press("enter")
	h.contains("Search: PAP   (Esc clears)")
	h.press("space")
	if !h.m.draft.State().Selections.Connections[idPaper].Enabled {
		t.Fatal("space after search did not toggle the filtered row")
	}
	h.press("/", "ctrl+u")
	h.paste("team\x1b[31m")
	h.press("enter")
	if h.m.search.String() != "team[31m" {
		t.Fatalf("%q", h.m.search.String())
	}
	h.press("/")
	h.press("backspace", "backspace", "backspace", "backspace", "backspace")
	h.press("down")
	h.contains("Research (1)")
}

func TestSearchNoResultsShowsReset(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("/")
	h.typeText("zzz")
	h.press("enter")
	h.contains(`No MCPs match "zzz" in Design. Esc clears the search.`, "Other (0)")
	h.press("esc")
	h.contains("[Design 0/2]", "Figma")
	if h.quit {
		t.Fatal("esc quit with an active search")
	}
}

func TestEscClearsSearchBeforeCancel(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("/")
	h.typeText("pap")
	h.press("enter", "esc")
	if h.quit || h.m.search.String() != "" {
		t.Fatal(h.quit, h.m.search.String())
	}
	h.press("esc")
	if !h.quit {
		t.Fatal("esc without search did not cancel")
	}
}

func TestTabFocusesActionsVisibly(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, func(o *Options) { o.Color = true })
	h.contains("> Figma", " [Save] [Cancel]")
	h.press("tab")
	h.contains(">[Save] [Cancel]")
	if strings.Contains(h.plain(), "> Figma") {
		t.Fatal("list still shows focus")
	}
	if !strings.Contains(h.view(), ">\x1b[7m[Save]\x1b[0m") {
		t.Fatalf("%q", h.view())
	}
	h.press("tab")
	h.contains(" [Save]>[Cancel]")
	h.press("shift+tab")
	h.contains(">[Save] [Cancel]")
	h.press("shift+tab")
	h.contains("> Figma", " [Save] [Cancel]")
	h.press("shift+tab", "enter")
	if !h.quit {
		t.Fatal("enter on Cancel did not cancel")
	}
}

func TestSaveWritesOnceAndStaysOpen(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	before := h.disk().Selections.Revision
	h.selectRow("Figma")
	h.press("space")
	h.contains("1 unsaved change")
	h.press("tab", "enter")
	disk := h.disk()
	if h.quit || disk.Selections.Revision != before+1 || !disk.Selections.Connections[idFigma].Enabled {
		t.Fatal(h.quit, disk.Selections.Revision, before)
	}
	h.contains(fmt.Sprintf("Saved at revision %d.", before+1), "No unsaved changes")
	h.press("ctrl+s")
	h.contains("Nothing to save.")
	if h.disk().Selections.Revision != before+1 {
		t.Fatal("empty save wrote")
	}
	if r := h.m.Result(); r.Saves != 1 || r.Revision != before+1 || r.Interrupted {
		t.Fatal(r)
	}
}

func TestSaveNothingToSave(t *testing.T) {
	h := newHarness(t, nil, 120, 35)
	before := h.fileBytes()
	h.press("ctrl+s")
	h.contains("Nothing to save.")
	if !reflect.DeepEqual(before, h.fileBytes()) {
		t.Fatal("nothing-to-save wrote files")
	}
	if _, err := os.Stat(h.paths.LockFile); !os.IsNotExist(err) {
		t.Fatal("lock file created", err)
	}
}

func TestCancelWritesNothing(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	before := h.fileBytes()
	h.press("space", "q", "y")
	if !h.quit || h.m.Result().Saves != 0 {
		t.Fatal(h.quit, h.m.Result())
	}
	if !reflect.DeepEqual(before, h.fileBytes()) {
		t.Fatal("cancel wrote files")
	}
	empty := newHarness(t, nil, 120, 35)
	empty.press("q")
	if !empty.quit {
		t.Fatal("cancel without changes did not quit")
	}
	if _, err := os.Stat(empty.paths.LockFile); !os.IsNotExist(err) {
		t.Fatal("lock file created", err)
	}
}

func TestCancelConfirmsUnsavedChanges(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("space", "q")
	h.contains("Discard 1 unsaved change? y/N")
	for _, no := range []string{"n", "enter", "esc", "N"} {
		h.press(no)
		if h.quit || h.m.draft.Changes() != 1 || strings.Contains(h.plain(), "Discard") {
			t.Fatal(no, h.quit, h.m.draft.Changes())
		}
		h.press("esc")
		h.contains("Discard 1 unsaved change? y/N")
	}
	h.press("x")
	h.contains("Discard 1 unsaved change? y/N")
	h.press("Y")
	if !h.quit {
		t.Fatal("y did not discard")
	}
}

func TestCtrlCQuitsWithoutWriting(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	before := h.fileBytes()
	h.press("space", "ctrl+c")
	if !h.quit || !h.m.Result().Interrupted || h.m.Result().Saves != 0 {
		t.Fatal(h.quit, h.m.Result())
	}
	if !reflect.DeepEqual(before, h.fileBytes()) {
		t.Fatal("ctrl+c wrote files")
	}
}

func TestCtrlCDuringSaveWaitsForSave(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	before := h.disk().Selections.Revision
	h.press("space")
	h.hold = true
	h.press("ctrl+s", "ctrl+c")
	if h.quit || len(h.held) != 1 {
		t.Fatal(h.quit, len(h.held))
	}
	h.flush()
	if !h.quit {
		t.Fatal("did not quit after the save returned")
	}
	if r := h.m.Result(); !r.Interrupted || r.Saves != 1 || r.Revision != before+1 || h.disk().Selections.Revision != before+1 {
		t.Fatal(r, before)
	}
}

func TestKeysIgnoredWhileSaving(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("space")
	h.hold = true
	h.press("ctrl+s")
	h.contains("Saving...")
	h.press("down", "space", "right", "q", "/", "ctrl+s")
	if h.m.cursor != 0 || h.m.tabIndex != 0 || h.m.screen != screenList || h.m.draft.Changes() != 1 || len(h.held) != 1 || h.quit {
		t.Fatal(h.m.cursor, h.m.tabIndex, h.m.screen, h.m.draft.Changes(), len(h.held))
	}
	h.flush()
	if h.m.Result().Saves != 1 || h.m.busy {
		t.Fatal(h.m.Result())
	}
}

func TestConflictKeepsDraftAndOffersReload(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("space")
	mustUpdate(t, h.store, func(s *config.State) { s.Local.Aliases = map[string]string{"p": idPaper} })
	h.press("ctrl+s")
	h.contains("The configuration changed since setup loaded it. Your 1 change is kept.", "r: reload and reapply   Esc: back")
	h.press("esc")
	if h.m.screen != screenList || h.m.draft.Changes() != 1 || h.m.Result().Saves != 0 {
		t.Fatal(h.m.screen, h.m.draft.Changes())
	}
	if h.disk().Local.Aliases["p"] != idPaper {
		t.Fatal("newer edit lost")
	}
}

func TestReloadReappliesAndReportsDropped(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectRow("Figma")
	h.press("space")
	h.selectTab("Other")
	h.selectRow("Excalidraw")
	h.press("space")
	fresh := mustUpdate(t, h.store, func(s *config.State) {
		sel := s.Selections.Connections[idExcali]
		sel.ReviewRequired = true
		s.Selections.Connections[idExcali] = sel
	})
	h.press("ctrl+s", "r")
	h.contains(fmt.Sprintf("Reloaded at revision %d. Reapplied 1 change. Dropped (changed elsewhere): disable Excalidraw.", fresh.Selections.Revision))
	h.press("ctrl+s")
	disk := h.disk()
	if !disk.Selections.Connections[idFigma].Enabled || !disk.Selections.Connections[idExcali].Enabled || !disk.Selections.Connections[idExcali].ReviewRequired {
		t.Fatal(disk.Selections.Connections)
	}
}

func TestBrowsingAndSavingNeverLoadTools(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("right", "right", "left", "down", "up", "/")
	h.typeText("a")
	h.press("enter", "esc", "space", "ctrl+s", "tab", "tab", "enter")
	if h.loads.calls != 0 {
		t.Fatal(h.loads.calls)
	}
}

func TestUIImportsAllowList(t *testing.T) {
	allowed := map[string][]string{
		".":       {"github.com/dedene/mcparcel/internal/config", "github.com/dedene/mcparcel/internal/edit", "github.com/dedene/mcparcel/internal/output", "charm.land/bubbletea/v2"},
		"../edit": {"github.com/dedene/mcparcel/internal/config", "github.com/dedene/mcparcel/internal/args", "github.com/dedene/mcparcel/internal/jsonutil"},
	}
	for dir, allow := range allowed {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatal(dir, err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range parsed.Imports {
				path, _ := strconv.Unquote(spec.Path.Value)
				first, _, _ := strings.Cut(path, "/")
				if !strings.Contains(first, ".") {
					continue // standard library
				}
				if !containsString(allow, path) {
					t.Errorf("%s imports %s", file, path)
				}
			}
		}
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestRunSetupQuitsFromPipeInput(t *testing.T) {
	store, _, state := fixtureStore(t, seedFixture)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := RunSetup(ctx, store, state, Options{In: strings.NewReader("q"), Out: io.Discard, Describe: describeFixture})
	if err != nil || result.Saves != 0 || result.Interrupted || result.Revision != state.Selections.Revision {
		t.Fatal(result, err)
	}
}

func TestRunSetupCanceledContext(t *testing.T) {
	store, _, state := fixtureStore(t, seedFixture)
	in, w := io.Pipe()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	done := make(chan struct{})
	var result Result
	var err error
	go func() {
		defer close(done)
		result, err = RunSetup(ctx, store, state, Options{In: in, Out: io.Discard, Describe: describeFixture})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSetup ignored the canceled context")
	}
	if !errors.Is(err, context.Canceled) || !result.Interrupted || result.Saves != 0 {
		t.Fatal(result, err)
	}
}
