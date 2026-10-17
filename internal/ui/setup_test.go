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
	h.contains("[Design 1/2]")
	h.focused("[x] Paper")
	h.selectTab("Development")
	h.selectRow("Paper")
	h.contains("[Development 2/2]")
	if row, _ := h.m.selected(); row.ID != idPaper || h.m.statusOf(row).step() {
		t.Fatal(row.ID, h.m.statusOf(row))
	}
	h.press("space")
	h.contains("[Development 1/2]", "Design 0/2")
}

func TestTabsOrderOtherLastAlwaysShown(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	var labels []string
	for _, tb := range tabs(h.m.live.Effective()) {
		labels = append(labels, tb.label)
	}
	if !reflect.DeepEqual(labels, []string{"Design", "Development", "Research", "Other"}) {
		t.Fatal(labels)
	}
	h.contains("[Design 0/2]", "Development 1/2", "Research 0/1", "Other 2/2")
	empty := newHarness(t, nil, 120, 35)
	empty.contains("[Other 0/0]", "No catalogs yet. Add one from a terminal: mcparcel add <owner/repo>.", "Press n to add a personal connection.")
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
	h.focused("MCP 29")
	if h.m.cursor != 29 || strings.Contains(h.plain(), "MCP 00") {
		t.Fatal(h.m.cursor, h.plain())
	}
	h.press("down")
	if h.m.cursor != 29 {
		t.Fatal(h.m.cursor)
	}
	h.press("home")
	h.focused("MCP 00")
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
	h.contains("[Design (1)]", "Development (1)", "Research (0)", "Other (0)", "Search: PAP_")
	h.focused("Paper")
	if strings.Contains(h.plain(), "Figma") {
		t.Fatal(h.plain())
	}
	h.press("enter")
	h.contains("Search: PAP   (Esc clears)")
	h.press("space")
	if !h.m.live.State().Selections.Connections[idPaper].Enabled {
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

// Esc clears an active search; otherwise nothing is pending, so it only
// hints how to quit. q quits at once.
func TestEscClearsSearchAndNeverQuits(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("/")
	h.typeText("pap")
	h.press("enter", "esc")
	if h.quit || h.m.search.String() != "" {
		t.Fatal(h.quit, h.m.search.String())
	}
	h.press("esc", "left")
	if h.quit {
		t.Fatal("esc quit")
	}
	h.contains("Changes apply at once; u undoes them. q quits.")
	h.press("q")
	if !h.quit {
		t.Fatal("q did not quit")
	}
}

// Leaving a search keeps the cursor on the server it found.
func TestLeavingASearchKeepsTheServer(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("/")
	h.typeText("pap")
	h.press("esc")
	h.focused("Paper")
	h.press("/")
	h.typeText("pap")
	h.press("enter", "left")
	h.focused("Paper")
	h.lacks("Search:")
}

// Tab and Shift+Tab move between domains, wrapping; Right and Enter open
// the server, Left and Esc go back.
func TestTabSwitchesDomainsAndArrowsGoInAndOut(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.contains("[Design 0/2]")
	h.press("tab")
	h.contains("[Development 1/2]")
	h.press("shift+tab", "shift+tab")
	h.contains("[Other 2/2]")
	h.press("tab")
	h.contains("[Design 0/2]")
	h.lacks("[Save]", "[Cancel]")
	h.press("right")
	if h.m.screen != screenDetails || h.m.detailID != idFigma {
		t.Fatal(h.m.screen, h.m.detailID)
	}
	h.press("left")
	if h.m.screen != screenList {
		t.Fatal(h.m.screen)
	}
	h.press("enter", "esc")
	if h.m.screen != screenList || h.quit {
		t.Fatal(h.m.screen, h.quit)
	}
}

// Right on a server page only goes deeper: on "Turn on" it writes nothing,
// so a stray arrow never turns on, signs in or removes anything. On
// "Choose tools" it opens the Tools page.
func TestRightOnServerPageOnlyOpensPages(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("right")
	if h.m.screen != screenDetails || h.m.detailID != idFigma {
		t.Fatal(h.m.screen, h.m.detailID)
	}
	h.focused("Turn on")
	before := h.writes()
	h.press("right")
	if h.writes() != before || h.m.screen != screenDetails {
		t.Fatalf("right did the action: writes %d -> %d, screen %v", before, h.writes(), h.m.screen)
	}
	h.do("Choose tools")
	h.press("esc")
	h.focused("Choose tools")
	h.press("right")
	if h.m.screen != screenTools {
		t.Fatal(h.m.screen)
	}
}

// Browsing writes nothing, and q quits at once.
func TestQuitWritesNothing(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	before := h.fileBytes()
	h.press("down", "tab", "/")
	h.typeText("a")
	h.press("enter", "right", "esc", "?", "esc", "q")
	if r := h.m.Result(); !h.quit || !reflect.DeepEqual(r, fixtureResult) {
		t.Fatal(h.quit, r)
	}
	if !reflect.DeepEqual(before, h.fileBytes()) {
		t.Fatal("browsing wrote files")
	}
	empty := newHarness(t, nil, 120, 35)
	empty.press("q")
	if !empty.quit {
		t.Fatal("q did not quit")
	}
	if _, err := os.Stat(empty.paths.LockFile); !os.IsNotExist(err) {
		t.Fatal("lock file created", err)
	}
}

func TestCtrlCQuitsWithoutWriting(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	before := h.fileBytes()
	h.press("down", "ctrl+c")
	if !h.quit || !h.m.Result().Interrupted {
		t.Fatal(h.quit, h.m.Result())
	}
	if !reflect.DeepEqual(before, h.fileBytes()) {
		t.Fatal("ctrl+c wrote files")
	}
}

// Browsing and toggling never contact the runtime or sign in: only opening
// a tools page and s do (see tools_test.go, finish_test.go). They read the
// stored sign-in of a server that is on, once a session.
func TestBrowsingAndTogglingNeverLoadToolsOrSignIn(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	h.press("tab", "tab", "shift+tab", "down", "up", "/")
	h.typeText("a")
	h.press("enter", "esc", "space")
	h.selectTab("Other")
	h.selectRow("Calendar")
	h.press("space", "u", "space", "right", "down", "left", "?", "esc", "q")
	if !h.disk().Selections.Connections["local:calendar"].Enabled {
		t.Fatal("calendar not on")
	}
	if h.loads.calls != 0 || h.fin.checks != 1 || h.fin.signIns != 0 || !h.quit {
		t.Fatal(h.loads.calls, h.fin.checks, h.fin.signIns, h.quit)
	}
}

func TestUIImportsAllowList(t *testing.T) {
	allowed := map[string][]string{
		// Charm's packages only: Bubble Tea, Lip Gloss and Bubbles. Widths and
		// the color profile come through them, not through their dependencies.
		".": {
			"github.com/dedene/mcparcel/internal/config", "github.com/dedene/mcparcel/internal/edit", "github.com/dedene/mcparcel/internal/output",
			"charm.land/bubbletea/v2", "charm.land/lipgloss/v2", "charm.land/bubbles/v2/...",
		},
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

// containsString matches s exactly, or under an entry ending in "/...".
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
		if prefix, ok := strings.CutSuffix(v, "/..."); ok && strings.HasPrefix(s, prefix+"/") {
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
	if err != nil || !reflect.DeepEqual(result, fixtureResult) {
		t.Fatal(result, err)
	}
}

// fixtureResult is how setup ends on seedFixture untouched: Excalidraw is
// ready, Reviewer needs a review and local:gone is unavailable.
var fixtureResult = Result{On: 3, Ready: 1, Steps: []Step{
	{ID: idReview, Label: "Reviewer", Need: NeedReview},
	{ID: idGone, Label: "local:gone", Need: NeedUnavailable},
}}

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
	if !errors.Is(err, context.Canceled) || !result.Interrupted {
		t.Fatal(result, err)
	}
}

// Plain setup opens on the first server, in tab order, that needs a step:
// here Reviewer's review in Development, not the first tab.
func TestSetupOpensOnTheFirstStep(t *testing.T) {
	h := landingHarness(t, seedFixture, 120, 35)
	h.contains("[Development 1/2]")
	h.focused("Reviewer", "review change")
	h.press("tab")
	h.contains("[Research 0/1]")
	h.m.signInsRead(signInsMsg{})
	h.contains("[Research 0/1]")
}

// The wide footer grows from one line to two while searching, so the list
// loses a row: the cursor on the last visible row scrolls into view.
func TestCursorStaysVisibleWhenFooterGrows(t *testing.T) {
	h := newHarness(t, seedMany(20), 100, 20)
	page := h.m.listHeight()
	for range page - 1 {
		h.press("down")
	}
	if h.m.cursor != page-1 || h.m.offset != 0 {
		t.Fatal("setup: cursor", h.m.cursor, "offset", h.m.offset, "page", page)
	}
	h.press("/")
	if h.m.listHeight() >= page {
		t.Fatal("the footer did not grow; the test needs another size", h.m.listHeight(), page)
	}
	if h.m.cursor < h.m.offset || h.m.cursor >= h.m.offset+h.m.listHeight() {
		t.Fatal("cursor off screen", h.m.cursor, h.m.offset, h.m.listHeight())
	}
	row, _ := h.m.selected()
	if line := h.focusedLine(); !strings.Contains(line, rowLabel(row)) {
		t.Fatalf("focused line %q, selected %q:\n%s", line, rowLabel(row), h.plain())
	}
}
