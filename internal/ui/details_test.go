package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func fixedAges(seconds float64) func(*Options) {
	return func(o *Options) {
		o.SourceAges = func(state config.State) ([]output.SourceMetadata, error) {
			out := []output.SourceMetadata{}
			for _, source := range state.Local.Sources {
				out = append(out, output.SourceMetadata{Source: source, CacheAgeSeconds: seconds})
			}
			return out, nil
		}
	}
}

// pick moves the server page's cursor to the action labeled label.
func (h *harness) pick(label string) {
	h.t.Helper()
	actions := h.m.actions(h.detailRow())
	for i, a := range actions {
		if a.label == label {
			h.m.detailCursor = i
			return
		}
	}
	names := make([]string, 0, len(actions))
	for _, a := range actions {
		names = append(names, a.label)
	}
	h.t.Fatalf("no action %q in %v", label, names)
}

// do picks the action labeled label and presses Enter.
func (h *harness) do(label string) {
	h.t.Helper()
	h.pick(label)
	h.press("enter")
}

// The page opens on the next step for the row's state, then lists what
// else it can do; the To do and the plain facts follow.
func TestServerPageLeadsWithTheNextStep(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, fixedAges(7200))
	h.details("Research", "Notes")
	h.contains("Research / Notes", "[ ] off - needs workspace", "Team notes.",
		"> Turn on", "asks for what it misses first", "  Set workspace", "  Link 1Password profile", "  Choose tools",
		"To do", "[.] Tools load on first use",
		"Runs  this device - notes-fixture", "From  acmeco/mcp-catalog - Research")
	// The actions are the steps left; the To do does not list them again.
	h.lacks("[*] Turn it on", "[*] Set workspace", "[*] Link a 1Password profile")
	h.lacks("ID:", "Transport", "Schema", "revision", "unsaved", "SECRET-MARKER", "Turn off", "Remove")
	h.press("esc")
	if h.m.screen != screenList {
		t.Fatal(h.m.screen)
	}
	h.details("Development", "Reviewer")
	h.contains("[~] needs a review", "> Accept the change", "Turn off", "To do", "[+] On")
	h.lacks("[*] Accept the change")
	h.press("enter")
	h.contains("[x] ready", "> Choose tools", "Start", "Turn off")
	if sel := h.disk().Selections.Connections[idReview]; !sel.Enabled || sel.ReviewRequired {
		t.Fatal(sel)
	}
}

// i shows the technical facts: the ID, the source with its commit, the
// transport, the tool policy and the 1Password requirement.
func TestServerPageTechnicalFacts(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			catalog := s.Catalogs[fixSource]
			notes := catalog.Connections["notes"]
			notes.ToolPolicy = &config.ToolPolicy{Allow: &[]string{"a", "b", "c"}, Deny: []string{"d"}}
			catalog.Connections["notes"] = notes
			s.Catalogs[fixSource] = catalog
			s.Local.Sources[0].Commit = strings.Repeat("b", 40)
			s.Selections.Connections[idNotes] = config.Selection{Inputs: map[string]string{"workspace": "docs"}, CredentialProfile: "work", DisabledTools: []string{"b"}}
		})
	}
	h := newHarness(t, seed, 120, 35, fixedAges(7200))
	h.details("Research", "Notes")
	h.contains("> Turn on", "Choose tools", "1 tool off", "Change workspace", "docs", "Change 1Password profile", "work",
		"[+] workspace is set", "[+] 1Password: work")
	h.lacks("ID", "Tool policy")
	h.press("i")
	h.contains("ID           "+idNotes, "Source       acmeco/mcp-catalog, commit bbbbbbb, synced 2h ago",
		"Transport    stdio: notes-fixture (2 arguments)", "Tool policy  3 allowed by source, 1 denied by source, 1 off",
		"1Password    needs profile team, linked to work")
	h.lacks("SECRET-MARKER", "Fixture account")
	h.press("i")
	h.lacks("Tool policy")
}

