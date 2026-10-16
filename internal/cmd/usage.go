package cmd

import (
	"context"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/alecthomas/kong"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// A usage error keeps kong's reason only as names kong took from the command
// tree or that pass these grammars: flags, argument placeholders, subcommand
// words. A positional value never appears, and neither does a mistyped flag
// that does not look like a flag name (it may be a pasted secret).
var (
	flagName        = regexp.MustCompile(`^-{1,2}[a-z][a-z0-9-]{0,31}$`)
	placeholderList = regexp.MustCompile(`^<[a-z][a-z-]*>(?: <[a-z][a-z-]*>)*$`)
	commandWord     = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)
	quotedToken     = regexp.MustCompile(`"([^"]*)"`)
)

// helpPath is a kong command path ("config input set <mcp> <name> <value>")
// without its placeholders, for mcparcel <path> --help.
func helpPath(command string) string {
	var words []string
	for _, w := range strings.Fields(command) {
		if strings.HasPrefix(w, "<") {
			continue
		}
		if !commandWord.MatchString(w) {
			return ""
		}
		words = append(words, w)
	}
	return strings.Join(words, " ")
}

// parseFailure is the product failure for a command line kong rejected.
func parseFailure(err error, i parseIntent) *output.Error {
	path := ""
	var parseErr *kong.ParseError
	if errors.As(err, &parseErr) && parseErr.Context != nil {
		path = helpPath(parseErr.Context.Command())
	}
	if path == "" && i.product {
		path = i.command
	}
	message := err.Error()
	if parseErr != nil && parseErr.Unwrap() != nil {
		message = parseErr.Unwrap().Error()
	}
	if path == "call" && message == `expected "<target>"` {
		return output.CallTargetError()
	}
	reason, argument, suggestion := usageReason(message, hiddenFlags(parseErr))
	// Outside the product surface (--json bogus) the stray word is the
	// command itself.
	if rest, ok := strings.CutPrefix(reason, "Unexpected argument."); ok && path == "" {
		reason = "Unknown command." + rest
	}
	return output.UsageError(path, reason, argument, suggestion)
}

// hiddenFlags names every spelling of the hidden flags kong knew at the
// failure (kong offers them as suggestions too), so no hint names one.
func hiddenFlags(parseErr *kong.ParseError) map[string]bool {
	hidden := map[string]bool{}
	if parseErr == nil || parseErr.Context == nil {
		return hidden
	}
	for _, f := range parseErr.Context.Flags() {
		if !f.Hidden {
			continue
		}
		hidden["--"+f.Name] = true
		if f.Short != 0 {
			hidden["-"+string(f.Short)] = true
		}
		for _, alias := range f.Aliases {
			hidden["--"+alias] = true
			hidden["-"+alias] = true
		}
	}
	return hidden
}

// missingCommand reports kong's error for a command line that names no
// command, which bare mcparcel answers with its short help.
func missingCommand(err error) bool {
	var parseErr *kong.ParseError
	if !errors.As(err, &parseErr) || parseErr.Context == nil || parseErr.Context.Selected() != nil || parseErr.Unwrap() == nil {
		return false
	}
	return strings.HasPrefix(parseErr.Unwrap().Error(), "expected one of ")
}

// usageReason rewrites a kong parse error as a sentence of safe names, plus
// the flag or placeholder it names and kong's single suggestion, if any.
// A suggestion in hidden is never offered.
func usageReason(message string, hidden map[string]bool) (reason, argument, suggestion string) {
	generic := "Invalid command arguments."
	head, hint, _ := strings.Cut(message, ", did you mean ")
	switch {
	case strings.HasPrefix(head, "unknown flag "):
		suggestion, also := suggestions(hint, flagName, hidden)
		flag := strings.TrimPrefix(head, "unknown flag ")
		if !flagName.MatchString(flag) {
			return "Unknown flag." + also, "", suggestion
		}
		return "Unknown flag " + flag + "." + also, flag, suggestion
	case strings.HasPrefix(head, "unexpected argument "):
		suggestion, also := suggestions(hint, commandWord, nil)
		return "Unexpected argument." + also, "", suggestion
	case strings.HasPrefix(head, "expected one of "), strings.HasPrefix(head, "expected "):
		var words []string
		for _, m := range quotedToken.FindAllStringSubmatch(head, -1) {
			words = append(words, m[1])
		}
		if len(words) == 1 {
			list := strings.TrimSuffix(words[0], " ...")
			if placeholderList.MatchString(list) {
				return missing("argument", strings.Fields(list)), strings.Fields(list)[0], ""
			}
		}
		if argument := branchArgument(words); argument != "" {
			return missing("argument", []string{argument}), argument, ""
		}
		if len(words) > 0 && !slices.ContainsFunc(words, func(w string) bool { return !commandWord.MatchString(w) }) {
			if len(words) == 1 {
				return "Missing subcommand " + words[0] + ".", "", ""
			}
			return "Missing subcommand: choose " + orList(words) + ".", "", ""
		}
	case strings.HasPrefix(head, "missing flags: "):
		var flags []string
		for _, f := range strings.Split(strings.TrimPrefix(head, "missing flags: "), ", ") {
			f, _, _ = strings.Cut(f, "=")
			if !flagName.MatchString(f) {
				return generic, "", ""
			}
			flags = append(flags, f)
		}
		return missing("flag", flags), flags[0], ""
	case strings.HasPrefix(head, "-"):
		// A flag whose value kong rejected: "--timeout: expected ...".
		flag, rest, ok := strings.Cut(head, ":")
		if !ok || !flagName.MatchString(flag) {
			return generic, "", ""
		}
		if strings.Contains(rest, `"EOL"`) || strings.Contains(rest, "missing value") {
			return "The " + flag + " flag needs a value.", flag, ""
		}
		return "Invalid value for the " + flag + " flag.", flag, ""
	}
	return generic, "", ""
}

