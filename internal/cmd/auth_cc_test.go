package cmd

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
	"github.com/dedene/mcparcel/internal/testutil"
)

// ccPersonal has client_credentials connection front and authorization-code
// connection code.
const ccPersonal = `{"schemaVersion":1,"connections":{
"front":{"transport":{"type":"http","url":"https://mcp.example/mcp"},"auth":{"type":"oauth","grant":"client_credentials","tokenUrl":"https://ws.example/oauth/token","clientId":{"secret":"env:FRONT_CLIENT_ID"},"clientSecret":{"secret":"env:FRONT_CLIENT_SECRET"}}},
"code":{"transport":{"type":"http","url":"https://code.example/mcp"},"auth":{"type":"oauth"}}}}`

// ccEnv writes ccPersonal in desktop or headless mode and returns the paths.
func ccEnv(t *testing.T, headless bool) config.Paths {
	t.Helper()
	if headless {
		headlessEnv(t)
	} else {
		// A workstation's config.json: Linux desktop mode then runs without
		// a desktop session too.
		p := metadataEnv(t)
		if err := os.WriteFile(p.ConfigFile, []byte(`{"schemaVersion":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := runtimePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(paths.PersonalFile, []byte(ccPersonal), 0o600); err != nil {
		t.Fatal(err)
	}
	return paths
}

// noRuntime fails when anything was created in the runtime directory.
func noRuntime(t *testing.T, paths config.Paths) {
	t.Helper()
	if entries, err := os.ReadDir(paths.RuntimeDir); err == nil && len(entries) > 0 || err != nil && !os.IsNotExist(err) {
		t.Fatal("runtime contacted", entries, err)
	}
}

func TestAuthClientCredentialsNothingToSignIn(t *testing.T) {
	for _, headless := range []bool{false, true} {
		name := "desktop"
		if headless {
			name = "headless"
		}
		t.Run(name, func(t *testing.T) {
			paths := ccEnv(t, headless)
			for _, argv := range [][]string{{"auth", "logout", "front"}, {"auth", "logout", "local:front"}, {"auth", "status", "front"}} {
				code, stdout, stderr := run(t, append(argv, "--json")...)
				e := envelopeError(t, stdout)
				if code != 2 || e.Code != "invalid_arguments" || e.Message != argv[2]+" uses client credentials; there is nothing to sign in to." || stderr != "" {
					t.Fatal(argv, code, stdout, stderr)
				}
			}
			noRuntime(t, paths)
		})
	}
}

// auth status omits client_credentials connections and never reads their
// Keychain item.
func TestAuthStatusOmitsClientCredentials(t *testing.T) {
	ccEnv(t, false)
	kr := &testutil.MemKeyring{}
	saved := keyringFactory
	t.Cleanup(func() { keyringFactory = saved })
	keyringFactory = func(config.Paths) auth.Keyring { return kr }
	code, stdout, stderr := run(t, "auth", "status", "--json")
	var envelope struct {
		Data struct{ Items []struct{ Connection string } }
	}
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &envelope) != nil {
		t.Fatal(code, stdout, stderr)
	}
	if len(envelope.Data.Items) != 1 || envelope.Data.Items[0].Connection != "local:code" || kr.Gets() != 1 {
		t.Fatal(stdout, kr.Gets())
	}
}

// Headless mode has no browser: a sign-in through auth <mcp> is refused
// before the runtime starts and nothing claims to open a browser, also when
// the connection's client secret comes from a service-account profile:
// nothing is read again first.
func TestAuthSignInHeadlessRefused(t *testing.T) {
	paths := ccEnv(t, true)
	for _, args := range [][]string{{"--json"}, {}} {
		code, stdout, stderr := run(t, append([]string{"auth", "code"}, args...)...)
		if code != 3 || strings.Contains(stdout+stderr, "browser to sign in") {
			t.Fatal(code, stdout, stderr)
		}
		if len(args) > 0 {
			if e := envelopeError(t, stdout); e.Code != "auth_required" || e.Message != "This server needs sign-in, which headless mode cannot do." || stderr != "" {
				t.Fatal(stdout, stderr)
			}
		}
	}
	noRuntime(t, paths)
	paths = bothEnv(t, true)
	code, stdout, stderr := run(t, "auth", "both", "--json")
	if e := envelopeError(t, stdout); code != 3 || e.Code != "auth_required" || e.Message != "This server needs sign-in, which headless mode cannot do." || stderr != "" {
		t.Fatal(code, stdout, stderr)
	}
	noRuntime(t, paths)
}

// bothEnv is desktop or headless mode with OAuth connection both, whose
// client secret is a 1Password reference read through service-account
// profile ops. It returns the paths commands use, as ccEnv does.
func bothEnv(t *testing.T, headless bool) config.Paths {
	t.Helper()
	paths := ccEnv(t, headless)
	personal := `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{
		"both":{"credentialProfile":"team","transport":{"type":"http","url":"https://both.example/mcp"},"auth":{"type":"oauth","clientId":"id","clientSecret":{"secret":"op://v/i/f"}}}}}`
	selections := `{"schemaVersion":1,"revision":1,"connections":{"local:both":{"enabled":true,"credentialProfile":"ops"}}}`
	raw, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	var local map[string]any
	if err = json.Unmarshal(raw, &local); err != nil {
		t.Fatal(err)
	}
	local["credentialProfiles"] = map[string]any{"ops": map[string]any{"mode": "service-account", "tokenEnv": "OP_SERVICE_ACCOUNT_TOKEN"}}
	if raw, err = json.Marshal(local); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{paths.PersonalFile: personal, paths.ConfigFile: string(raw), paths.SelectionsFile: selections} {
		if err = os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

// --no-input never opens a browser: a planned sign-in is refused offline,
// before any 1Password effect, also when the client secret is a 1Password
// reference.
func TestAuthSignInNoInputOffline(t *testing.T) {
	for _, name := range []string{"code", "both"} {
		t.Run(name, func(t *testing.T) {
			env := ccEnv
			if name == "both" {
				env = bothEnv
			}
			paths := env(t, false)
			code, stdout, stderr := run(t, "auth", name, "--no-input", "--json")
			e := envelopeError(t, stdout)
			if code != 3 || e.Code != "auth_required" || e.Message != "Signing in to "+name+" opens a browser, which --no-input does not allow." || e.NextAction != "Run mcparcel auth "+name+" without --no-input." || stderr != "" {
				t.Fatal(code, stdout, stderr)
			}
			noRuntime(t, paths)
		})
	}
}

// A connection with only literals and env: references has nothing auth
// <mcp> can renew; it is refused offline in both modes.
func TestAuthNothingToAuthenticate(t *testing.T) {
	const personal = `{"schemaVersion":1,"connections":{
"plain":{"transport":{"type":"stdio","command":"/bin/sh","env":{"K":"v"}}},
"envy":{"transport":{"type":"stdio","command":"/bin/sh","env":{"K":{"secret":"env:FIXTURE_TOKEN"}}}},
"keyed":{"transport":{"type":"http","url":"https://keyed.example/mcp","headers":{"Authorization":{"secret":"env:FIXTURE_TOKEN"}}}}}}`
	for _, headless := range []bool{false, true} {
		paths := ccEnv(t, headless)
		if err := os.WriteFile(paths.PersonalFile, []byte(personal), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, next := range map[string]string{"plain": "", "envy": "mcparcel runtime restart", "keyed": "mcparcel runtime restart"} {
			code, stdout, stderr := run(t, "auth", name, "--json")
			e := envelopeError(t, stdout)
			msg := name + " has no sign-in, client credentials or 1Password secrets to refresh."
			if next != "" {
				msg += " Its env: values are read when the runtime starts."
			}
			if code != 2 || e.Code != "invalid_arguments" || e.Message != msg || e.NextAction != next || stderr != "" {
				t.Fatal(headless, name, code, stdout, stderr)
			}
		}
		noRuntime(t, paths)
	}
}

// auth <mcp> prints one line per step, in step order.
func TestAuthHumanLines(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		data runtimeclient.AuthData
		want string
	}{
		{runtimeclient.AuthData{SecretsRefreshed: &yes}, "Read the 1Password secrets for n again.\n"},
		{runtimeclient.AuthData{SignedIn: &yes}, "Signed in to n.\n"},
		{runtimeclient.AuthData{SignedIn: &no}, "n did not ask for sign-in. Nothing was stored.\n"},
		{runtimeclient.AuthData{SecretsRefreshed: &yes, SignedIn: &yes}, "Read the 1Password secrets for n again.\nSigned in to n.\n"},
		{runtimeclient.AuthData{TokenRenewed: &yes}, "Got a new access token for n.\n"},
		{runtimeclient.AuthData{SecretsRefreshed: &yes, TokenRenewed: &yes}, "Read the 1Password secrets for n again.\nGot a new access token for n.\n"},
	} {
		if got := authLines("n", tc.data); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.data, got, tc.want)
		}
	}
}

// output.AuthSubcommands, which next actions route around, must name every
// auth subcommand.
func TestAuthSubcommandsReserved(t *testing.T) {
	parser, err := kong.New(&CLI{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, node := range parser.Model.Children {
		if node.Name != "auth" {
			continue
		}
		for _, child := range node.Children {
			if child.Type == kong.CommandNode {
				names = append(names, child.Name)
			}
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, output.AuthSubcommands) {
		t.Fatal(names, output.AuthSubcommands)
	}
}

func TestSignInPromptWording(t *testing.T) {
	if got := signInPrompt("n", "https://as.example/authorize?x=1", true); got != "Opening your browser to sign in to n.\nIf it does not open, visit:\nhttps://as.example/authorize?x=1\n" {
		t.Fatal(got)
	}
	got := signInPrompt("n", "https://as.example/authorize?x=1", false)
	if got != "To sign in to n, open this URL in a browser:\nhttps://as.example/authorize?x=1\n" || strings.Contains(got, "Opening") {
		t.Fatal(got)
	}
}