// visitAll opens every screen for every row and asserts no marker shows.
func visitAll(t *testing.T, h *harness, markers []string) {
	t.Helper()
	check := func(where string) {
		t.Helper()
		for _, marker := range markers {
			if strings.Contains(h.view(), marker) {
				t.Fatalf("%s shows %q:\n%s", where, marker, h.plain())
			}
		}
	}
	h.m.technical = true
	for _, tb := range tabs(h.m.live.Effective()) {
		h.selectTab(tb.label)
		check("list " + tb.label)
		for i := range h.m.rows() {
			h.press("home")
			for range i {
				h.press("down")
			}
			check("row")
			h.press("enter")
			check("details")
			row := h.detailRow()
			for j, a := range h.m.actions(row) {
				h.m.detailCursor = j
				switch a.kind {
				case actInput, actProfile, actTools, actEdit:
					h.press("enter")
					check(fmt.Sprintf("%s action %q", row.ID, a.label))
					h.press("esc")
				}
			}
			h.press("esc")
		}
	}
	h.press("?")
	check("help")
	h.press("esc")
}

func (h *harness) detailRow() config.EffectiveConnection { return h.m.detailRow() }

func TestDetailsNeverShowsSecretsOrURLPath(t *testing.T) {
	h := newHarness(t, seedSecrets, 120, 35, fixedAges(60))
	visitAll(t, h, secretMarkers)
	if h.m.screen != screenList {
		t.Fatalf("visit ended on screen %d:\n%s", h.m.screen, h.plain())
	}
	h.details("Other", "Hook")
	h.contains("Runs  remote - hook.example", "Transport    HTTP: hook.example")
	h.press("esc")
	h.details("Other", "Vault")
	h.contains("Runs  this device - vault-fixture", "Transport    stdio: vault-fixture (2 arguments)", "Change endpoint", "https://in.example/...")
	small := newHarness(t, seedSecrets, 60, 20)
	visitAll(t, small, secretMarkers)
}

// Set <input> edits the input right on the page; Enter writes it at once
// and moves the cursor to the next step.
func TestSetInputOnThePage(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Research", "Notes")
	h.do("Set workspace")
	if !h.m.editing || h.m.screen != screenDetails {
		t.Fatal(h.m.editing, h.m.screen)
	}
	h.contains("> Set workspace", "_", "Workspace", "Enter set it")
	h.typeText("docs")
	h.press("enter")
	if h.m.editing || h.writes() != 1 || h.m.detailCursor != 0 {
		t.Fatal(h.m.editing, h.writes(), h.m.detailCursor)
	}
	h.contains("workspace is set.", "> Turn on", "Change workspace", "[+] workspace is set")
	h.lacks("Set workspace")
	h.do("Change workspace")
	h.contains("> Change workspace", "docs_")
	h.press("esc")
	if h.m.editing || h.writes() != 1 {
		t.Fatal("Esc wrote the input")
	}
	if sel := h.disk().Selections.Connections[idNotes]; sel.Inputs["workspace"] != "docs" || sel.Enabled {
		t.Fatal(sel)
	}
}

func TestURLInputShowsHostOnlyAndEmptyKeeps(t *testing.T) {
	h := newHarness(t, seedSecrets, 120, 35)
	h.details("Other", "Vault")
	h.contains("Change endpoint", "https://in.example/...")
	h.do("Change endpoint")
	h.contains("Now https://in.example/...; an empty Enter keeps it.", "empty keeps it")
	h.press("enter")
	h.contains("Kept the current value.")
	if h.m.editing || h.writes() != 0 {
		t.Fatal(h.m.editing, h.writes())
	}
	h.do("Change endpoint")
	h.paste("https://new.example/other/path")
	h.press("enter")
	h.contains("https://new.example/...")
	h.lacks("other/path")
	if h.m.live.State().Selections.Connections["local:vault"].Inputs["endpoint"] != "https://new.example/other/path" {
		t.Fatal(h.m.live.State().Selections.Connections["local:vault"])
	}
}