// branchArgument is the placeholder of a branching argument (auth <mcp>,
// tools <mcp>) when kong lists it among subcommand words: kong names it by
// its summary, "tools <mcp> [flags]", and the argument is the usual intent.
func branchArgument(words []string) string {
	var branch []string
	for _, w := range words {
		if !commandWord.MatchString(w) {
			branch = append(branch, w)
		}
	}
	if len(words) < 2 || len(branch) != 1 {
		return ""
	}
	fields := strings.Fields(strings.TrimSuffix(branch[0], " [flags]"))
	for len(fields) > 1 && commandWord.MatchString(fields[0]) {
		fields = fields[1:]
	}
	if len(fields) != 1 || !placeholderList.MatchString(fields[0]) {
		return ""
	}
	return fields[0]
}

// suggestions reads kong's `"a"?` or `one of "a", "b"?` hint: a single safe
// candidate is the suggestion, and the sentence offers every safe one that
// is not hidden.
func suggestions(hint string, grammar *regexp.Regexp, hidden map[string]bool) (single, sentence string) {
	var names []string
	for _, m := range quotedToken.FindAllStringSubmatch(hint, -1) {
		if grammar.MatchString(m[1]) && !hidden[m[1]] {
			names = append(names, m[1])
		}
	}
	switch len(names) {
	case 0:
		return "", ""
	case 1:
		return names[0], " Did you mean " + names[0] + "?"
	}
	return "", " Did you mean " + orList(names) + "?"
}

func missing(kind string, names []string) string {
	if len(names) == 1 {
		return "Missing " + kind + " " + names[0] + "."
	}
	return "Missing " + kind + "s " + strings.Join(names, " ") + "."
}

