package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

var fixtureTools = []string{`{"name":"draw","description":"Draw a diagram.\nSecond line."}`, `{"name":"erase","inputSchema":{}}`}

func fixedNow() func(*Options) {
	return func(o *Options) {
		now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
		o.Now = func() time.Time { return now }
	}
}

func toolHarness(t *testing.T, seed func(*testing.T, *config.Store), opts ...func(*Options)) *harness {
	t.Helper()
	h := newHarness(t, seed, 120, 35, append([]func(*Options){fixedNow()}, opts...)...)
	h.loads.items = fixtureTools
	return h
}

// Opening the tools page of a server that is on loads its tools once per
// session; browsing details never does.
func TestToolsLoadWhenThePageOpens(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.details("Other", "Excalidraw")
	h.press("down", "up", "esc", "down", "up")
	if h.loads.calls != 0 {
		t.Fatal("browsing loaded tools", h.loads.calls)
	}
	h.details("Other", "Excalidraw")
	h.press("enter")
	if h.loads.calls != 1 || h.loads.ids[0] != idExcali || h.m.screen != screenTools {
		t.Fatal(h.loads.calls, h.loads.ids, h.m.screen)
	}
	h.contains("Loaded less than 1m ago (2 tools).", "> [+] draw", "Draw a diagram.", "[+] erase")
	h.lacks("Second line.", "Press l", "Save first", "l connect")
	h.press("left")
	if h.m.screen != screenDetails {
		t.Fatal(h.m.screen)
	}
	h.press("right", "esc", "esc", "down", "up")
	if h.loads.calls != 1 {
		t.Fatal(h.loads.calls)
	}
}

// The tools page says what is missing before tools can load, and loads
// them once it is not: there is no save step in between.
func TestToolsPageSaysWhatIsMissing(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.details("Design", "Paper")
	h.do("Choose tools")
	h.contains("Paper is off: Left back, then Space turns it on.")
	h.press("esc", "space")
	h.do("Choose tools")
	if h.loads.calls != 1 || h.loads.ids[0] != idPaper {
		t.Fatal(h.loads.calls, h.loads.ids)
	}
	h.lacks("Save first", "save")
	h.press("esc", "esc")
	h.details("Development", "Reviewer")
	h.do("Choose tools")
	h.contains("Reviewer changed upstream: Left back, then Space accepts it.")
	if h.loads.calls != 1 {
		t.Fatal(h.loads.calls)
	}

	hidden := newHarness(t, seedFixture, 120, 35, func(o *Options) { o.LoadTools = nil })
	hidden.details("Other", "Excalidraw")
	hidden.press("enter")
	if hidden.m.screen != screenTools {
		t.Fatal(hidden.m.screen)
	}
	hidden.lacks("Loading", "Turn it on")
}

// A load that asks for a sign-in is a sign-in, not a failure, as when
// setting up: the tools page, the Status and the exit summary all say so.
// Where setup can sign in, s on the tools page does it and the tools show
// right there; elsewhere mcparcel auth does.
func TestToolsNeedingSignInPointAtSetUp(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.loads.err = output.NewError("auth_required", nil)
	h.details("Other", "Excalidraw")
	h.press("enter")
	h.contains("Needs a sign-in first: run mcparcel auth excalidraw in a terminal.")
	h.lacks("Could not connect.", "could not start")
	h.press("esc", "esc")
	h.lineHas("Excalidraw", "[~]", "run mcparcel auth excalidraw")
	if step, ok := stepFor(h.m.Result(), idExcali); !ok || step.Need != NeedSignIn || step.Command != "mcparcel auth excalidraw" {
		t.Fatal(step)
	}

	oauth := finishHarness(t, seedFinish, 120, 35)
	oauth.turnOn("Calendar")
	oauth.loads.errs[idCalendar] = output.NewError("auth_required", nil)
	oauth.details("Other", "Calendar")
	oauth.do("Choose tools")
	oauth.contains("Needs a sign-in first: s signs you in.")
	oauth.press("?")
	oauth.contains("Help")
	oauth.press("esc")
	if oauth.m.screen != screenTools {
		t.Fatal(oauth.m.screen)
	}
	if step, ok := stepFor(oauth.m.Result(), idCalendar); !ok || step.Need != NeedSignIn || step.Command != "" {
		t.Fatal(step)
	}
	oauth.fin.mu.Lock()
	oauth.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	oauth.fin.mu.Unlock()
	oauth.press("s")
	if _, signIns, signed := oauth.fin.counts(); signIns != 1 || !slices.Equal(signed, []string{idCalendar}) || oauth.m.screen != screenTools {
		t.Fatal(signIns, signed, oauth.m.screen)
	}
	oauth.contains("Loaded less than 1m ago (2 tools).", "[+] draw")
	oauth.press("q")
	if !oauth.quit {
		t.Fatal("q does not quit the tools page")
	}
}

