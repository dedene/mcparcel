package ui

import (
	"slices"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// seedStatus turns on servers that each stop at a different Status: Docs
// misses its input, Notes its 1Password profile (two could match), Maps
// its command, Calendar a sign-in, Mail can be read only with a prompt,
// Search and Wiki need nothing. Paper is off and its command is missing.
func seedStatus(t *testing.T, store *config.Store) {
	seedFinish(t, store)
	mustUpdate(t, store, func(s *config.State) {
		s.Local.CredentialProfiles["other"] = config.Profile{Mode: "desktop", Account: "Other account"}
		s.Personal.Connections["docs"] = config.Connection{
			Label:     "Docs",
			Inputs:    map[string]config.Input{"space": {Kind: "string", Description: "Space"}},
			Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("docs-fixture"), Args: []config.Value{{Input: &config.InputRef{Input: "space"}}}}},
		}
		s.Selections.Connections[idNotes] = config.Selection{Enabled: true, Inputs: map[string]string{"workspace": "team"}}
		for _, id := range []string{idSpace, idMaps, idCalendar, idMail, idSearch, idWiki} {
			s.Selections.Connections[id] = config.Selection{Enabled: true}
		}
	})
}

const idSpace = "local:docs" // Docs: a personal server with an input

func statusHarness(t *testing.T, opts ...func(*Options)) *harness {
	t.Helper()
	missing := func(o *Options) {
		o.CommandAvailable = func(command, _ string) bool { return command != "maps-fixture" && command != "paper-fixture" }
	}
	h := finishHarness(t, nil, 120, 35, append([]func(*Options){missing}, opts...)...)
	seedStatus(t, h.store)
	h.fin.mu.Lock()
	h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	h.fin.needs[idMail] = SignInState{Possible: true, Unknown: true}
	h.fin.mu.Unlock()
	h.reread()
	h.send(resize(120, 35)) // reads the stored sign-ins of what is on
	return h
}

// The Status column shows the first thing that applies, in order: a
// review, unavailable, an input, a 1Password profile, a missing command, a
// sign-in, an unreadable sign-in, the tools, or that it starts on first
// use. A row that is off shows only what would keep it from running.
func TestStatusOrder(t *testing.T) {
	h := statusHarness(t)
	h.selectTab("Other")
	h.lineHas("Calendar", "[~]", "sign in")
	h.lineHas("Mail", "[x]", "sign-in unknown")
	h.lineHas("Maps", "[-]", "maps-fixture not installed")
	h.lineHas("Search", "[x]", "starts on first use")
	h.lineHas("local:gone", "[-]", "unavailable")
	h.lineHas("Docs", "[~]", "needs space")
	h.selectTab("Research")
	h.lineHas("Notes", "[~]", "link 1Password")
	h.selectTab("Development")
	h.lineHas("Reviewer", "[~]", "review change")
	h.lineHas("Paper", "[ ]", "paper-fixture not installed")
	h.selectRow("Paper")
	h.paneContains("[ ] off - paper-fixture not installed", "[*] Install paper-fixture")
	h.contains("9 on - 6 need a step")
	h.selectTab("Other")
	h.selectRow("Calendar")
	h.paneContains("[~] needs a sign-in", "[*] Sign in with your browser", "[.] Tools load after sign-in")

	h.press("s") // Maps and Vault wait on the user; the rest start
	if got := slices.Sorted(slices.Values(h.loads.ids)); !slices.Equal(got, []string{idCalendar, idMail, idSearch, idWiki}) {
		t.Fatal(got)
	}
	h.lineHas("Calendar", "[x]", "2 tools")
	h.lineHas("Mail", "[x]", "2 tools")
	h.selectRow("Calendar")
	h.paneContains("[+] Signed in", "[+] 2 tools loaded")
	r := h.m.Result()
	var needs []Need
	for _, step := range r.Steps {
		needs = append(needs, step.Need)
	}
	if r.On != 9 || r.Ready != 4 || !slices.Equal(needs, []Need{NeedInput, NeedCommand, NeedProfile, NeedReview, NeedUnavailable}) {
		t.Fatal(r)
	}
	if r.Steps[1] != (Step{ID: idMaps, Label: "Maps", Need: NeedCommand, Detail: "maps-fixture"}) || r.Steps[0].Detail != "space" {
		t.Fatal(r.Steps)
	}
}

