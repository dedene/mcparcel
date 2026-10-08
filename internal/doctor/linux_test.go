package doctor

import (
	"slices"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/output"
)

// linuxInput is a desktop Input as on Linux: no 1Password desktop app and a
// keyring reached through the session bus, reachable when bus is true.
func linuxInput(t *testing.T, d docs, bus bool) Input {
	t.Helper()
	in := inputFor(t, d)
	in.DesktopOnePassword = false
	in.KeyringReachable = func() bool { return bus }
	return in
}

// Without the desktop app, a profile that uses it fails credentials.profile
// with the runtime's text, and prereq.onepassword is not shown.
func TestDesktopAppProfileUnavailable(t *testing.T) {
	for _, bootstrap := range []string{"", "op://v/bootstrap/token"} {
		checks := Offline(linuxInput(t, opDocs("op://v/i/f", bootstrap), true))
		c := find(t, checks, "credentials.profile", "local:op")
		want(t, c, Fail, "config_required")
		if e := output.DesktopAppUnavailableError("p"); c.Message != e.Message || c.NextAction != e.NextAction {
			t.Fatal(c.Message, c.NextAction)
		}
		absent(t, checks, "prereq.onepassword")
	}
	// A service-account profile works without the app.
	in := linuxInput(t, saDocs(`"tokenEnv":"OP_SERVICE_ACCOUNT_TOKEN"`), true)
	want(t, find(t, Offline(in), "credentials.profile", "local:op"), OK, "")
	// Headless mode reports desktop profiles through config.connection.
	h := headlessInput(linuxInput(t, opDocs("op://v/i/f", ""), true))
	want(t, find(t, Offline(h), "credentials.profile", "local:op"), OK, "")
}

const signInDocsPersonal = `{"schemaVersion":1,"connections":{"pkce":{"transport":{"type":"http","url":"https://a.example.invalid/mcp"},"auth":{"type":"oauth"}},"open":{"transport":{"type":"http","url":"https://c.example.invalid/mcp"}},"cc":{"transport":{"type":"http","url":"https://b.example.invalid/mcp"},"auth":{"type":"oauth","grant":"client_credentials","tokenUrl":"https://b.example.invalid/token","clientId":{"secret":"env:CC_ID"},"clientSecret":{"secret":"env:CC_SECRET"}}}}}`

func signInDocs(enabled ...string) docs {
	sel := `{"schemaVersion":1,"revision":1,"connections":{`
	for i, id := range enabled {
		if i > 0 {
			sel += ","
		}
		sel += `"local:` + id + `":{"enabled":true}`
	}
	return docs{personal: signInDocsPersonal, selections: sel + `}}`}
}

// prereq.keyring: OK with a bus; without one it fails when a connection is
// marked OAuth, else warns. Neither stdio nor client_credentials connections,
// nor macOS (KeyringReachable nil) or headless mode, get the row.
func TestKeyringRow(t *testing.T) {
	c := find(t, Offline(linuxInput(t, signInDocs("pkce"), true)), "prereq.keyring", "")
	want(t, c, OK, "")
	if c.Message != "A D-Bus session bus is reachable; doctor does not check that a Secret Service provider runs or is unlocked." {
		t.Fatal(c.Message)
	}
	e := output.KeyringUnreachableError()
	c = find(t, Offline(linuxInput(t, signInDocs("pkce", "cc"), false)), "prereq.keyring", "")
	want(t, c, Fail, "keychain_unavailable")
	if c.Message != e.Message || c.NextAction != e.NextAction {
		t.Fatal(c.Message, c.NextAction)
	}
	want(t, find(t, Offline(linuxInput(t, signInDocs("open"), false)), "prereq.keyring", ""), Warn, "keychain_unavailable")
	absent(t, Offline(linuxInput(t, signInDocs("cc"), false)), "prereq.keyring")
	absent(t, Offline(linuxInput(t, docs{personal: stdioPaper, selections: enabledPaper}, false)), "prereq.keyring")
	absent(t, Offline(inputFor(t, signInDocs("pkce"))), "prereq.keyring")
	absent(t, Offline(headlessInput(linuxInput(t, signInDocs("open"), false))), "prereq.keyring")
	// The row is the last global row.
	got := ids(Offline(linuxInput(t, signInDocs("pkce"), true)))
	at := slices.Index(got, "prereq.keyring")
	if at < 1 || got[at-1] != "config.summary" || !strings.HasPrefix(got[at+1], "config.connection ") {
		t.Fatal(got)
	}
}
