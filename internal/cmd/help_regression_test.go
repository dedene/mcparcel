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