// Stored sign-ins are read once a session, for the servers that are on,
// and again for a URL that changed; a sign-in keeps what it found. Without
// a reader nothing is read and no sign-in shows.
func TestStoredSignInReadOnceASession(t *testing.T) {
	h := statusHarness(t)
	checks, _, _ := h.fin.counts()
	if checks != 2 { // Calendar and Mail: Search has no OAuth, the rest no HTTP
		t.Fatal(checks)
	}
	h.selectTab("Other")
	h.selectRow("Calendar")
	h.press("space", "space", "down", "up", "right", "left")
	if checks, _, _ := h.fin.counts(); checks != 2 {
		t.Fatal("read again while browsing", checks)
	}
	mustUpdate(t, h.store, func(s *config.State) {
		c := s.Personal.Connections["calendar"]
		c.Transport.HTTP.URL = config.Literal("https://calendar.example/v2")
		s.Personal.Connections["calendar"] = c
	})
	h.reread()
	h.lineHas("Calendar", "review change") // a changed personal server needs a review
	h.selectRow("Calendar")
	h.press("space")
	if checks, _, _ := h.fin.counts(); checks != 3 {
		t.Fatal("a changed URL was not read again", checks)
	}

	none := statusHarness(t, func(o *Options) { o.StoredSignIn = nil })
	none.selectTab("Other")
	none.lineHas("Calendar", "[x]", "starts on first use")
	if checks, _, _ := none.fin.counts(); checks != 0 {
		t.Fatal(checks)
	}
}

// s looks for commands again, so one installed since shows; a server page
// sets up its server once it is found.
func TestSetUpLooksForCommandsAgain(t *testing.T) {
	installed := false
	h := statusHarness(t, func(o *Options) {
		o.CommandAvailable = func(command, _ string) bool { return command != "maps-fixture" || installed }
	})
	h.selectTab("Other")
	h.lineHas("Maps", "not installed")
	installed = true
	h.lineHas("Maps", "not installed") // looked up once a session
	h.press("s")
	h.lineHas("Maps", "[x]", "2 tools")
}

// A run stopped before a server settles leaves it as it was before the
// run: what an earlier run found no longer holds. A server turned off
// after it failed has nothing left: the exit summary leaves it out.
func TestSetUpOutcomesFollowTheLatestRun(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	h.loads.errs[idMaps] = output.NewError("connection_failed", nil)
	h.turnOn("Maps")
	h.press("s")
	h.lineHas("Maps", "could not start")
	if step, ok := stepFor(h.m.Result(), idMaps); !ok || step.Need != NeedFailed {
		t.Fatal(step)
	}
	h.hold = true
	h.press("s", "q")
	h.flush()
	h.lineHas("Maps", "[x]", "starts on first use")
	h.press("s")
	h.press("space") // Maps off
	if _, ok := stepFor(h.m.Result(), idMaps); ok {
		t.Fatal("a server turned off is still listed")
	}
	h.lineHas("Maps", "[ ]")
}

// A failure on the tools page shows in the Status too, and the tools page
// says to sign in where a sign-in is what it needs.
func TestToolsFailureShowsInStatus(t *testing.T) {
	h := toolHarness(t, seedFixture)
	h.loads.err = output.NewError("timeout", nil)
	h.details("Other", "Excalidraw")
	h.press("enter", "esc", "esc")
	h.lineHas("Excalidraw", "[-]", "timed out")
}

