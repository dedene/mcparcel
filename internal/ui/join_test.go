package ui

import (
	"maps"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

const (
	joinSource = "github-77"
	idLint     = "github:fixture-owner/team#lint"
	idDocs     = "github:fixture-owner/team#docs"
	idCanvas   = "github:fixture-owner/team#canvas"
)

var joinFixture = Join{SourceID: joinSource, Repository: "fixture-owner/team", Commit: "bbbbbbb"}

func withJoin(o *Options) { j := joinFixture; o.Join = &j }

// seedJoinCatalog adds a second catalog that uses the join fields: a
// suggestion for vault, one for work (a local profile seedFixture already
// has) and two presets. Canvas needs an input, so a preset cannot turn it on.
func seedJoinCatalog(t *testing.T, store *config.Store) {
	t.Helper()
	seedJoinCatalogWith(t, store, func(*config.Catalog) {})
}

// seedJoinCatalogWith adds the join catalog after change.
func seedJoinCatalogWith(t *testing.T, store *config.Store, change func(*config.Catalog)) {
	t.Helper()
	mustUpdate(t, store, func(s *config.State) {
		s.Local.Sources = append(s.Local.Sources, config.Source{ID: joinSource, RepositoryID: 77, Owner: "fixture-owner", Repo: "team", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("b", 40)})
		c := config.Catalog{
			SchemaVersion: 1,
			MinVersion:    config.JoinMinVersion,
			Name:          "Team",
			CredentialProfiles: map[string]config.ProfileRequirement{
				"vault": {Description: "MCP Integrations", Suggest: &config.ProfileSuggestion{Mode: config.ProfileModeDesktop, Account: "team.1password.com"}},
				"work":  {Suggest: &config.ProfileSuggestion{Mode: config.ProfileModeDesktop, Account: "work.1password.com"}},
			},
			Presets: []config.Preset{
				{ID: "developer", Label: "Developer", Description: "Code and docs.", Connections: []string{"lint", "docs"}},
				{ID: "designer", Label: "Designer", Connections: []string{"canvas", "docs"}},
			},
			Connections: map[string]config.Connection{
				"lint":   {Label: "Lint", CredentialProfile: "vault", Transport: stdio("lint-fixture")},
				"docs":   {Label: "Docs", Transport: stdio("docs-fixture")},
				"canvas": {Label: "Canvas", Inputs: map[string]config.Input{"home": {Kind: "path", Description: "Home"}}, Transport: stdio("canvas-fixture")},
			},
		}
		change(&c)
		s.Catalogs[joinSource] = c
	})
}

func seedJoin(t *testing.T, store *config.Store) {
	t.Helper()
	seedFixture(t, store)
	seedJoinCatalog(t, store)
}

func TestJoinIntroWritesOnlyOnEnter(t *testing.T) {
	h := newHarness(t, seedJoin, 120, 35, withJoin)
	before := h.fileBytes()
	h.contains("MCParcel / Join", "Team - fixture-owner/team - bbbbbbb", "3 MCP servers picked by the team.",
		"This catalog asks for a 1Password profile: vault.", "  MCP Integrations",
		"Use this 1Password account? The catalog suggests it.", "Account: team.1password.com", "1 server uses it.")
	h.lacks("work.1password.com", "1Password work", "1Password for MCP") // no server uses work
	if !maps.EqualFunc(before, h.fileBytes(), func(a, b []byte) bool { return string(a) == string(b) }) {
		t.Fatal("showing the suggestion wrote files")
	}
	h.press("enter")
	got := h.disk().Local.CredentialProfiles["vault"]
	if got != (config.Profile{Mode: "desktop", Account: "team.1password.com", SessionDuration: "24h"}) {
		t.Fatalf("%+v", got)
	}
	if h.m.screen != screenPresets || h.writes() != 1 {
		t.Fatal(h.m.screen, h.writes())
	}
	h.contains("[+] 1Password: using your profile vault (desktop app, team.1password.com)")
}

func TestJoinAnotherAccount(t *testing.T) {
	h := newHarness(t, seedJoin, 80, 24, withJoin)
	revision := h.disk().Selections.Revision
	h.press("e")
	h.typeText("Me@Example.com")
	h.press("enter")
	h.contains("Enter a 1Password sign-in address, such as example.1password.com.")
	h.press("esc")
	if h.m.join.editing || h.disk().Selections.Revision != revision {
		t.Fatal("Esc did not go back, or an invalid account was saved")
	}
	h.press("e")
	h.paste("other.1password.com")
	h.contains("Account: other.1password.com_")
	h.press("enter")
	if got := h.disk().Local.CredentialProfiles["vault"]; got != (config.Profile{Mode: "desktop", Account: "other.1password.com", SessionDuration: "24h"}) {
		t.Fatalf("%+v", got)
	}
}

func TestJoinSkipAndQuitWriteNothing(t *testing.T) {
	h := newHarness(t, seedJoin, 120, 35, withJoin)
	before := h.fileBytes()
	h.press("esc")
	if h.m.screen != screenPresets {
		t.Fatal(h.m.screen)
	}
	h.contains("[ ] 1Password vault: skipped for now")
	h.press("space", "q")
	if !h.quit {
		t.Fatal("q did not quit")
	}
	if !maps.EqualFunc(before, h.fileBytes(), func(a, b []byte) bool { return string(a) == string(b) }) {
		t.Fatal("skip or quit wrote files")
	}
	q := newHarness(t, seedJoin, 120, 35, withJoin)
	q.press("q")
	if !q.quit {
		t.Fatal("q on the intro did not quit")
	}
}

// A profile with the suggestion's ID is never replaced: one that exists is
// not asked about, and one created elsewhere while the question shows stays.
func TestJoinKeepsExistingProfile(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedJoin(t, store)
		mustUpdate(t, store, func(s *config.State) {
			s.Local.CredentialProfiles["vault"] = config.Profile{Mode: "desktop", Account: "mine.example"}
		})
	}
	mine := "[!] 1Password: your profile vault uses mine.example, the catalog suggests team.1password.com"
	h := newHarness(t, seed, 120, 35, withJoin)
	h.containsWrapped(mine)
	h.contains("Enter continue")
	h.lacks("Use this 1Password account?")
	h.press("enter")
	if h.m.screen != screenPresets {
		t.Fatal(h.m.screen)
	}

	h = newHarness(t, seedJoin, 120, 35, withJoin)
	mustUpdate(t, h.store, func(s *config.State) {
		s.Local.CredentialProfiles["vault"] = config.Profile{Mode: "desktop", Account: "mine.example"}
	})
	h.press("enter")
	if got := h.disk().Local.CredentialProfiles["vault"].Account; got != "mine.example" || h.m.screen != screenPresets {
		t.Fatal(got, h.m.screen)
	}
	h.containsWrapped(mine)
}