func TestToolsLoadFailureKeepsMetadata(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.loads.err = output.NewError("connection_failed", nil)
	h.details("Other", "Excalidraw")
	h.press("enter")
	h.contains("Could not start Excalidraw.", "mcparcel doctor shows why.")
	h.lacks("configuration", "Could not connect.")
	h.press("esc")
	h.contains("[-] could not start", "> Start again", "Could not start Excalidraw.", "Runs  this device - excalidraw-fixture")
	h.loads.err = nil
	h.do("Choose tools") // opening the page again tries again
	h.contains("> [+] draw")
	h.lacks("Could not start")
}

// A tool toggle is written at once, as tools enable|disable would.
func TestToolToggleIsWrittenAtOnce(t *testing.T) {
	h := toolHarness(t, seedFixture)
	before := h.disk().Selections.Revision
	h.details("Other", "Excalidraw")
	h.press("enter", "down", "space")
	h.contains("[+] draw", "> [ ] erase", "Tool policy: all tools, 1 off")
	disk := h.disk()
	if disk.Selections.Revision != before+1 || !slices.Equal(disk.Selections.Connections[idExcali].DisabledTools, []string{"erase"}) {
		t.Fatal(disk.Selections.Revision, disk.Selections.Connections[idExcali])
	}
	h.press("space")
	h.contains("> [+] erase")
	if got := h.disk().Selections.Connections[idExcali].DisabledTools; len(got) != 0 {
		t.Fatal(got)
	}
	h.press("u")
	h.contains("> [ ] erase", "Tool erase of Excalidraw is off again.")
}

// A tool disabled before setup opened stays listed while the tools load,
// and after it is turned back on.
func TestDisabledToolsListedBeforeLoad(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			sel := s.Selections.Connections[idExcali]
			sel.DisabledTools = []string{"old"}
			s.Selections.Connections[idExcali] = sel
		})
	}
	h := toolHarness(t, seed)
	h.details("Other", "Excalidraw")
	h.hold = true
	h.press("enter")
	h.contains("> [ ] old (disabled)", "Loading the tools of Excalidraw...")
	h.held, h.hold = nil, false
	h.m.cancelLoad()
	h.press("space")
	h.contains("> [+] old")
	if got := h.disk().Selections.Connections[idExcali].DisabledTools; len(got) != 0 {
		t.Fatal(got)
	}
}

func TestToolTextSanitized(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.loads.items = []string{`{"name":"bad\u001b[31mname","description":"desc\u001b]0;title\u0007 ok\nhidden"}`, `{"description":"no name"}`, `not json`}
	h.details("Other", "Excalidraw")
	h.press("enter")
	if strings.ContainsRune(h.view(), 0x1b) || strings.ContainsRune(h.view(), 0x07) {
		t.Fatalf("%q", h.view())
	}
	checkFits(t, h, 120, 35, "tools")
	h.contains("(1 tool)")
	h.lacks("hidden", "no name")
	h.press("space")
	if got := h.disk().Selections.Connections[idExcali].DisabledTools; !slices.Equal(got, []string{"bad\x1b[31mname"}) {
		t.Fatalf("%q", got)
	}
}

func TestQuitCancelsPendingLoad(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.details("Other", "Excalidraw")
	h.hold = true
	h.press("enter")
	h.contains("Loading the tools of Excalidraw... 1Password may ask you to approve.")
	h.press("ctrl+c")
	h.flush()
	if !h.quit || h.loads.calls != 1 || !h.loads.canceled[0] {
		t.Fatal(h.quit, h.loads.calls, h.loads.canceled)
	}

	back := toolHarness(t, seedFixture)
	back.details("Other", "Excalidraw")
	back.hold = true
	back.press("enter", "esc")
	back.flush()
	if back.loads.calls != 1 || !back.loads.canceled[0] || back.m.loaded[idExcali].tools != nil || back.m.loading != "" {
		t.Fatal(back.loads.calls, back.loads.canceled, back.m.loaded)
	}
	back.press("enter") // opening it again loads again
	if back.loads.calls != 2 {
		t.Fatal(back.loads.calls)
	}
}
