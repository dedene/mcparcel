package output

import "testing"

func TestAuthTarget(t *testing.T) {
	for _, tc := range []struct{ name, canonical, want string }{
		{"n", "local:n", "n"},
		{"local:n", "local:n", "local:n"},
		{"alias", "github:o/r#n", "alias"},
		{"status", "local:status", "local:status"},
		{"logout", "github:o/r#logout", "github:o/r#logout"},
		{"lock", "local:lock", "local:lock"},
		{"", "local:n", "local:n"},
	} {
		if got := AuthTarget(tc.name, tc.canonical); got != tc.want {
			t.Errorf("AuthTarget(%q, %q) = %q, want %q", tc.name, tc.canonical, got, tc.want)
		}
	}
	if got := AuthAction("status", "local:status"); got != "mcparcel auth local:status" {
		t.Fatal(got)
	}
}

func TestRereadNeedsInputError(t *testing.T) {
	e := RereadNeedsInputError("a", "local:a")
	if e.Code != "auth_required" || ExitCode(e) != 3 ||
		e.Message != "Reading a's 1Password secrets again may need approval in the 1Password app, which --no-input does not allow." ||
		e.NextAction != "Run mcparcel auth a without --no-input." {
		t.Fatalf("%+v", e)
	}
}
