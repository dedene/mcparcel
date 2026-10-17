package cmd

import (
	"fmt"
	"io"

	"github.com/alecthomas/kong"

	"github.com/dedene/mcparcel/internal/output"
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
func (c *VersionCmd) Run(s *Streams, opts *CommandOptions) error {
	if err := printVersion(s.Out, opts.JSON); err != nil {
		return &commandWriteFailure{err}
	}
	return nil
}

// versionFlag is --version (-V): it prints what the version command prints
// and exits before any command runs.
type versionFlag bool

// jsonIntent is --json as scanned before parsing, for output written while
// kong parses (--version, --help), before the JSON flag is set.
type jsonIntent bool

// BeforeReset runs while parsing, so --version wins over any command.
func (versionFlag) BeforeReset(app *kong.Kong, jsonMode jsonIntent) error {
	if printVersion(app.Stdout, bool(jsonMode)) != nil {
		app.Exit(ExitInternal)
	}
	app.Exit(ExitOK)
	return nil
}

// versionData is the JSON form of the version line.
type versionData struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Date    string `json:"date,omitempty"`
}

func printVersion(w io.Writer, jsonMode bool) error {
	if jsonMode {
		return output.WriteJSON(w, versionData{Version: version, Commit: commit, Date: date}, nil)
	}
	line := "mcparcel " + version
	if commit != "" {
		line += fmt.Sprintf(" (%s, %s)", commit, date)
	}
	_, err := fmt.Fprintln(w, line)
	return err
}
