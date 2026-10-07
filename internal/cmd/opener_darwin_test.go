//go:build !mcparceltest

package cmd

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/elicit"
)

// The dialog runs only /usr/bin/osascript with a fixed script; server text is
// argv after it, never script source. The command is built, never run.
func TestDialogCommand(t *testing.T) {
	text := `"; do shell script "touch /tmp/x" --`
	argv := elicit.DialogArgs("fixture", elicit.Prompt{Message: text, Persist: []string{"session"}})
	cmd := dialogCommand(context.Background(), argv)
	if cmd.Path != "/usr/bin/osascript" || cmd.Args[0] != "/usr/bin/osascript" {
		t.Fatal(cmd.Path, cmd.Args)
	}
	script := cmd.Args[1 : len(cmd.Args)-len(argv)]
	if !slices.Equal(cmd.Args[len(cmd.Args)-len(argv):], argv) || len(script) != 2*len(dialogScript) {
		t.Fatal(cmd.Args)
	}
	for i, line := range dialogScript {
		if script[2*i] != "-e" || script[2*i+1] != line || strings.Contains(line, "touch") {
			t.Fatal(script)
		}
	}
	if !strings.HasPrefix(argv[0], "MCParcel") || !slices.Equal(argv[3:], []string{"Decline", "Allow once", "Allow for this session"}) {
		t.Fatal(argv)
	}
}
