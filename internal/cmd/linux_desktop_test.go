package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

// Over SSH, a prompt without a browser tells how to reach the loopback
// callback from a browser elsewhere; localhost becomes 127.0.0.1 and ::1 is
// bracketed. With a browser, or outside SSH, there is no hint.
func TestSignInPromptSSHHint(t *testing.T) {
	authorize := func(redirect string) string {
		return "https://as.example/authorize?client_id=x&redirect_uri=" + redirect + "&state=s"
	}
	t.Setenv("SSH_CONNECTION", "10.0.0.2 52000 10.0.0.1 22")
	for redirect, hint := range map[string]string{
		"http%3A%2F%2F127.0.0.1%3A53682%2Fcallback": "The sign-in returns to http://127.0.0.1:53682 on this machine. If your browser runs elsewhere, first run there: ssh -N -L 53682:127.0.0.1:53682 <this host>\n",
		"http%3A%2F%2Flocalhost%3A8080%2Fcallback":  "The sign-in returns to http://127.0.0.1:8080 on this machine. If your browser runs elsewhere, first run there: ssh -N -L 8080:127.0.0.1:8080 <this host>\n",
		"http%3A%2F%2F%5B%3A%3A1%5D%3A9000%2Fcb":    "The sign-in returns to http://[::1]:9000 on this machine. If your browser runs elsewhere, first run there: ssh -N -L 9000:[::1]:9000 <this host>\n",
	} {
		u := authorize(redirect)
		if got := signInPrompt("n", u, false); got != "To sign in to n, open this URL in a browser:\n"+u+"\n"+hint {
			t.Error(got)
		}
		if got := signInPrompt("n", u, true); strings.Contains(got, "ssh") {
			t.Error(got)
		}
	}
	for _, u := range []string{authorize("https%3A%2F%2Fapp.example%2Fcb"), authorize("http%3A%2F%2F127.0.0.1%2Fcb"), "https://as.example/authorize", "%zz"} {
		if got := signInPrompt("n", u, false); strings.Contains(got, "ssh") {
			t.Error(u, got)
		}
	}
	t.Setenv("SSH_CONNECTION", "")
	if got := signInPrompt("n", authorize("http%3A%2F%2F127.0.0.1%3A53682%2Fcallback"), false); strings.Contains(got, "ssh") {
		t.Error(got)
	}
}

// openPersonal has marked connection code and unmarked connection open.
const openPersonal = `{"schemaVersion":1,"connections":{
"code":{"transport":{"type":"http","url":"https://code.example/mcp"},"auth":{"type":"oauth"}},
"open":{"transport":{"type":"http","url":"https://open.example/mcp"}}}}`

// keyringEnv writes personal in desktop mode with a config.json and a keyring
// that fails every call with err.
func keyringEnv(t *testing.T, personal string, err error) config.Paths {
	t.Helper()
	paths := ccEnv(t, false)
	if e := os.WriteFile(paths.PersonalFile, []byte(personal), 0o600); e != nil {
		t.Fatal(e)
	}
	saved := keyringFactory
	t.Cleanup(func() { keyringFactory = saved })
	kr := &testutil.MemKeyring{Err: err}
	keyringFactory = func(config.Paths) auth.Keyring { return kr }
	return paths
}

// The auth status listing leaves out an unmarked connection the keyring
// cannot read; a marked one, or any connection named explicitly, still fails
// keychain_unavailable.
func TestAuthStatusKeyringErrorListingOnly(t *testing.T) {
	keyringEnv(t, `{"schemaVersion":1,"connections":{"open":{"transport":{"type":"http","url":"https://open.example/mcp"}}}}`, errors.New("no session bus"))
	code, stdout, stderr := run(t, "auth", "status", "--json")
	var envelope struct {
		Data struct{ Items []struct{ Connection string } }
	}
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &envelope) != nil {
		t.Fatal(code, stdout, stderr)
	}
	for _, item := range envelope.Data.Items {
		if item.Connection == "local:open" {
			t.Fatal(stdout)
		}
	}
	code, stdout, stderr = run(t, "auth", "status", "open", "--json")
	if e := envelopeError(t, stdout); code != 3 || e.Code != "keychain_unavailable" || stderr != "" {
		t.Fatal(code, stdout, stderr)
	}
	keyringEnv(t, openPersonal, errors.New("no session bus"))
	code, stdout, stderr = run(t, "auth", "status", "--json")
	if e := envelopeError(t, stdout); code != 3 || e.Code != "keychain_unavailable" || stderr != "" {
		t.Fatal(code, stdout, stderr)
	}
}

// Without a reachable keyring and with no runtime running, auth login is
// refused offline: no runtime starts and nothing is written.
func TestAuthLoginKeyringUnreachable(t *testing.T) {
	paths := keyringEnv(t, openPersonal, nil)
	saved := keyringReachableCheck
	t.Cleanup(func() { keyringReachableCheck = saved })
	keyringReachableCheck = func(config.Paths) bool { return false }
	want := output.KeyringUnreachableError()
	for _, name := range []string{"code", "open"} {
		code, stdout, stderr := run(t, "auth", "login", name, "--json")
		if e := envelopeError(t, stdout); code != 3 || e.Code != "keychain_unavailable" || e.Message != want.Message || e.NextAction != want.NextAction || strings.Contains(stderr, "browser") {
			t.Fatal(code, stdout, stderr)
		}
	}
	noRuntime(t, paths)
	if entries, err := os.ReadDir(paths.StateDir); err == nil && len(entries) > 0 {
		t.Fatal("state written", entries)
	}
}
