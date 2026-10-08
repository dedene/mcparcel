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

func TestConnectAndLoadToolsCallsLoaderOnce(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.details("Other", "Excalidraw")
	h.press("t", "esc", "down", "up", "esc")
	if h.loads.calls != 0 {
		t.Fatal("browsing loaded tools", h.loads.calls)
	}
	h.details("Other", "Excalidraw")
	h.press("l")
	if h.loads.calls != 1 || h.loads.ids[0] != idExcali || h.m.screen != screenTools {
		t.Fatal(h.loads.calls, h.loads.ids, h.m.screen)
	}
	h.contains("Loaded this session less than 1m ago (2 tools).", "> [x] draw", "Draw a diagram.", "[x] erase")
	h.lacks("Second line.", "Press l to connect")
	h.press("esc")
	h.contains("Schema: loaded this session less than 1m ago")
	h.press("t", "esc", "esc", "down", "up")
	if h.loads.calls != 1 {
		t.Fatal(h.loads.calls)
	}
}

func TestConnectRequiresSavedEnabledConnection(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.details("Design", "Paper")
	h.press("l")
	h.contains("Paper is off. Turn it on and save first: the runtime uses the saved configuration.")
	h.press("esc", "space", "l")
	h.contains("Save first: the runtime uses the saved configuration.")
	h.press("esc", "esc", "ctrl+s")
	h.details("Design", "Paper")
	h.press("l")
	if h.loads.calls != 1 || h.loads.ids[0] != idPaper {
		t.Fatal(h.loads.calls, h.loads.ids)
	}
	h.press("esc", "esc")
	h.details("Development", "Reviewer")
	h.press("l")
	h.contains("Reviewer needs review. Press Space to accept it, then save.")
	if h.loads.calls != 1 {
		t.Fatal(h.loads.calls)
	}

	hidden := newHarness(t, seedFixture, 120, 35, func(o *Options) { o.LoadTools = nil })
	hidden.details("Other", "Excalidraw")
	hidden.lacks("Connect and load tools", "l: connect")
	hidden.press("l")
	if hidden.m.screen != screenDetails {
		t.Fatal(hidden.m.screen)
	}
	hidden.press("t")
	hidden.lacks("Press l")
}

func TestConnectAllowedWithPendingToolToggles(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.details("Other", "Excalidraw")
	h.press("l", "space")
	if h.m.draft.Changes() != 1 || h.m.draft.Pending(idExcali) {
		t.Fatal(h.m.draft.Changes(), h.m.draft.Pending(idExcali))
	}
	h.press("l")
	if h.loads.calls != 2 {
		t.Fatal(h.loads.calls)
	}
	h.contains("> [ ] draw")
}

func TestConnectAuthRequiredShowsLoginHint(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.loads.err = output.NewError("auth_required", nil)
	h.details("Other", "Excalidraw")
	h.press("l")
	h.contains("Credential authorization is required.", "Run mcparcel auth local:excalidraw.")
	h.lacks("Could not connect.")
}

func TestConnectFailureKeepsMetadata(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.loads.err = output.NewError("connection_failed", nil)
	h.details("Other", "Excalidraw")
	h.press("l")
	h.contains("Could not connect to the MCP server.", "Check the connection configuration and prerequisites.",
		"Could not connect. Configured MCPs on this device still work.")
	h.press("esc")
	h.contains("Transport: stdio: excalidraw-fixture (0 arguments)", "Schema: none cached", "Enabled: [x]   State: Ready")
	h.loads.err = nil
	h.press("l")
	h.contains("> [x] draw")
	h.lacks("Could not connect")
}

func TestToolToggleStagesToolsDisable(t *testing.T) {
	h := toolHarness(t, seedFixture)
	before := h.disk().Selections.Revision
	h.details("Other", "Excalidraw")
	h.press("l", "down", "space")
	h.contains("[x] draw", "> [ ] erase", "Policy: all tools, 1 disabled by you")
	h.press("esc", "esc", "ctrl+s")
	disk := h.disk()
	if disk.Selections.Revision != before+1 || !slices.Equal(disk.Selections.Connections[idExcali].DisabledTools, []string{"erase"}) {
		t.Fatal(disk.Selections.Revision, disk.Selections.Connections[idExcali])
	}
	h.details("Other", "Excalidraw")
	h.press("t", "down", "space")
	h.contains("> [x] erase", "1 unsaved change")
}

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
	h.press("t")
	h.contains("> [ ] old (disabled)", "Press l to connect and load tools.")
	h.press("space")
	h.contains("> [x] old", "1 unsaved change")
	if h.loads.calls != 0 {
		t.Fatal(h.loads.calls)
	}
}

func TestToolTextSanitized(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.loads.items = []string{`{"name":"bad\u001b[31mname","description":"desc\u001b]0;title\u0007 ok\nhidden"}`, `{"description":"no name"}`, `not json`}
	h.details("Other", "Excalidraw")
	h.press("l")
	if strings.ContainsRune(h.view(), 0x1b) || strings.ContainsRune(h.view(), 0x07) {
		t.Fatalf("%q", h.view())
	}
	checkFits(t, h, 120, 35, "tools")
	h.contains("(1 tool)")
	h.lacks("hidden", "no name")
	h.press("space")
	if got := h.m.draft.State().Selections.Connections[idExcali].DisabledTools; !slices.Equal(got, []string{"bad\x1b[31mname"}) {
		t.Fatalf("%q", got)
	}
}

func TestQuitCancelsPendingLoad(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.details("Other", "Excalidraw")
	h.hold = true
	h.press("l")
	h.contains("Connecting to Excalidraw... Credentials may be requested.")
	h.press("ctrl+c")
	h.flush()
	if !h.quit || h.loads.calls != 1 || !h.loads.canceled[0] {
		t.Fatal(h.quit, h.loads.calls, h.loads.canceled)
	}

	back := toolHarness(t, seedFixture)
	back.details("Other", "Excalidraw")
	back.hold = true
	back.press("l", "esc")
	back.flush()
	if back.loads.calls != 1 || !back.loads.canceled[0] || back.m.loaded[idExcali].tools != nil || back.m.loading != "" {
		t.Fatal(back.loads.calls, back.loads.canceled, back.m.loaded)
	}
	back.press("t")
	back.contains("Press l to connect and load tools.")
}
