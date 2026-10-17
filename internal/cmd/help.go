package cmd

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/alecthomas/kong"

	"github.com/dedene/mcparcel/internal/output"
)

const appDescription = "One CLI for every MCP call: team catalogs, personal selections, on-demand credentials."

// commandGroups titles the --help sections; each top-level command names its
// group with a group tag, and its subcommands inherit it.
var commandGroups = []kong.Group{
	{Key: "use", Title: "Use MCP tools:"},
	{Key: "choose", Title: "Choose connections:"},
	{Key: "catalogs", Title: "Catalogs and personal definitions:"},
	{Key: "credentials", Title: "Credentials and configuration:"},
	{Key: "diagnose", Title: "Diagnose and help:"},
}

// shortHelpExamples open the short help bare mcparcel prints.
var shortHelpExamples = []string{
	"mcparcel tools context7",
	"mcparcel call context7.resolve-library-id libraryName=react",
	"mcparcel setup",
}

// helpData is the JSON form of help text: --help, help and bare mcparcel
// with --json.
type helpData struct {
	Help string `json:"help"`
}

// HelpCmd is help [command...], the same as mcparcel [command...] --help.
type HelpCmd struct {
	Command []string `arg:"" optional:"" name:"command" json:"-" help:"Command to explain, for example tools or auth status."`
}

// Run prints the help of the named command. A word that names no command,
// or names a hidden one, is a usage error that never repeats the word; a
// near miss of a subcommand is suggested, as on the command line.
func (c *HelpCmd) Run(kctx *kong.Context) error {
	trace, err := kong.Trace(kctx.Kong, c.Command)
	if err != nil {
		return err
	}
	for _, step := range trace.Path {
		if step.Command != nil && step.Command.Hidden {
			return output.UsageError("", "Unknown command.", "", "")
		}
	}
	if trace.Error != nil {
		message := trace.Error.Error()
		var parseErr *kong.ParseError
		if errors.As(trace.Error, &parseErr) && parseErr.Unwrap() != nil {
			message = parseErr.Unwrap().Error()
		}
		reason, _, suggestion := usageReason(message, nil)
		// Under help, any stray word is a command that does not exist.
		if rest, ok := strings.CutPrefix(reason, "Unexpected argument."); ok {
			reason = "Unknown command." + rest
		} else {
			reason, suggestion = "Unknown command.", ""
		}
		return output.UsageError(helpPath(trace.Command()), reason, "", suggestion)
	}
	return trace.PrintUsage(false)
}

// helpPrinter is kong's help, or that text as {"help": ...} with --json.
func helpPrinter(jsonMode bool) kong.HelpPrinter {
	return func(options kong.HelpOptions, kctx *kong.Context) error {
		if !jsonMode {
			return kong.DefaultHelpPrinter(options, kctx)
		}
		var text bytes.Buffer
		stdout := kctx.Stdout
		kctx.Stdout = &text
		err := kong.DefaultHelpPrinter(options, kctx)
		kctx.Stdout = stdout
		if err != nil {
			return err
		}
		return output.WriteJSON(stdout, helpData{Help: text.String()}, nil)
	}
}

// writeShortHelp is what bare mcparcel prints: what it is, a few examples
// and the command groups, pointing at --help for the rest.
func writeShortHelp(w io.Writer, app *kong.Application, jsonMode bool) int {
	text := shortHelp(app)
	var err error
	if jsonMode {
		err = output.WriteJSON(w, helpData{Help: text}, nil)
	} else {
		_, err = io.WriteString(w, text)
	}
	if err != nil {
		return ExitInternal
	}
	return ExitOK
}

func shortHelp(app *kong.Application) string {
	var b strings.Builder
	b.WriteString(appDescription + "\n\nExamples:\n")
	for _, example := range shortHelpExamples {
		b.WriteString("  " + example + "\n")
	}
	var titles []string
	commands := map[string][]string{}
	for _, child := range app.Children {
		group := child.ClosestGroup()
		if child.Hidden || group == nil {
			continue
		}
		title := strings.TrimSuffix(group.Title, ":")
		if _, seen := commands[title]; !seen {
			titles = append(titles, title)
		}
		commands[title] = append(commands[title], child.Name)
	}
	width := 0
	for _, title := range titles {
		width = max(width, len(title))
	}
	b.WriteString("\nCommands:\n")
	for _, title := range titles {
		b.WriteString("  " + title + strings.Repeat(" ", width-len(title)+2) + strings.Join(commands[title], ", ") + "\n")
	}
	b.WriteString("\nRun mcparcel --help for all commands.\n")
	return b.String()
}

// Help gives the most used commands examples in their --help.

func (c *CallCmd) Help() string {
	return `Examples:

    mcparcel call context7.resolve-library-id libraryName=react
    mcparcel call paper.search --args '{"query":"mcp","limit":5}' --json`
}

func (c *ToolsCmd) Help() string {
	return `Examples:

    mcparcel tools context7
    mcparcel tools disable context7 query-docs`
}

func (c *EnableCmd) Help() string {
	return `Examples:

    mcparcel enable context7
    mcparcel enable context7 paper`
}

func (c *AddCmd) Help() string {
	return `Examples:

    mcparcel add acme/mcp-catalog
    mcparcel add acme/platform --path mcp/catalog.json --ref main`
}

func (c *JoinCmd) Help() string {
	return `Examples:

    mcparcel join acme/mcp-catalog`
}

func (c *AuthCmd) Help() string {
	return `Examples:

    mcparcel auth glitchtip
    mcparcel auth status`
}
