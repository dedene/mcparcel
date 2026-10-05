package cmd

import "fmt"

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
	line := "mcparcel " + version
	if commit != "" {
		line += fmt.Sprintf(" (%s, %s)", commit, date)
	}
	_, err := fmt.Fprintln(s.Out, line)
	return err
}