// Picked presets turn their servers on at once, in one change, as Space
// does; setting them up waits for s.
func TestJoinPresetsApplyAtOnce(t *testing.T) {
	h := newHarness(t, seedJoin, 120, 35, withJoin)
	h.press("enter") // accept the suggestion
	h.contains("What do you work on? Pick any.", "Developer", "Lint, Docs", "Start empty", "0 servers selected", "Code and docs.")
	h.press("space", "down", "space")
	h.contains("3 servers selected")
	saved := h.disk()
	h.press("enter")
	if h.m.screen != screenList || h.m.join != nil {
		t.Fatal(h.m.screen)
	}
	disk := h.disk()
	if !disk.Selections.Connections[idLint].Enabled || !disk.Selections.Connections[idDocs].Enabled || disk.Selections.Connections[idCanvas].Enabled {
		t.Fatalf("%+v", disk.Selections.Connections)
	}
	if disk.Selections.Connections[idLint].CredentialProfile != "vault" {
		t.Fatal("the accepted profile was not linked")
	}
	if disk.Selections.Revision != saved.Selections.Revision+1 {
		t.Fatal("presets took more than one change", disk.Selections.Revision, saved.Selections.Revision)
	}
	h.contains("Turned on 2 servers. 1 needs something first; Space asks for it.")
	h.lacks("Ctrl+S", "unsaved")
	if h.loads.calls != 0 || h.m.screen != screenList {
		t.Fatal("setting up did not wait for the user", h.m.screen, h.loads.calls)
	}
	h.press("u")
	h.contains("2 servers are off again.")
	if disk := h.disk(); disk.Selections.Connections[idLint].Enabled || disk.Selections.Connections[idDocs].Enabled {
		t.Fatal("undo left presets on")
	}
}

func TestJoinStartEmpty(t *testing.T) {
	h := newHarness(t, seedJoin, 120, 35, withJoin)
	h.press("esc", "space", "down", "down", "enter")
	if h.m.screen != screenList || h.writes() != 0 {
		t.Fatal(h.m.screen, h.writes())
	}
}

// A catalog without suggestions or presets shows the intro, then the list.
func TestJoinPlainCatalog(t *testing.T) {
	h := newHarness(t, seedFixture, 80, 24, func(o *Options) {
		o.Join = &Join{SourceID: fixSource, Repository: "acmeco/mcp-catalog", Commit: "aaaaaaa"}
	})
	h.contains("acmeco/mcp-catalog - aaaaaaa", "4 MCP servers picked by the team.", "Enter continue")
	// A profile the catalog needs but does not suggest is named, with how
	// to add it.
	h.containsWrapped("[*] 1Password team: not set up. Add it with mcparcel config profile set team.")
	h.press("enter")
	if h.m.screen != screenList {
		t.Fatal(h.m.screen)
	}
	// A source the state does not hold opens the list.
	h = newHarness(t, seedFixture, 80, 24, func(o *Options) { o.Join = &Join{SourceID: "github-9"} })
	if h.m.screen != screenList {
		t.Fatal(h.m.screen)
	}
}

