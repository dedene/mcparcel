package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
	"github.com/dedene/mcparcel/internal/testutil"
	"github.com/dedene/mcparcel/internal/ui"
)

// configFiles reads every file under the config and data directories, so a
// test can prove setup wrote nothing (not even the lock file).
func configFiles(t *testing.T, p config.Paths) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range []string{p.ConfigDir, p.DataDir} {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				b, _ := os.ReadFile(path)
				out[path] = string(b)
			}
			return nil
		})
	}
	return out
}

func TestSetupRejectsJSON(t *testing.T) {
	p, _, _ := metadataSeed(t)
	before := configFiles(t, p)
	code, out, errOut := run(t, "setup", "--json")
	if code != 2 || errOut != "" || !strings.Contains(out, `"code":"terminal_required"`) || strings.Count(out, "\n") != 1 {
		t.Fatal(code, out, errOut)
	}
	if !reflect.DeepEqual(before, configFiles(t, p)) {
		t.Fatal("setup --json wrote files")
	}
}

func TestSetupRejectsNoInput(t *testing.T) {
	p, _, _ := metadataSeed(t)
	before := configFiles(t, p)
	code, out, errOut := run(t, "setup", "--no-input")
	if code != 2 || out != "" || !strings.Contains(errOut, "Setup needs an interactive terminal") || !strings.Contains(errOut, "config input set") {
		t.Fatal(code, out, errOut)
	}
	if !reflect.DeepEqual(before, configFiles(t, p)) {
		t.Fatal("setup --no-input wrote files")
	}
}

func TestSetupRejectsWithoutTerminal(t *testing.T) {
	p, _, _ := metadataSeed(t)
	before := configFiles(t, p)
	code, out, errOut := run(t, "setup")
	if code != 2 || out != "" || !strings.Contains(errOut, "Setup needs an interactive terminal") {
		t.Fatal(code, out, errOut)
	}
	if !reflect.DeepEqual(before, configFiles(t, p)) {
		t.Fatal("setup without a terminal wrote files")
	}
	if _, err := os.Stat(p.LockFile); !os.IsNotExist(err) {
		t.Fatal("lock file created", err)
	}
}

func TestSetupRejectsBeforeReadingConfig(t *testing.T) {
	p := metadataEnv(t)
	if err := os.MkdirAll(filepath.Dir(p.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{"setup"}, {"setup", "--json"}, {"setup", "--no-input"}} {
		code, out, errOut := run(t, argv...)
		if code != 2 || !strings.Contains(out+errOut, "terminal_required") && !strings.Contains(errOut, "Setup needs an interactive terminal") {
			t.Fatal(argv, code, out, errOut)
		}
	}
}

func TestSetupHelp(t *testing.T) {
	code, out, errOut := run(t, "setup", "--help")
	if code != 0 || errOut != "" || !strings.Contains(out, "Choose connections and tools in an interactive terminal.") {
		t.Fatal(code, out, errOut)
	}
	code, out, _ = run(t, "--help")
	if code != 0 || !strings.Contains(out, "setup") {
		t.Fatal(code, out)
	}
	code, out, _ = run(t, "setup", "extra", "--json")
	if code != 2 || !strings.Contains(out, `"code":"invalid_arguments"`) {
		t.Fatal(code, out)
	}
}

// Every change is written as it is made, so setup has no save to report:
// it says in one line what is on and what is left, then what to run for
// what it could not do: setup once more for the sign-ins s does, auth for
// one it cannot do here, doctor for a start that failed. Then a change it
// could not confirm. An interrupted or failed setup prints no summary.
func TestSetupSummary(t *testing.T) {
	steps := ui.Result{On: 10, Ready: 4, Steps: []ui.Step{
		{ID: "local:front", Label: "Front", Need: ui.NeedSignIn},
		{ID: "local:glitchtip", Label: "GlitchTip", Need: ui.NeedSignIn, Command: "mcparcel auth glitchtip"},
		{ID: "local:maps", Label: "Maps", Need: ui.NeedFailed},
		{ID: "github:zenjoy/club#notes", Label: "Notes", Need: ui.NeedInput, Detail: "workspace"},
		{ID: "local:rails", Label: "Rails Blocks", Need: ui.NeedCommand, Detail: "rails-blocks-mcp"},
		{ID: "local:wiki", Label: "Wiki", Need: ui.NeedFailed},
	}}
	stepsOut := "10 on: 4 ready, Front needs a sign-in, GlitchTip needs a sign-in, Maps could not start, Notes needs input workspace, " +
		"Rails Blocks needs rails-blocks-mcp installed, Wiki could not start.\n" +
		"  mcparcel setup, then s signs you in\n  mcparcel auth glitchtip\n  mcparcel doctor\n"
	for _, tc := range []struct {
		result  ui.Result
		err     error
		out     string
		wantErr error
	}{
		{ui.Result{}, nil, "Nothing is on.\n", nil},
		{ui.Result{On: 9, Ready: 9}, nil, "9 on, all ready.\n", nil},
		{steps, nil, stepsOut, nil},
		{
			ui.Result{On: 2, Steps: []ui.Step{{ID: "x", Label: "X", Need: ui.NeedReview}, {ID: "y", Label: "Y", Need: ui.NeedUnavailable}, {ID: "z", Label: "Z", Need: ui.NeedProfile}}},
			nil,
			"2 on: 0 ready, X needs a review, Y is unavailable, Z needs a 1Password profile.\n", nil,
		},
		{ui.Result{Interrupted: true, On: 1, Ready: 1}, nil, "", context.Canceled},
		{ui.Result{}, context.Canceled, "", context.Canceled},
		{ui.Result{}, config.ErrConfigWrite, "", config.ErrConfigWrite},
		{ui.Result{Unconfirmed: true, On: 1, Ready: 1}, nil, unconfirmedSummary + "1 on, all ready.\n", nil},
		{ui.Result{Unconfirmed: true, Interrupted: true}, nil, unconfirmedSummary, context.Canceled},
	} {
		var out bytes.Buffer
		err := finishSetup(&Streams{Out: &out}, tc.result, tc.err)
		if out.String() != tc.out || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
			t.Fatalf("%+v: %q %v", tc.result, out.String(), err)
		}
	}
}

