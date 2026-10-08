package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
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
			for _, argv := range [][]string{{"auth", "login", "front"}, {"auth", "logout", "front"}, {"auth", "logout", "local:front"}, {"auth", "status", "front"}} {
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

// Headless mode has no browser: auth login is refused before the runtime
// starts and nothing claims to open a browser.
func TestAuthLoginHeadlessRefused(t *testing.T) {
	paths := ccEnv(t, true)
	for _, args := range [][]string{{"--json"}, {}} {
		code, stdout, stderr := run(t, append([]string{"auth", "login", "code"}, args...)...)
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
