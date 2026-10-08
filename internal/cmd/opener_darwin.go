//go:build !mcparceltest

package cmd

import (
	"context"
	"os/exec"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

// browserOpens reports whether newBrowser can open a browser at all; auth
// login words its prompt by it. macOS always can.
func browserOpens() bool { return true }

// newBrowser opens a sign-in URL that validSignInURL accepts in the default
// browser. The URL is one argv entry; no shell runs.
func newBrowser(config.Paths) func(context.Context, string) error {
	return func(ctx context.Context, raw string) error {
		if err := validSignInURL(raw); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "/usr/bin/open", raw).Run()
	}
}

// dialogScript shows argv as title, text, timeout in seconds and buttons;
// server text only ever arrives as argv, never as script source.
var dialogScript = []string{
	"on run argv",
	"set r to display dialog (item 2 of argv) with title (item 1 of argv) buttons (items 4 thru -1 of argv) default button 1 giving up after ((item 3 of argv) as integer) with icon caution",
	"if gave up of r then return \"\"",
	"return button returned of r",
	"end run",
}

func newDialog(config.Paths) func(context.Context, []string) (string, error) {
	return func(ctx context.Context, argv []string) (string, error) {
		out, err := dialogCommand(ctx, argv).Output()
		return string(out), err
	}
}

func dialogCommand(ctx context.Context, argv []string) *exec.Cmd {
	args := make([]string, 0, 2*len(dialogScript)+len(argv))
	for _, line := range dialogScript {
		args = append(args, "-e", line)
	}
	return exec.CommandContext(ctx, "/usr/bin/osascript", append(args, argv...)...)
}
