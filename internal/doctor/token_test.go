package doctor

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// saDocs binds one op:// connection to a service-account profile whose token
// comes from source, a JSON fragment such as "tokenEnv":"OP_X".
func saDocs(source string) docs {
	d := opDocs("op://v/i/f", "")
	d.local = `{"schemaVersion":1,"credentialProfiles":{"p":{"mode":"service-account",` + source + `}}}`
	return d
}

// noTokenFile fails the test if doctor looks at a token file.
func noTokenFile(t *testing.T) func(string) error {
	return func(path string) error {
		t.Fatal("doctor checked a token file:", path)
		return nil
	}
}

func TestTokenEnvRow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		headless bool
		set      bool
		status   string
		message  string
	}{
		{"desktop set", false, true, OK, "OP_X is set in this shell; the runtime reads your login-shell environment."},
		{"headless set", true, true, OK, "OP_X is set in this shell; the runtime reads its own environment."},
		{"desktop missing", false, false, Warn, "OP_X is not set in this shell; the runtime reads your login-shell environment."},
		{"headless missing", true, false, Warn, "OP_X is not set in this shell; the runtime reads its own environment."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OP_X", "token-canary")
			in := inputFor(t, saDocs(`"tokenEnv":"OP_X"`))
			if tc.headless {
				in = headlessInput(in)
			}
			in.TokenFile = noTokenFile(t)
			var asked []string
			in.LookupEnv = func(name string) bool { asked = append(asked, name); return tc.set }
			checks := Offline(in)
			c := find(t, checks, "credentials.token", "local:op")
			want(t, c, tc.status, "")
			if c.Message != tc.message || !slices.Contains(asked, "OP_X") {
				t.Fatal(c, asked)
			}
			if !tc.set && !strings.HasPrefix(c.NextAction, map[bool]string{false: "Export OP_X where your login shell reads it", true: "Set OP_X in the environment that starts mcparcel"}[tc.headless]) {
				t.Fatal(c.NextAction)
			}
			for _, row := range checks {
				if strings.Contains(row.Message+row.NextAction, "token-canary") {
					t.Fatal("token in row:", row)
				}
			}
			// The connection is servable in both modes.
			want(t, find(t, checks, "config.connection", "local:op"), OK, "")
		})
	}
}

func TestTokenFileRow(t *testing.T) {
	for _, tc := range []struct {
		name          string
		err           error
		status, code  string
		message, next string
	}{
		{"ok", nil, OK, "", "Token file /run/op exists and is private.", ""},
		{
			"missing", os.ErrNotExist, Fail, "config_required",
			"The 1Password service-account token file /run/op of profile p is missing, unreadable, empty or not a single token.",
			"Write the token to /run/op (one line, mode 600, owned by the user that runs mcparcel). MCParcel reads it again on the next call; no restart is needed.",
		},
		{"unreadable", os.ErrPermission, Fail, "config_required", "", ""},
		{"empty", config.ErrTokenEmpty, Fail, "config_required", "", ""},
		{
			"unsafe", config.ErrUnsafePath, Fail, "unsafe_local_path",
			"The token file /run/op of profile p is unsafe: it must be a regular file owned by you or root, never readable by others, and readable or writable by its group only on a read-only mount.",
			"Run chmod 600 /run/op. In Kubernetes, mount the Secret with defaultMode 0440 and fsGroup set to the container's group.",
		},
	} {
		for _, headlessMode := range []bool{false, true} {
			in := inputFor(t, saDocs(`"tokenFile":"/run/op"`))
			if headlessMode {
				in = headlessInput(in)
			}
			var checked []string
			// Doctor gets an error or nil, never content.
			in.TokenFile = func(path string) error { checked = append(checked, path); return tc.err }
			in.LookupEnv = func(name string) bool { t.Fatal("tokenFile looked at the environment:", name); return false }
			c := find(t, Offline(in), "credentials.token", "local:op")
			want(t, c, tc.status, tc.code)
			if tc.message != "" && (c.Message != tc.message || c.NextAction != tc.next) || !slices.Equal(checked, []string{"/run/op"}) {
				t.Fatal(tc.name, c, checked)
			}
		}
	}
}

// The real check opens and stats the file without reading it.
func TestTokenFileDefaultCheck(t *testing.T) {
	dir := tempDir(t)
	path := dir + "/token"
	if err := os.WriteFile(path, []byte("token-canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := inputFor(t, saDocs(`"tokenFile":"`+path+`"`))
	c := find(t, Offline(in), "credentials.token", "local:op")
	want(t, c, OK, "")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	want(t, find(t, Offline(in), "credentials.token", "local:op"), Fail, "unsafe_local_path")
}

// A service-account-only configuration needs no 1Password app; headless
// checks its references.
func TestServiceAccountRows(t *testing.T) {
	in := inputFor(t, saDocs(`"tokenEnv":"OP_X"`))
	checks := Offline(in)
	absent(t, checks, "prereq.onepassword")
	got := ids(filterSubject(checks, "local:op"))
	wantIDs := []string{"config.connection local:op", "credentials.profile local:op", "credentials.token local:op", "credentials.reference local:op", "prereq.command local:op"}
	if !slices.Equal(got, wantIDs) {
		t.Fatalf("rows %v\nwant %v", got, wantIDs)
	}
	h := headlessInput(inputFor(t, saDocs(`"tokenEnv":"OP_X"`)))
	checks = Offline(h)
	absent(t, checks, "prereq.onepassword")
	if got = ids(filterSubject(checks, "local:op")); !slices.Equal(got, wantIDs) {
		t.Fatalf("headless rows %v\nwant %v", got, wantIDs)
	}
	want(t, find(t, checks, "credentials.reference", "local:op"), OK, "")
	// A desktop profile in headless still gets no reference or token row.
	checks = Offline(headlessInput(inputFor(t, opDocs("op://v/i/f", ""))))
	absent(t, checks, "credentials.reference")
	absent(t, checks, "credentials.token")
	// A desktop profile on desktop still needs the app.
	find(t, Offline(inputFor(t, opDocs("op://v/i/f", ""))), "prereq.onepassword", "")
}