// joinTour adds the join catalog and walks the join screens: the intro, a
// mistyped account, the presets, Start empty, then the list with the
// presets turned on.
func joinTour(h *harness, visit func(name string)) {
	h.t.Helper()
	seedJoinCatalog(h.t, h.store)
	h.reread()
	h.m.openJoin(joinFixture)
	visit("join intro")
	h.press("e")
	h.typeText("not an account " + strings.Repeat("x", 80))
	h.press("enter")
	visit("join another account")
	h.press("esc", "esc")
	visit("join presets")
	h.press("down", "down")
	visit("join start empty")
	h.press("up", "space", "up", "space", "enter")
	visit("join presets on")
}

// Every join screen fits every size and ends in its key line.
func TestJoinFitsAllSizes(t *testing.T) {
	// The tour covers the join screens with motion; this run is without.
	for _, unicode := range []bool{false, true} {
		h := newHarness(t, seedFixture, 120, 35, fixedNow(), func(o *Options) { o.Color, o.Unicode, o.ReduceMotion = true, unicode, true })
		joinTour(h, func(name string) {
			for _, w := range []int{40, 41, 59, 80, 99, 100, 120, 160} {
				for _, ht := range []int{10, 11, 17, 24, 29, 30, 35, 50} {
					h.send(resize(w, ht))
					checkFits(t, h, w, ht, name)
					lines := strings.Split(h.plain(), "\n")
					if last := lines[len(lines)-1]; name != "join presets on" && len(lines) == ht && !strings.HasPrefix(last, "Enter") && !strings.HasPrefix(last, "Space") {
						t.Fatalf("%dx%d %s: no key line:\n%s", w, ht, name, h.plain())
					}
				}
			}
			h.send(resize(120, 35))
		})
	}
}

// Where the 1Password desktop app cannot read secrets (Linux, headless), a
// desktop suggestion is a note, never a question, and nothing is written.
func TestJoinWithoutDesktopApp(t *testing.T) {
	h := newHarness(t, seedJoin, 120, 35, withJoin, func(o *Options) { o.NoDesktopApp = true })
	before := h.fileBytes()
	h.containsWrapped("[*] 1Password vault: the desktop app works on macOS only. Add a service-account profile with mcparcel config profile set vault.", "MCP Integrations")
	h.contains("Enter continue")
	h.lacks("Use this 1Password account?", "team.1password.com")
	h.press("enter")
	if h.m.screen != screenPresets {
		t.Fatal(h.m.screen)
	}
	if !maps.EqualFunc(before, h.fileBytes(), func(a, b []byte) bool { return string(a) == string(b) }) {
		t.Fatal("a suggestion was saved without the desktop app")
	}
}

// A local profile with the requirement's ID and the suggested account is
// set up, whatever its mode. One with another account, or none, is what
// the catalog's connections link to: the intro names both accounts.
func TestJoinNamesDifferentExistingProfile(t *testing.T) {
	withVault := func(p config.Profile) func(*testing.T, *config.Store) {
		return func(t *testing.T, store *config.Store) {
			seedJoin(t, store)
			mustUpdate(t, store, func(s *config.State) { s.Local.CredentialProfiles["vault"] = p })
		}
	}
	h := newHarness(t, withVault(config.Profile{Mode: config.ProfileModeServiceAccount, TokenEnv: "OP_SERVICE_ACCOUNT_TOKEN"}), 120, 35, withJoin)
	h.containsWrapped("[!] 1Password: your profile vault uses a service-account token, the catalog suggests team.1password.com")
	h.lacks("using your profile", "OP_SERVICE_ACCOUNT_TOKEN")
	h = newHarness(t, withVault(config.Profile{Mode: "desktop", Account: "team.1password.com"}), 120, 35, withJoin)
	h.contains("[+] 1Password: using your profile vault (desktop app, team.1password.com)", "  MCP Integrations")
	h = newHarness(t, withVault(config.Profile{Mode: config.ProfileModeDesktopServiceAccount, Account: "team.1password.com", BootstrapRef: "op://Vault/item/field"}), 120, 35, withJoin)
	h.contains("[+] 1Password: using your profile vault (service account, team.1password.com)")
	h.lacks("[!]", "op://")
}