func TestInputValidationErrorInline(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		mustUpdate(t, store, func(s *config.State) {
			s.Personal.Connections["files"] = config.Connection{
				Label: "Files", Inputs: map[string]config.Input{"root": {Kind: "path", Description: "Root"}},
				Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("files-fixture"), Args: []config.Value{{Input: &config.InputRef{Input: "root"}}}}},
			}
		})
	}
	h := newHarness(t, seed, 120, 35)
	h.details("Other", "Files")
	h.do("Set root")
	h.typeText("relative/WRONG-VALUE")
	h.press("enter")
	h.contains("Invalid: selections.connections.local:files.inputs.root: absolute path required.")
	if !h.m.editing || h.writes() != 0 {
		t.Fatal(h.m.editing, h.writes())
	}
	for _, line := range strings.Split(h.plain(), "\n") {
		if strings.Contains(line, "Invalid") && strings.Contains(line, "WRONG-VALUE") {
			t.Fatal("error repeats the value")
		}
	}
	h.press("ctrl+u")
	h.typeText("/srv/files")
	h.press("enter")
	if h.m.editing || h.writes() != 1 {
		t.Fatal(h.m.editing, h.writes())
	}
}

// withProfiles adds local profiles to the fixture, which has only "work".
func withProfiles(names ...string) func(*testing.T, *config.Store) {
	return func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) {
			for _, name := range names {
				s.Local.CredentialProfiles[name] = config.Profile{Mode: "desktop", Account: "Fixture account"}
			}
		})
	}
}

func TestBindProfileFromExistingProfiles(t *testing.T) {
	h := newHarness(t, withProfiles("other"), 120, 35)
	h.details("Research", "Notes")
	h.do("Link 1Password profile")
	h.contains("Research / Notes / 1Password profile", "Choose a 1Password profile for Notes.", "> other", "  work")
	h.lacks("Fixture account")
	h.press("down", "enter")
	if h.m.screen != screenDetails || h.m.detailCursor != 0 {
		t.Fatal(h.m.screen, h.m.detailCursor)
	}
	h.contains("Linked 1Password profile work.", "Change 1Password profile", "[+] 1Password: work")
	if h.disk().Selections.Connections[idNotes].CredentialProfile != "work" {
		t.Fatal(h.m.live.State().Selections.Connections[idNotes])
	}
	h.do("Change 1Password profile")
	h.contains("> other", "  work (linked)")
}

func TestSingleProfileLinksWithoutPicker(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Research", "Notes")
	h.do("Link 1Password profile")
	if h.m.screen != screenDetails {
		t.Fatal(h.m.screen)
	}
	h.contains("Change 1Password profile", "Linked 1Password profile work.")
	h.lacks("Choose a 1Password profile", "Fixture account")
	if sel := h.disk().Selections.Connections[idNotes]; sel.CredentialProfile != "work" || sel.Enabled {
		t.Fatal(sel)
	}
}

func TestNoProfilesShowsProfileSetHint(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		mustUpdate(t, store, func(s *config.State) { delete(s.Local.CredentialProfiles, "work") })
	}
	h := newHarness(t, seed, 120, 35)
	h.details("Research", "Notes")
	h.do("Link 1Password profile")
	h.contains("No profiles yet. Create one: mcparcel config profile set <name> --file <profile.json>.")
	h.press("enter", "esc")
	if h.m.screen != screenDetails || h.writes() != 0 {
		t.Fatal(h.m.screen, h.writes())
	}
}

// Turn on, from the page, of a row that misses something asks for it as
// Space does, then comes back to the page.
func TestTurnOnFromThePageAsksThenReturns(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Research", "Notes")
	h.press("enter")
	if h.m.screen != screenField {
		t.Fatal(h.m.screen)
	}
	h.contains("Research / Notes / workspace", "Notes needs workspace and a 1Password profile. Esc keeps it off.")
	h.typeText("docs")
	h.press("enter")
	if h.m.screen != screenDetails || h.m.detailID != idNotes || h.writes() != 1 {
		t.Fatal(h.m.screen, h.m.detailID, h.writes())
	}
	h.contains("Linked 1Password profile work. Notes is on.", "[x] ready", "> Choose tools", "Turn off")
	h.press("space")
	h.contains("[ ] off", "> Turn on")
	h.lacks("asks for what it misses first")
	h.press("enter")
	if h.m.screen != screenDetails || h.writes() != 3 {
		t.Fatal(h.m.screen, h.writes())
	}
	h.contains("[x] ready", "> Choose tools")
}