// The tools page of a server whose stored sign-in is missing says so and
// loads nothing: the load would only ask for the sign-in.
func TestToolsPageWaitsForAKnownSignIn(t *testing.T) {
	h := statusHarness(t)
	h.details("Other", "Calendar")
	h.do("Choose tools")
	h.contains("Needs a sign-in first: s signs you in.")
	if h.loads.calls != 0 {
		t.Fatal(h.loads.calls)
	}
}

// A stored sign-in read that started before a sign-in settled it is older
// than the sign-in: it changes nothing.
func TestOlderSignInReadIsIgnored(t *testing.T) {
	h := statusHarness(t)
	row := h.m.live.Effective().Connections[idCalendar]
	key := signInKey(row)
	h.m.signInPending[key] = true
	h.m.keepSignIn(idCalendar, SignInState{Possible: true})
	h.send(signInsMsg{states: map[string]SignInState{key: {Possible: true, Needed: true}}})
	h.selectTab("Other")
	h.lineHas("Calendar", "[x]", "starts on first use")
}

// The exit summary is complete however soon setup ends: a stored sign-in
// whose read has not returned yet is read again.
func TestExitReadsSignInsStillPending(t *testing.T) {
	h := finishHarness(t, nil, 120, 35)
	seedStatus(t, h.store)
	h.fin.mu.Lock()
	h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	h.fin.mu.Unlock()
	h.reread()
	h.hold = true
	h.send(resize(120, 35)) // the read starts; setup ends before it returns
	if step, ok := stepFor(h.m.Result(), idCalendar); !ok || step.Need != NeedSignIn || step.Command != "" {
		t.Fatal(step, h.m.Result())
	}
}

// A command is looked up on the PATH its connection sets, as the runtime
// does; one whose PATH comes from an input is not checked. The tools page
// of a server whose command is not installed says so and loads nothing.
func TestCommandLookedUpOnTheConnectionPath(t *testing.T) {
	var asked []string
	h := statusHarness(t, func(o *Options) {
		o.CommandAvailable = func(command, search string) bool {
			asked = append(asked, command+"@"+search)
			return command != "maps-fixture" || search == "/opt/maps/bin"
		}
	})
	h.details("Other", "Maps")
	h.do("Choose tools")
	h.contains("maps-fixture is not installed. Install it, then press s.")
	if h.loads.calls != 0 {
		t.Fatal(h.loads.calls)
	}
	h.press("esc", "esc")
	for _, tc := range []struct {
		path config.Value
		want string
	}{
		{config.Literal("/opt/maps/bin"), "[x]"},
		{config.Value{Input: &config.InputRef{Input: "bin"}}, "[x]"},
		{config.Literal("/usr/bin"), "maps-fixture not installed"},
	} {
		mustUpdate(t, h.store, func(s *config.State) {
			c := s.Personal.Connections["maps"]
			c.Inputs = map[string]config.Input{"bin": {Kind: "string", Description: "Bin", Default: new("/opt/maps/bin")}}
			c.Transport.Stdio.Env = map[string]config.Value{"PATH": tc.path}
			s.Personal.Connections["maps"] = c
		})
		mustUpdate(t, h.store, func(s *config.State) { s.Selections.Connections[idMaps] = config.Selection{Enabled: true} })
		h.reread()
		h.lineHas("Maps", tc.want)
	}
	if !slices.Contains(asked, "maps-fixture@/opt/maps/bin") || !slices.Contains(asked, "maps-fixture@") {
		t.Fatal(asked)
	}
}

// The bottom line offers s when s takes a step, here a sign-in.
func TestBottomLineOffersSetUpForASignIn(t *testing.T) {
	h := statusHarness(t)
	h.contains("6 servers need a step.  s set up all")
	compact := statusHarness(t)
	compact.send(resize(80, 24))
	mustUpdate(t, compact.store, func(s *config.State) {
		for _, id := range []string{idSpace, idMaps, idNotes, idReview, "local:gone"} {
			delete(s.Selections.Connections, id)
		}
	})
	compact.reread()
	compact.contains("1 needs a step. s set up all")
}
