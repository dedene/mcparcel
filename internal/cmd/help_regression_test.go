package cmd

import (
	"strings"
	"testing"
)

func TestOfflineCommandHelpDescriptionsAndPositionals(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		wants []string
	}{
		{[]string{"local", "--help"}, []string{"Add a personal connection definition offline.", "Update a personal connection definition offline.", "Remove a personal connection definition offline."}},
		{[]string{"local", "add", "--help"}, []string{"Add a personal connection definition offline."}},
		{[]string{"local", "update", "--help"}, []string{"Update a personal connection definition offline."}},
		{[]string{"local", "remove", "--help"}, []string{"Remove a personal connection definition offline."}},
		{[]string{"enable", "--help"}, []string{"<mcp> ...", "Enable and accept selected connections offline."}},
		{[]string{"disable", "--help"}, []string{"<mcp> ...", "Disable selected connections offline."}},
		{[]string{"auth", "--help"}, []string{"auth <mcp>", "Make a connection's credentials fresh: sign in again, read its 1Password", "auth status [<mcp>]", "Show stored sign-in and 1Password session state without contacting servers.", "auth logout <mcp>", "Remove the stored sign-in for a connection.", "auth lock", "End 1Password sessions and block stored sign-ins until the next mcparcel"}},
		{[]string{"auth", "n", "--help"}, []string{"Usage: mcparcel auth <mcp>"}},
		{[]string{"auth", "status", "--help"}, []string{"[<mcp>]"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			exit, stdout, stderr := run(t, tc.args...)
			if exit != 0 || stderr != "" {
				t.Fatal(exit, stdout, stderr)
			}
			for _, want := range tc.wants {
				if !strings.Contains(stdout, want) {
					t.Errorf("help missing %q: %s", want, stdout)
				}
			}
			if strings.Contains(stdout, "auth login") || strings.Contains(stdout, "auth refresh") {
				t.Errorf("help names a removed command: %s", stdout)
			}
		})
	}
}

// The tools help names its real forms: no catch-all positional, no
// enable/disable smuggled into <mcp>, and no --cached while it cannot work.
func TestToolsHelpIsTruthful(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		wants []string
	}{
		{[]string{"tools", "--help"}, []string{"tools <mcp>", "tools list <mcp>", "also reaches a connection named list, enable or", "tools enable <mcp> <tool> ...", "tools disable <mcp> <tool> ..."}},
		{[]string{"tools", "paper", "--help"}, []string{"Usage: mcparcel tools <mcp>"}},
		{[]string{"tools", "list", "--help"}, []string{"Usage: mcparcel tools list <mcp>"}},
		{[]string{"tools", "enable", "--help"}, []string{"Usage: mcparcel tools enable <mcp> <tool> ...", "Tools to turn on."}},
		{[]string{"tools", "disable", "--help"}, []string{"Usage: mcparcel tools disable <mcp> <tool> ...", "Tools to turn off."}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			exit, stdout, stderr := run(t, tc.args...)
			if exit != 0 || stderr != "" {
				t.Fatal(exit, stdout, stderr)
			}
			for _, want := range tc.wants {
				if !strings.Contains(stdout, want) {
					t.Errorf("help missing %q: %s", want, stdout)
				}
			}
			for _, banned := range []string{"<rest>", "--cached", "enable/disable"} {
				if strings.Contains(stdout, banned) {
					t.Errorf("help names %q: %s", banned, stdout)
				}
			}
		})
	}
}

// --cached is hidden but still accepted, and still a cache miss.
func TestToolsCachedStillAccepted(t *testing.T) {
	metadataEnv(t)
	for _, argv := range [][]string{{"tools", "paper", "--cached"}, {"tools", "list", "paper", "--cached"}} {
		code, stdout, stderr := run(t, append(argv, "--json")...)
		if code != 6 || stderr != "" || !strings.Contains(stdout, `"code":"schema_cache_miss"`) {
			t.Fatal(argv, code, stdout, stderr)
		}
	}
}