// seedTwoSuggestions gives the join catalog a second suggestion that more
// servers use, so it is asked about first.
func seedTwoSuggestions(t *testing.T, store *config.Store) {
	t.Helper()
	seedFixture(t, store)
	seedJoinCatalogWith(t, store, func(c *config.Catalog) {
		c.CredentialProfiles["seo"] = config.ProfileRequirement{Description: "SEO tools", Suggest: &config.ProfileSuggestion{Mode: config.ProfileModeDesktop, Account: "seo.1password.com"}}
		c.CredentialProfiles["zz"] = config.ProfileRequirement{Suggest: &config.ProfileSuggestion{Mode: config.ProfileModeDesktop, Account: "zz.1password.com"}}
		for _, id := range []string{"docs", "canvas"} {
			conn := c.Connections[id]
			conn.CredentialProfile = "seo"
			c.Connections[id] = conn
		}
	})
}

// Suggestions are asked about most used first, with how far along the
// intro is; one no server uses is not asked about; and confirming one
// leaves no status behind on the next question.
func TestJoinAsksInOrder(t *testing.T) {
	h := newHarness(t, seedTwoSuggestions, 80, 24, withJoin)
	h.contains("This catalog asks for a 1Password profile: seo (1 of 2).", "  SEO tools", "2 servers use it.")
	h.lacks("zz", "1Password work")
	h.press("enter")
	h.contains("[+] 1Password: using your profile seo (desktop app, seo.1password.com)", "a 1Password profile: vault (2 of 2).")
	if h.m.status != "" {
		t.Fatalf("status %q", h.m.status)
	}
}

// A typed account is stored as the sign-in address: trimmed, lowercase,
// without a scheme or a trailing slash.
func TestJoinNormalizesTypedAccount(t *testing.T) {
	for _, typed := range []string{" My.1Password.com ", "https://my.1password.com/"} {
		h := newHarness(t, seedJoin, 80, 24, withJoin)
		h.press("e")
		h.paste(typed)
		h.press("enter")
		if got := h.disk().Local.CredentialProfiles["vault"].Account; got != "my.1password.com" {
			t.Fatalf("%q saved %q", typed, got)
		}
	}
}

// The suggested account is never cut, however narrow the terminal.
func TestJoinNarrowShowsWholeAccount(t *testing.T) {
	long := "prompt-engineering-club-with-a-longer-name.1password.eu"
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		seedJoinCatalogWith(t, store, func(c *config.Catalog) {
			c.CredentialProfiles["vault"] = config.ProfileRequirement{Suggest: &config.ProfileSuggestion{Mode: config.ProfileModeDesktop, Account: long}}
		})
	}
	for _, w := range []int{40, 59, 80} {
		h := newHarness(t, seed, w, 24, withJoin)
		if got := strings.Join(strings.Fields(strings.ReplaceAll(h.plain(), "\n", "")), ""); !strings.Contains(got, long) {
			t.Fatalf("%d columns cut the account:\n%s", w, h.plain())
		}
		h.lacks("...")
	}
}

// Enter on Start empty says it drops the picks before it does.
func TestJoinStartEmptySaysItDropsPicks(t *testing.T) {
	h := newHarness(t, seedJoin, 120, 35, withJoin)
	h.press("esc", "down", "down")
	h.contains("Space pick   Enter continue")
	h.press("up", "up", "space", "down", "down")
	h.contains("Enter start empty, without the picks")
	h.lacks("Space pick")
}

// After the presets the list opens on the first domain, in tab order, with
// a picked server, on the first of them there that needs a step, else on
// the first of them. Once the stored sign-ins are read it lands again.
func TestJoinLandsOnTheFirstStep(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		seedFixture(t, store)
		seedJoinCatalogWith(t, store, func(c *config.Catalog) {
			c.Domains = map[string]config.Domain{"design": {Label: "Design"}}
			lint, docs, canvas := c.Connections["lint"], c.Connections["docs"], c.Connections["canvas"]
			lint.Domains, docs.Domains, canvas.Domains = []string{"design"}, []string{"design"}, []string{"design"}
			docs.Label, canvas.Label = "Wiki", "Whiteboard"
			docs.Auth = &config.OAuth{Type: "oauth"}
			docs.Transport = config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://wiki.example/mcp")}}
			c.Connections["lint"], c.Connections["docs"], c.Connections["canvas"] = lint, docs, canvas
		})
	}
	designer := newHarness(t, seed, 120, 35, withJoin)
	designer.press("enter", "down", "space", "enter")
	if designer.m.currentTab().label != "Design" {
		t.Fatal(designer.m.currentTab())
	}
	designer.focused("Whiteboard", "needs home") // a step, after Wiki (ready)

	developer := newHarness(t, seed, 120, 35, withJoin)
	developer.fin.needs = map[string]SignInState{idDocs: {Possible: true, Needed: true}}
	developer.press("enter", "space", "enter")
	developer.focused("Wiki", "sign in") // after Lint (ready), once read
	developer.press("up")                // a key keeps the cursor where it is from now on
	developer.send(signInsMsg{})
	developer.focused("Whiteboard")
}
