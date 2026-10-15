package cmd

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
	"github.com/dedene/mcparcel/internal/ui"
)

var joinSource = config.Source{ID: "github-7", RepositoryID: 7, Owner: "fixture", Repo: "team", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("c", 40)}

// joinFetcher serves a team catalog that suggests a 1Password account for
// vault and has one preset, through the real GitHub fetcher and the
// minVersion check add uses. It counts fetches.
type joinFetcher struct {
	catalog.Fetcher
	fetches int
}

func (f *joinFetcher) Fetch(ctx context.Context, source config.Source) (catalog.Snapshot, error) {
	f.fetches++
	return f.Fetcher.Fetch(ctx, source)
}

func newJoinFetcher(t *testing.T) *joinFetcher {
	t.Helper()
	stdio := func(command string) config.Transport {
		return config.Transport{Stdio: &config.Stdio{Command: config.Literal(command)}}
	}
	api := testutil.NewCatalogAPI()
	err := api.SetSnapshot(catalog.Snapshot{Source: joinSource, Catalog: config.Catalog{
		SchemaVersion: 1,
		MinVersion:    config.JoinMinVersion,
		Name:          "Team",
		CredentialProfiles: map[string]config.ProfileRequirement{
			"vault": {Description: "MCP Integrations", Suggest: &config.ProfileSuggestion{Mode: config.ProfileModeDesktop, Account: "team.1password.com"}},
		},
		Presets: []config.Preset{{ID: "developer", Label: "Developer", Connections: []string{"lint", "docs"}}},
		Connections: map[string]config.Connection{
			"lint": {Label: "Lint", CredentialProfile: "vault", Transport: stdio("lint-fixture")},
			"docs": {Label: "Docs", Transport: stdio("docs-fixture")},
			"misc": {Label: "Misc", Transport: stdio("misc-fixture")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &joinFetcher{Fetcher: minVersionFetcher{&catalog.GitHub{Public: api, Private: api}}}
}

// keys turns names into key presses: enter, esc, space, ctrl+s, or text
// typed one character at a time.
func keys(names ...string) []tea.KeyPressMsg {
	var out []tea.KeyPressMsg
	for _, name := range names {
		switch name {
		case "enter":
			out = append(out, tea.KeyPressMsg{Code: tea.KeyEnter})
		case "esc":
			out = append(out, tea.KeyPressMsg{Code: tea.KeyEscape})
		case "space":
			out = append(out, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
		case "ctrl+s":
			out = append(out, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		default:
			for _, r := range name {
				out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
			}
		}
	}
	return out
}

// scriptJoin runs join in a pretend terminal: the setup program is the real
// model, driven by presses with each command run synchronously. It returns
// the options join passed. The runtime must never be contacted.
func scriptJoin(t *testing.T, presses ...string) *ui.Options {
	t.Helper()
	got := &ui.Options{}
	oldUI, oldTerminal := runSetupUI, joinTerminal
	t.Cleanup(func() { runSetupUI, joinTerminal = oldUI, oldTerminal })
	joinTerminal = func(_, _ *os.File) bool { return true }
	runSetupUI = func(ctx context.Context, store *config.Store, state config.State, opts ui.Options) (ui.Result, error) {
		*got = opts
		opts.LoadTools = func(context.Context, string) (output.ToolList, error) {
			t.Error("join contacted the runtime")
			return output.ToolList{}, nil
		}
		opts.NeedsSignIn, opts.SignIn, opts.SourceAges = nil, nil, nil
		// The suggestion flow as on macOS, whatever runs the test;
		// TestSetupOptionsDesktopApp covers where join does not ask.
		opts.NoDesktopApp = false
		opts.Tick = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
		m, err := ui.NewModel(ctx, store, state, opts)
		if err != nil {
			return ui.Result{}, err
		}
		quit := false
		var run func(tea.Cmd)
		send := func(msg tea.Msg) {
			_, cmd := m.Update(msg)
			run(cmd)
		}
		run = func(cmd tea.Cmd) {
			if cmd == nil {
				return
			}
			switch msg := cmd().(type) {
			case nil:
			case tea.QuitMsg:
				quit = true
			case tea.BatchMsg:
				for _, c := range msg {
					run(c)
				}
			default:
				send(msg)
			}
		}
		send(tea.WindowSizeMsg{Width: 120, Height: 35})
		for _, k := range keys(presses...) {
			if !quit {
				send(k)
			}
		}
		if !quit {
			t.Error("the script did not end setup")
		}
		return m.Result(), nil
	}
	return got
}

func runJoin(t *testing.T, f *joinFetcher, argv ...string) (string, error) {
	t.Helper()
	cmd := &JoinCmd{Repository: "fixture/team"}
	for i := 0; i+1 < len(argv); i += 2 {
		switch argv[i] {
		case "--path":
			cmd.Path = argv[i+1]
		case "--ref":
			cmd.Ref = argv[i+1]
		}
	}
	var out, errOut bytes.Buffer
	err := cmd.Run(context.Background(), &Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, &CommandOptions{}, &CatalogDependencies{Fetcher: f})
	if errOut.Len() != 0 {
		t.Fatal(errOut.String())
	}
	return out.String(), err
}

func TestJoinNeedsTerminalBeforeFetch(t *testing.T) {
	p := metadataEnv(t)
	before := configFiles(t, p)
	for _, argv := range [][]string{{"join", "fixture/team"}, {"join", "fixture/team", "--no-input"}, {"join", "not a repo"}} {
		code, out, errOut := run(t, argv...)
		if code != 2 || out != "" || !strings.Contains(errOut, "Join needs an interactive terminal") {
			t.Fatal(argv, code, out, errOut)
		}
		want := "mcparcel add fixture/team, then mcparcel setup"
		if argv[1] == "not a repo" {
			want = "mcparcel add <owner/repo>, then mcparcel setup"
		}
		if !strings.Contains(errOut, want) {
			t.Fatal(errOut)
		}
	}
	code, out, _ := run(t, "join", "fixture/team", "--json")
	if code != 2 || !strings.Contains(out, `"code":"terminal_required"`) || !strings.Contains(out, "Join needs an interactive terminal") {
		t.Fatal(code, out)
	}
	f := newJoinFetcher(t)
	if _, err := runJoin(t, f); safeFailure(err).Code != "terminal_required" || f.fetches != 0 {
		t.Fatal(err, f.fetches)
	}
	if !reflect.DeepEqual(before, configFiles(t, p)) {
		t.Fatal("join without a terminal wrote files")
	}
	code, out, _ = run(t, "join", "fixture/team", "--path", "a.json", "--path", "b.json", "--json")
	if code != 2 || !strings.Contains(out, `"code":"invalid_arguments"`) {
		t.Fatal(code, out)
	}
}

// Join registers exactly as add does; quitting at once leaves only that.
func TestJoinRegistersLikeAdd(t *testing.T) {
	read := func(p config.Paths) config.State {
		state, err := config.NewStore(p).Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	added := metadataEnv(t)
	var out bytes.Buffer
	if err := (&AddCmd{Repository: "fixture/team"}).Run(context.Background(), &Streams{Out: &out}, &CommandOptions{}, &CatalogDependencies{Fetcher: newJoinFetcher(t)}); err != nil {
		t.Fatal(err)
	}
	joined := metadataEnv(t)
	scriptJoin(t, "q")
	stdout, err := runJoin(t, newJoinFetcher(t))
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "Added fixture/team (ccccccc).\nNo changes saved.\n" {
		t.Fatalf("%q", stdout)
	}
	a, j := read(added), read(joined)
	if !reflect.DeepEqual(a.Local, j.Local) || !reflect.DeepEqual(a.Catalogs, j.Catalogs) || !reflect.DeepEqual(a.Selections, j.Selections) {
		t.Fatalf("add and join differ:\n%+v\n%+v", a, j)
	}
}

func TestJoinReusesRegisteredSource(t *testing.T) {
	metadataEnv(t)
	f := newJoinFetcher(t)
	scriptJoin(t, "q")
	if _, err := runJoin(t, f); err != nil {
		t.Fatal(err)
	}
	got := scriptJoin(t, "q")
	stdout, err := runJoin(t, f)
	if err != nil || f.fetches != 1 {
		t.Fatal(err, f.fetches)
	}
	if stdout != "fixture/team is already added (ccccccc). Run mcparcel sync to update it.\nNo changes saved.\n" {
		t.Fatalf("%q", stdout)
	}
	if got.Join == nil || *got.Join != (ui.Join{SourceID: "github-7", Repository: "fixture/team", Commit: "ccccccc"}) {
		t.Fatalf("%+v", got.Join)
	}
	if _, err := runJoin(t, f, "--path", "other.json"); safeFailure(err).Code != "catalog_source_conflict" || f.fetches != 1 {
		t.Fatal(err, f.fetches)
	}
}

// The suggested account becomes a local profile only on Enter: mode and
// account, nothing else. Another account follows the catalog's rule; Esc
// creates nothing.
func TestJoinSuggestion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		presses []string
		account string
	}{
		{"accept", []string{"enter", "q"}, "team.1password.com"},
		{"other account", []string{"e", "Me@Example.com", "enter", "esc", "e", "other.1password.com", "enter", "q"}, "other.1password.com"},
		{"skip", []string{"esc", "q"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := metadataEnv(t)
			scriptJoin(t, tc.presses...)
			stdout, err := runJoin(t, newJoinFetcher(t))
			if err != nil {
				t.Fatal(err)
			}
			state, err := config.NewStore(p).Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			profile, ok := state.Local.CredentialProfiles["vault"]
			if tc.account == "" {
				if ok || !strings.HasSuffix(stdout, "No changes saved.\n") {
					t.Fatal(profile, stdout)
				}
				return
			}
			if profile != (config.Profile{Mode: config.ProfileModeDesktop, Account: tc.account, SessionDuration: profile.SessionDuration}) {
				t.Fatalf("%+v", profile)
			}
			if !strings.HasSuffix(stdout, "Configuration saved at revision 2.\n") {
				t.Fatalf("%q", stdout)
			}
		})
	}
}

// Presets turn connections on in the draft; only Ctrl+S saves them, and the
// finish offer that follows is declined.
func TestJoinPresetsStage(t *testing.T) {
	enabled := func(p config.Paths) []string {
		state, err := config.NewStore(p).Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for id, sel := range state.Selections.Connections {
			if sel.Enabled {
				ids = append(ids, id+"="+sel.CredentialProfile)
			}
		}
		return ids
	}
	p := metadataEnv(t)
	scriptJoin(t, "enter", "space", "enter", "q", "y")
	if _, err := runJoin(t, newJoinFetcher(t)); err != nil {
		t.Fatal(err)
	}
	if got := enabled(p); len(got) != 0 {
		t.Fatal("presets were saved without Ctrl+S", got)
	}

	p = metadataEnv(t)
	scriptJoin(t, "enter", "space", "enter", "ctrl+s", "esc", "q")
	stdout, err := runJoin(t, newJoinFetcher(t))
	if err != nil {
		t.Fatal(err)
	}
	got := enabled(p)
	slices.Sort(got)
	if !reflect.DeepEqual(got, []string{"github:fixture/team#docs=", "github:fixture/team#lint=vault"}) {
		t.Fatal(got)
	}
	if !strings.Contains(stdout, "Configuration saved at revision 3.\n") {
		t.Fatalf("%q", stdout)
	}
}
