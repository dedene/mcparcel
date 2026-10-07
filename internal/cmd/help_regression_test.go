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
		{[]string{"auth", "--help"}, []string{"Sign in to an HTTP connection in the browser.", "Show stored sign-in and 1Password session state without contacting servers.", "Remove the stored sign-in for a connection.", "End 1Password sessions and block stored sign-ins until the next auth login.", "Read a connection's 1Password secrets again on its next call."}},
		{[]string{"auth", "login", "--help"}, []string{"<mcp>"}},
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
		})
	}
}
