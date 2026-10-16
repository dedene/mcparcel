package output

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

// Errors that name what failed carry only identifiers that pass a grammar:
// connection IDs, MCP tool names and command flag names. Anything else, such
// as a value, a path or a mistyped token that could be a pasted secret, is
// left out and the generic message stands.

var (
	shortConnectionName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	// toolName is the MCP tool name grammar: 1 to 128 of A-Z a-z 0-9 _ - . /.
	toolName = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,128}$`)
)

// SafeConnectionName reports whether name is a connection ID that may appear
// in a message: a short name or a canonical ID.
func SafeConnectionName(name string) bool {
	if strings.Contains(name, ":") {
		return config.ValidateCanonicalID(name) == nil
	}
	return len(name) <= 64 && shortConnectionName.MatchString(name)
}

// SafeToolName reports whether name is an MCP tool name that may appear in a
// message.
func SafeToolName(name string) bool { return toolName.MatchString(name) }

// Closest returns the candidate nearest to name by edit distance, or "" when
// none is close enough to be a likely typo. Ties go to the earlier candidate.
func Closest(name string, candidates []string) string {
	best, bestDistance := "", 0
	limit := max(2, len(name)/3)
	for _, c := range candidates {
		if c == name || c == "" {
			continue
		}
		d := editDistance(name, c)
		if d > limit || d >= max(len(name), len(c)) {
			continue
		}
		if best == "" || d < bestDistance {
			best, bestDistance = c, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b in bytes; the
// names compared are ASCII.
func editDistance(a, b string) int {
	row := make([]int, len(b)+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= len(a); i++ {
		prev := row[0]
		row[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			prev, row[j] = row[j], min(row[j]+1, row[j-1]+1, prev+cost)
		}
	}
	return row[len(b)]
}

func didYouMean(suggestion string) string {
	if suggestion == "" {
		return ""
	}
	return " Did you mean " + suggestion + "?"
}

// ConnectionUnavailableError is connection_unavailable naming the connection
// the user typed, with the closest known ID when one is near. A name or
// suggestion that is not a connection ID is left out.
func ConnectionUnavailableError(name, suggestion string) *Error {
	if !SafeConnectionName(name) {
		return NewError("connection_unavailable", nil)
	}
	if !SafeConnectionName(suggestion) {
		suggestion = ""
	}
	err := NewError("connection_unavailable", &Details{Connection: name, Suggestion: suggestion})
	err.Message = "No connection is named " + name + "." + didYouMean(suggestion)
	return err
}

// NameCallTimeout words a timeout or outcome_unknown that a call's own
// deadline caused: it names the deadline and, when both pass their grammars,
// <mcp>.<tool>. The code, exit and next action stay; other codes are left.
func NameCallTimeout(err *Error, connection, tool string, d time.Duration) {
	if err == nil || err.Code != "timeout" && err.Code != "outcome_unknown" {
		return
	}
	who := "The call"
	if SafeConnectionName(connection) && SafeToolName(tool) {
		who = connection + "." + tool
		if err.Details == nil {
			err.Details = &Details{}
		}
		err.Details.Connection, err.Details.Tool = connection, tool
	}
	after := d.String()
	if d > 0 && d%time.Second == 0 {
		after = strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
	if err.Code == "outcome_unknown" {
		err.Message = who + " did not answer within " + after + "; it may have run."
		return
	}
	err.Message = who + " timed out after " + after + " before it was sent."
}

// UnknownDomainError is invalid_arguments for catalog --domain naming a
// domain no catalog declares, with the closest declared ID when one is
// near. A domain outside the identifier grammar is not repeated.
func UnknownDomainError(domain, suggestion string) *Error {
	safe := func(id string) bool { return !strings.Contains(id, ":") && SafeConnectionName(id) }
	if !safe(suggestion) {
		suggestion = ""
	}
	err := NewError("invalid_arguments", &Details{Argument: "--domain", Suggestion: suggestion})
	err.Message = "Unknown domain."
	if safe(domain) {
		err.Message = "Unknown domain " + domain + "." + didYouMean(suggestion)
	}
	err.NextAction = "Run mcparcel catalog to see all of them."
	return err
}

// PersonalConnectionUnavailableError is connection_unavailable for local
// update and local remove, which take only personal connection IDs; the
// suggestion is the closest personal ID.
func PersonalConnectionUnavailableError(name, suggestion string) *Error {
	if !SafeConnectionName(name) {
		return NewError("connection_unavailable", nil)
	}
	if !SafeConnectionName(suggestion) {
		suggestion = ""
	}
	err := NewError("connection_unavailable", &Details{Connection: name, Suggestion: suggestion})
	err.Message = "No personal connection is named " + name + "." + didYouMean(suggestion)
	err.NextAction = "Run mcparcel catalog to see your personal connections, listed as local:<id>."
	return err
}

// ConnectionGoneError is connection_unavailable for a connection the install
// still lists but can no longer use: its catalog or personal definition was
// removed and its selection kept.
func ConnectionGoneError(name string) *Error {
	if !SafeConnectionName(name) {
		return NewError("connection_unavailable", nil)
	}
	err := NewError("connection_unavailable", &Details{Connection: name})
	err.Message = "Connection " + name + " is unavailable: its definition is gone."
	err.NextAction = "Run mcparcel inspect " + name + " to see it, or mcparcel catalog for connections you can use."
	return err
}

// ConfigRequiredError is config_required naming the connection the command
// line gave.
func ConfigRequiredError(name string) *Error {
	if !SafeConnectionName(name) {
		return NewError("config_required", nil)
	}
	err := NewError("config_required", &Details{Connection: name})
	err.Message = "Connection " + name + " needs configuration."
	err.NextAction = "Run mcparcel inspect " + name + " to see what the connection needs, then mcparcel setup."
	return err
}

// ConnectionDisabledError is connection_disabled naming the connection.
func ConnectionDisabledError(name string) *Error {
	if !SafeConnectionName(name) {
		return NewError("connection_disabled", nil)
	}
	err := NewError("connection_disabled", &Details{Connection: name})
	err.Message = "Connection " + name + " is disabled."
	err.NextAction = "Run mcparcel enable " + name + "."
	return err
}

// ToolNotFoundError is tool_not_found naming the connection and the tool,
// with the closest advertised tool name when one is near.
func ToolNotFoundError(connection, tool, suggestion string) *Error {
	if !SafeConnectionName(connection) {
		return NewError("tool_not_found", nil)
	}
	if !SafeToolName(suggestion) {
		suggestion = ""
	}
	named := "by that name"
	if SafeToolName(tool) {
		named = "named " + tool
	} else {
		tool = ""
	}
	err := NewError("tool_not_found", &Details{Connection: connection, Tool: tool, Suggestion: suggestion})
	err.Message = "Connection " + connection + " has no tool " + named + "." + didYouMean(suggestion)
	err.NextAction = "Run " + ToolsCommand(connection) + "."
	return err
}

// ToolsCommand is the command that lists a connection's tools. The tools
// subcommands list, enable and disable shadow a connection of that name in
// tools <mcp>, so such a name goes through tools list <mcp>.
func ToolsCommand(connection string) string {
	switch connection {
	case "list", "enable", "disable":
		return "mcparcel tools list " + connection
	}
	return "mcparcel tools " + connection
}

// NoConnectionsError is config_required on an install with no connections
// yet: nothing is set up, so there is nothing to call.
func NoConnectionsError() *Error {
	err := NewError("config_required", nil)
	err.Message = "No connections are set up yet."
	err.NextAction = "Run mcparcel setup, or mcparcel add <owner/repo>."
	return err
}

// CallTargetError is invalid_arguments for a call target that is missing or
// is not <mcp>.<tool>.
func CallTargetError() *Error {
	err := NewError("invalid_arguments", nil)
	err.Message = "Name the tool as <mcp>.<tool>, for example context7.resolve-library-id."
	err.NextAction = "Run mcparcel list for connection IDs, then mcparcel tools <mcp> for its tools."
	return err
}

// ArgumentsError is invalid_arguments with the reason a call's arguments
// failed: argument keys, expected types and schema paths, never a value.
func ArgumentsError(reason error) *Error {
	err := NewError("invalid_arguments", nil)
	if reason != nil {
		err.Message = ArgumentsMessage(reason)
	}
	return err
}

// ArgumentsMessage turns an args.ErrInvalidArgs error ("invalid arguments:
// argument "x" must be integer") into a sentence ("Argument "x" must be
// integer.").
func ArgumentsMessage(reason error) string {
	text := reason.Error()
	if rest, ok := strings.CutPrefix(text, "invalid arguments: "); ok {
		text = rest
	}
	if text == "" || text == "invalid arguments" {
		return registry["invalid_arguments"].message
	}
	text = strings.ToUpper(text[:1]) + text[1:]
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	return text
}

// UsageError is invalid_arguments for a command line that does not parse.
// reason is a sentence built only from flag names, argument placeholders and
// subcommand names; argument is the flag or placeholder it names, if any.
// command is the command path for --help, such as "config input set".
func UsageError(command, reason, argument, suggestion string) *Error {
	var details *Details
	if argument != "" || suggestion != "" {
		details = &Details{Argument: argument, Suggestion: suggestion}
	}
	err := NewError("invalid_arguments", details)
	err.Message = reason
	err.NextAction = HelpAction(command)
	return err
}

// HelpAction points at the help of a command path.
func HelpAction(command string) string {
	if command == "" {
		return "Run mcparcel --help."
	}
	return "Run mcparcel " + command + " --help."
}
