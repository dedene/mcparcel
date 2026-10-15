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

func TestSetupSummary(t *testing.T) {
	for _, tc := range []struct {
		result  ui.Result
		err     error
		out     string
		wantErr error
	}{
		{ui.Result{}, nil, "No changes saved.\n", nil},
		{ui.Result{Revision: 7, Saves: 2}, nil, "Configuration saved at revision 7.\n", nil},
		{ui.Result{Revision: 7, Interrupted: true}, nil, "", context.Canceled},
		{ui.Result{Revision: 8, Saves: 1, Interrupted: true}, nil, "Configuration saved at revision 8.\n", context.Canceled},
		{ui.Result{Revision: 8, Saves: 1, Interrupted: true}, context.Canceled, "Configuration saved at revision 8.\n", context.Canceled},
		{ui.Result{}, config.ErrConfigWrite, "", config.ErrConfigWrite},
		{ui.Result{Revision: 7, Unconfirmed: true}, nil, unconfirmedSummary, nil},
		{ui.Result{Revision: 7, Unconfirmed: true, Interrupted: true}, nil, unconfirmedSummary, context.Canceled},
		{ui.Result{Revision: 8, Saves: 1, Unconfirmed: true}, nil, "Configuration saved at revision 8.\n" + unconfirmedSummary, nil},
	} {
		var out bytes.Buffer
		err := finishSetup(&Streams{Out: &out}, tc.result, tc.err, func(ui.Result) ([]string, []string) { return nil, nil })
		if out.String() != tc.out || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
			t.Fatal(tc.result, out.String(), err)
		}
	}
	// A finished setup lists the sign-ins still needed after the summary,
	// then what the finish step could not start; an interrupted or failed one
	// does not look them up.
	actions := func() ([]string, []string) {
		return []string{"mcparcel auth code", "mcparcel auth local:status"}, []string{"maps", "local:wiki"}
	}
	hints := "Sign in before first use:\n  mcparcel auth code\n  mcparcel auth local:status\nCould not start maps, local:wiki. Run mcparcel doctor to see why.\n"
	for _, tc := range []struct {
		result  ui.Result
		err     error
		out     string
		wantErr error
	}{
		{ui.Result{}, nil, "No changes saved.\n" + hints, nil},
		{ui.Result{Revision: 7, Saves: 2}, nil, "Configuration saved at revision 7.\n" + hints, nil},
		{ui.Result{Revision: 8, Saves: 1, Unconfirmed: true}, nil, "Configuration saved at revision 8.\n" + unconfirmedSummary + hints, nil},
		{ui.Result{Revision: 8, Saves: 1, Interrupted: true}, nil, "Configuration saved at revision 8.\n", context.Canceled},
		{ui.Result{}, config.ErrConfigWrite, "", config.ErrConfigWrite},
	} {
		var out bytes.Buffer
		called := false
		err := finishSetup(&Streams{Out: &out}, tc.result, tc.err, func(ui.Result) ([]string, []string) { called = true; return actions() })
		if out.String() != tc.out || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) || called != (tc.wantErr == nil) {
			t.Fatal(tc.result, out.String(), err, called)
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

// TestSetupHints lists enabled OAuth connections whose stored
// session needs sign-in, by the name auth <mcp> accepts. It skips signed-in,
// disabled, unmarked and client_credentials connections, connections auth
// <mcp> refuses (under review, SSE) and any connection whose Keychain item
// cannot be read; headless mode lists nothing.
func TestSetupHints(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	oauth := func(u string) config.Connection {
		return config.Connection{Auth: &config.OAuth{Type: "oauth"}, Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(u)}}}
	}
	_, err := store.Update(context.Background(), 0, func(s *config.State) error {
		for _, id := range []string{"code", "status", "signed", "broken", "off"} {
			s.Personal.Connections[id] = oauth("https://" + id + ".example/mcp")
			s.Selections.Connections["local:"+id] = config.Selection{Enabled: id != "off"}
		}
		// auth <mcp> refuses a connection under review or on SSE.
		s.Personal.Connections["rev"] = oauth("https://rev.example/mcp")
		s.Selections.Connections["local:rev"] = config.Selection{Enabled: true, ReviewRequired: true}
		sse := oauth("https://sse.example/mcp")
		sse.Transport.HTTP.Mode = "sse"
		s.Personal.Connections["sse"] = sse
		s.Selections.Connections["local:sse"] = config.Selection{Enabled: true}
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
	got, failed := setupHints(context.Background(), p, store, ui.Result{})
	if want := []string{"mcparcel auth code", "mcparcel auth local:status"}; !reflect.DeepEqual(got, want) || failed != nil {
		t.Fatal(got, failed)
	}
	// The finish step's outcomes: one it started is not listed again, one
	// it left needing a sign-in is (an unmarked server too), and one it could
	// not start is named for doctor.
	got, failed = setupHints(context.Background(), p, store, ui.Result{Started: []string{"local:code"}, SignIn: []string{"local:plain"}, Failed: []string{"local:off", "local:front"}})
	if want := []string{"mcparcel auth plain", "mcparcel auth local:status"}; !reflect.DeepEqual(got, want) || !reflect.DeepEqual(failed, []string{"front", "off"}) {
		t.Fatal(got, failed)
	}
	// A failed browser sign-in is listed once, as the sign-in it still needs.
	got, failed = setupHints(context.Background(), p, store, ui.Result{Failed: []string{"local:code"}})
	if want := []string{"mcparcel auth code", "mcparcel auth local:status"}; !reflect.DeepEqual(got, want) || failed != nil {
		t.Fatal(got, failed)
	}
	headless := p
	headless.StateRoot = filepath.Join(filepath.Dir(p.Home), "state-root")
	if got, failed := setupHints(context.Background(), headless, store, ui.Result{SignIn: []string{"local:plain"}}); got != nil || !reflect.DeepEqual(failed, []string{"plain"}) {
		t.Fatal("headless mode listed sign-ins", got, failed)
	}
	// What NeedsSignIn reports: a stored sign-in that is missing, one that
	// is there, an unmarked server (its server decides), a local command or
	// client credentials (no sign-in), and nothing in headless mode.
	for id, want := range map[string]ui.SignInState{
		"local:code":   {Possible: true, Needed: true},
		"local:signed": {Possible: true},
		"local:plain":  {Possible: true},
		"local:front":  {},
		"local:rev":    {},
	} {
		if got := setupSignInState(context.Background(), p, store, id); got != want {
			t.Error(id, got, want)
		}
	}
	if got := setupSignInState(context.Background(), headless, store, "local:code"); got != (ui.SignInState{}) {
		t.Fatal("headless mode can sign in", got)
	}
	if setupSignIn(headless, &CommandOptions{}) != nil {
		t.Fatal("headless mode has a sign-in")
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