// Sign in, Start and Test again set this server up, as s does on its page.
func TestServerPageSignsInAndStarts(t *testing.T) {
	h := statusHarness(t)
	h.details("Other", "Calendar")
	h.contains("Other / Calendar", "[~] needs a sign-in", "> Sign in to Calendar", "opens your browser",
		"Choose tools", "all, after sign-in", "[+] On", "[.] Tools load after sign-in", "Runs  remote - calendar.example")
	h.lacks("[*] Sign in with your browser")
	h.press("enter")
	_, signIns, signed := h.fin.counts()
	if signIns != 1 || len(signed) != 1 {
		t.Fatal(signIns, signed)
	}
	h.contains("Calendar is ready.", "[x] ready - 2 tools", "> Choose tools", "2 of 2 on", "Test again", "last ok just now",
		"Tools", "draw", "Draw a diagram.", "erase")
	h.lacks("To do", "Second line.")
	// Off, its one step is the action above: the tools still show, under no
	// To do of done items only.
	h.press("space")
	h.contains("[ ] off", "> Turn on", "Tools", "draw")
	h.lacks("To do", "2 tools loaded")
	h.press("u")
	h.contains("[x] ready - 2 tools")
	h.press("esc")
	h.details("Other", "Search")
	h.contains("> Choose tools", "Start", "loads its tools")
	h.do("Start")
	h.contains("Search is ready.", "Test again")

	headless := statusHarness(t, func(o *Options) { o.SignIn = nil })
	headless.details("Other", "Calendar")
	headless.contains("> Sign in to Calendar", "run mcparcel auth calendar")
	headless.press("enter")
	headless.contains("Setup can't sign in here: run mcparcel auth calendar in a terminal.")
	if _, signIns, _ := headless.fin.counts(); signIns != 0 || headless.loads.calls != 0 {
		t.Fatal("a headless sign-in started something")
	}
}

// A command that is not found says so first; Enter looks for it again.
func TestServerPageCommandNotInstalled(t *testing.T) {
	found := false
	h := statusHarness(t, func(o *Options) {
		o.CommandAvailable = func(command, _ string) bool { return command != "maps-fixture" || found }
	})
	h.details("Other", "Maps")
	h.contains("[-] maps-fixture not installed", "> maps-fixture not installed", "Enter looks again", "[+] On")
	h.lacks("Start  ", "[*] Install maps-fixture")
	h.press("enter")
	h.contains("maps-fixture is still not installed: it is not found on PATH.", "> maps-fixture not installed")
	found = true
	h.press("enter")
	h.contains("Found maps-fixture.", "[x] ready", "> Choose tools", "Start")
}

// Remove removes a personal connection and turns it off, as local remove
// does; u brings both back.
func TestRemovePersonalAndUndo(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Other", "Excalidraw")
	h.contains("Edit", "Remove", "u brings it back")
	h.do("Remove")
	if h.m.screen != screenList {
		t.Fatal(h.m.screen)
	}
	h.contains("Removed Excalidraw. u brings it back.")
	disk := h.disk()
	if _, ok := disk.Personal.Connections["excalidraw"]; ok || disk.Selections.Connections[idExcali].Enabled {
		t.Fatal("remove did not match local remove", disk.Personal.Connections, disk.Selections.Connections[idExcali])
	}
	if h.m.statusOf(h.m.live.Effective().Connections[idExcali]).step() {
		t.Fatal("a removed connection still counts as a step")
	}
	h.press("u")
	h.contains("Excalidraw is back.")
	if _, ok := h.disk().Personal.Connections["excalidraw"]; !ok || !h.disk().Selections.Connections[idExcali].Enabled {
		t.Fatal("undo did not bring it back on")
	}
}

// A shared definition has no Edit or Remove; a personal one opens its form.
func TestSharedDefinitionReadOnly(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Design", "Paper")
	h.lacks("Edit", "Remove")
	h.press("esc")
	h.details("Other", "Excalidraw")
	h.do("Edit")
	if h.m.screen != screenForm {
		t.Fatal(h.m.screen)
	}
	h.contains("Other / Excalidraw / Edit")
}