func orList(words []string) string {
	if len(words) == 1 {
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

// flagFailure catches flags kong accepts but the command does not: a flag
// given twice, or one given an empty value.
func flagFailure(i parseIntent, path string) *output.Error {
	var once []string
	empty := false
	switch i.command {
	case "config", "import", "local":
		once, empty = []string{"--file", "--bindings"}, true
	case "add", "sync", "join":
		once, empty = []string{"--path", "--ref"}, true
	case "catalog":
		empty = true
	}
	for _, name := range append(once, "--domain") {
		if i.flags[name] > 1 {
			return output.UsageError(path, "Give the "+name+" flag only once.", name, "")
		}
	}
	if empty && i.emptyFlag != "" {
		return output.UsageError(path, "The "+i.emptyFlag+" flag needs a value.", i.emptyFlag, "")
	}
	return nil
}

// contextualFailure names what failed when the command line says it: the
// connection a command was given, or the empty install behind a
// config_required. Anything it cannot name safely stays as it was.
func contextualFailure(ctx context.Context, cli *CLI, command, path string, err error) error {
	if command != "call" && errors.Is(err, args.ErrInvalidArgs) {
		return output.CommandUsageError(path)
	}
	var withData *commandFailure
	if errors.As(err, &withData) {
		return err
	}
	safe := safeFailure(err)
	if safe.Details != nil || safe.Message != output.NewError(safe.Code, nil).Message {
		return err
	}
	switch safe.Code {
	case "connection_unavailable":
		if name := connectionArgument(cli, path); name != "" {
			return unavailableFailure(ctx, path, name, err)
		}
	case "connection_disabled":
		if name := connectionArgument(cli, path); name != "" {
			return output.ConnectionDisabledError(name)
		}
	case "config_required":
		if noConnections(ctx) {
			return output.NoConnectionsError()
		}
		if name := connectionArgument(cli, path); name != "" {
			return output.ConfigRequiredError(name)
		}
	}
	return err
}

// unavailableFailure says why the connection the command line named is
// unavailable. local update and local remove take only personal IDs, so
// there no personal connection has the name. Elsewhere either no connection
// has it, or the install still lists one whose definition is gone (its
// catalog or personal definition was removed, the selection kept). A name
// that resolves to an available connection, or to several, keeps err.
func unavailableFailure(ctx context.Context, path, name string, err error) error {
	if path == "local update" || path == "local remove" {
		return output.PersonalConnectionUnavailableError(name, suggestPersonal(ctx, name))
	}
	effective, ok := readEffective(ctx)
	if !ok {
		return output.ConnectionUnavailableError(name, "")
	}
	id, resolveErr := config.ResolveID(name, effective.Aliases, slices.Sorted(maps.Keys(effective.Connections)))
	switch {
	case errors.Is(resolveErr, config.ErrNotFound):
		return output.ConnectionUnavailableError(name, suggestConnection(effective, name))
	case resolveErr == nil && !effective.Connections[id].Available:
		return output.ConnectionGoneError(name)
	}
	return err
}

// connectionArgument is the one connection name the command line gave, or ""
// when it gave none or several.
func connectionArgument(cli *CLI, path string) string {
	switch path {
	case "tools":
		return cli.Tools.MCP.MCP
	case "tools list":
		return cli.Tools.List.MCP
	case "tools enable":
		return cli.Tools.Enable.MCP
	case "tools disable":
		return cli.Tools.Disable.MCP
	case "call":
		connection, _, err := splitCallTarget(cli.Call.Target)
		if err != nil {
			return ""
		}
		return connection
	case "inspect":
		return cli.Inspect.MCP
	case "auth":
		return cli.Auth.MCP.MCP
	case "auth status":
		return cli.Auth.Status.MCP
	case "auth logout":
		return cli.Auth.Logout.MCP
	case "doctor":
		return cli.Doctor.MCP
	case "config input set":
		return cli.Config.Input.Set.MCP
	case "config profile bind":
		return cli.Config.Profile.Bind.MCP
	case "local update":
		return cli.Local.Update.ID
	case "local remove":
		return cli.Local.Remove.ID
	case "enable":
		if len(cli.Enable.MCPs) == 1 {
			return cli.Enable.MCPs[0]
		}
	case "disable":
		if len(cli.Disable.MCPs) == 1 {
			return cli.Disable.MCPs[0]
		}
	}
	return ""
}

// readHintState reads the configuration offline for a diagnostic; any
// failure, or a writer holding the lock past a short wait, just means no hint.
func readHintState(ctx context.Context) (config.State, bool) {
	paths, err := commandPaths()
	if err != nil {
		return config.State{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	state, err := config.ReadState(ctx, paths)
	return state, err == nil
}

// readEffective is readHintState resolved.
func readEffective(ctx context.Context) (config.EffectiveConfig, bool) {
	state, ok := readHintState(ctx)
	if !ok {
		return config.EffectiveConfig{}, false
	}
	effective, err := config.Resolve(state)
	return effective, err == nil
}

// suggestConnection is the known connection name closest to name: aliases
// and unique short names for a short name, canonical IDs for a canonical one.
func suggestConnection(effective config.EffectiveConfig, name string) string {
	var ids []string
	for id, row := range effective.Connections {
		if row.Available {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var candidates []string
	if strings.Contains(name, ":") {
		candidates = ids
	} else {
		count := map[string]int{}
		for _, id := range ids {
			count[config.ConnectionBase(id)]++
		}
		for _, id := range ids {
			if base := config.ConnectionBase(id); count[base] == 1 {
				candidates = append(candidates, base)
			}
		}
		for alias, id := range effective.Aliases {
			if slices.Contains(ids, id) {
				candidates = append(candidates, alias)
			}
		}
		slices.Sort(candidates)
		candidates = slices.Compact(candidates)
	}
	return output.Closest(name, candidates)
}

// suggestPersonal is the personal connection ID closest to name, the IDs
// local update and local remove take: bare, or as local:<id> when name is.
func suggestPersonal(ctx context.Context, name string) string {
	state, ok := readHintState(ctx)
	if !ok {
		return ""
	}
	prefix := ""
	if strings.HasPrefix(name, "local:") {
		prefix = "local:"
	}
	var candidates []string
	for id := range state.Personal.Connections {
		candidates = append(candidates, prefix+id)
	}
	slices.Sort(candidates)
	return output.Closest(name, candidates)
}

// noConnections reports an install with no connections at all.
func noConnections(ctx context.Context) bool {
	effective, ok := readEffective(ctx)
	return ok && len(effective.Connections) == 0
}