// failingKeyring fails reads of one account and serves the rest from mem.
type failingKeyring struct {
	*testutil.MemKeyring
	account string
}

func (k failingKeyring) Get(service, account string) (string, error) {
	if account == k.account {
		return "", errors.New("fixture keyring failure")
	}
	return k.MemKeyring.Get(service, account)
}

// What setup's Status reads for each stored sign-in, as auth status
// explains it: one that is missing, one that is there, an unmarked server
// (its server decides), client credentials and a connection auth <mcp>
// refuses (no sign-in), one the keyring cannot read without a prompt
// (unknown), and nothing in headless mode.
func TestSetupSignInState(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	oauth := func(u string) config.Connection {
		return config.Connection{Auth: &config.OAuth{Type: "oauth"}, Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(u)}}}
	}
	_, err := store.Update(context.Background(), 0, func(s *config.State) error {
		for _, id := range []string{"code", "signed", "broken"} {
			s.Personal.Connections[id] = oauth("https://" + id + ".example/mcp")
			s.Selections.Connections["local:"+id] = config.Selection{Enabled: true}
		}
		s.Personal.Connections["rev"] = oauth("https://rev.example/mcp")
		s.Selections.Connections["local:rev"] = config.Selection{Enabled: true, ReviewRequired: true}
		s.Personal.Connections["plain"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://plain.example/mcp")}}}
		s.Selections.Connections["local:plain"] = config.Selection{Enabled: true}
		cc := oauth("https://front.example/mcp")
		cc.Auth.Grant = config.GrantClientCredentials
		cc.Auth.TokenURL = "https://front.example/oauth/token"
		cc.Auth.ClientID = &config.Value{Secret: &config.SecretRef{Secret: "env:FRONT_CLIENT_ID"}}
		cc.Auth.ClientSecret = &config.Value{Secret: &config.SecretRef{Secret: "env:FRONT_CLIENT_SECRET"}}
		s.Personal.Connections["front"] = cc
		s.Selections.Connections["local:front"] = config.Selection{Enabled: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mem := &testutil.MemKeyring{}
	if err := auth.SaveOAuth(context.Background(), mem, "local:signed", auth.OAuthState{Version: 1, URL: "https://signed.example/mcp", RefreshToken: "fixture-refresh"}); err != nil {
		t.Fatal(err)
	}
	saved := keyringFactory
	t.Cleanup(func() { keyringFactory = saved })
	keyringFactory = func(config.Paths) auth.Keyring { return failingKeyring{mem, "local:broken"} }
	for id, want := range map[string]ui.SignInState{
		"local:code":   {Possible: true, Needed: true},
		"local:signed": {Possible: true},
		"local:broken": {Possible: true, Unknown: true},
		"local:plain":  {Possible: true},
		"local:front":  {},
		"local:rev":    {},
	} {
		if got := setupSignInState(context.Background(), p, store, id); got != want {
			t.Error(id, got, want)
		}
	}
	headless := p
	headless.StateRoot = filepath.Join(filepath.Dir(p.Home), "state-root")
	if got := setupSignInState(context.Background(), headless, store, "local:code"); got != (ui.SignInState{}) {
		t.Fatal("headless mode can sign in", got)
	}
	if setupSignIn(headless, &CommandOptions{}) != nil {
		t.Fatal("headless mode has a sign-in")
	}
}

// Setup looks for a command as the runtime does: on the PATH the
// connection sets, else on PATH, or at its absolute path.
func TestSetupCommandAvailable(t *testing.T) {
	dir, own := t.TempDir(), t.TempDir()
	tool := filepath.Join(dir, "fixture-tool")
	for _, path := range []string{tool, filepath.Join(own, "own-tool")} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	for command, want := range map[string]bool{"fixture-tool": true, tool: true, "missing-tool": false, filepath.Join(dir, "missing"): false, "./fixture-tool": false, "own-tool": false} {
		if got := setupCommandAvailable(command, ""); got != want {
			t.Error(command, got)
		}
	}
	if !setupCommandAvailable("own-tool", own) || setupCommandAvailable("fixture-tool", own) {
		t.Fatal("the connection's PATH is not the one looked on")
	}
}

func TestUnicodeTerminal(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{"LANG": "en_US.UTF-8", "TERM": "xterm-256color"}, true},
		{map[string]string{"LANG": "nl_BE.utf8"}, true},
		{map[string]string{"LC_CTYPE": "UTF-8", "LANG": "C"}, true},
		{map[string]string{"LC_ALL": "C", "LC_CTYPE": "en_US.UTF-8"}, false}, // LC_ALL wins
		{map[string]string{"LC_ALL": "", "LANG": "en_US.utf-8"}, true},       // empty means unset
		{map[string]string{"LANG": "en_US.UTF-8", "TERM": "dumb"}, false},
		{map[string]string{"LANG": "en_US.ISO8859-1"}, false},
		{map[string]string{}, false},
	} {
		if got := unicodeTerminal(func(name string) string { return tc.env[name] }); got != tc.want {
			t.Errorf("%v: got %v", tc.env, got)
		}
	}
}

// Setup's sign-in refuses offline exactly as auth <mcp> does: without a
// reachable keyring and no runtime, no runtime starts and no URL shows.
func TestSetupSignInPreflight(t *testing.T) {
	paths := keyringEnv(t, openPersonal, nil)
	saved, savedClient := keyringReachableCheck, authRuntimeClient
	t.Cleanup(func() { keyringReachableCheck, authRuntimeClient = saved, savedClient })
	keyringReachableCheck = func(config.Paths) bool { return false }
	authRuntimeClient = func(*CommandOptions) (*runtimeclient.Client, error) {
		t.Error("the runtime client was built")
		return nil, output.NewError("runtime_start_failed", nil)
	}
	sign := setupSignIn(paths, &CommandOptions{})
	for _, name := range []string{"code", "open"} {
		err := sign(context.Background(), name, func(string, bool) { t.Error("a URL was shown") })
		var e *output.Error
		if !errors.As(err, &e) || e.Code != "keychain_unavailable" {
			t.Fatal(name, err)
		}
	}
	noRuntime(t, paths)
}

// Join asks about a suggested desktop profile only where the 1Password
// desktop app can read secrets: not on Linux, never in headless mode.
func TestSetupOptionsDesktopApp(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	if got := setupOptions(p, store, &CommandOptions{}, nil, nil).NoDesktopApp; got != !desktopOnePassword(p) {
		t.Fatal(got)
	}
	headless := p
	headless.StateRoot = filepath.Join(filepath.Dir(p.Home), "state-root")
	if !setupOptions(headless, store, &CommandOptions{}, nil, nil).NoDesktopApp {
		t.Fatal("headless mode asks about the desktop app")
	}
}
