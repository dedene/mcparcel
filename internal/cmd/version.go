package cmd

import (
	"fmt"
	"io"

	"github.com/alecthomas/kong"
)

// Set through -ldflags at build time; see Makefile.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// VersionCmd prints build information.
type VersionCmd struct{}

// Run implements the version command.
func (c *VersionCmd) Run(s *Streams) error {
	return printVersion(s.Out)
}

// versionFlag is --version: it prints what the version command prints and
// exits before any command runs.
type versionFlag bool

// BeforeReset runs while parsing, so --version wins over any command.
func (versionFlag) BeforeReset(app *kong.Kong) error {
	if printVersion(app.Stdout) != nil {
		app.Exit(ExitInternal)
	}
	app.Exit(ExitOK)
	return nil
}

func printVersion(w io.Writer) error {
	line := "mcparcel " + version
	if commit != "" {
		line += fmt.Sprintf(" (%s, %s)", commit, date)
	}
	_, err := fmt.Fprintln(w, line)
	return err
}
