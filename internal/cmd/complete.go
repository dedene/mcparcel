package cmd

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/alecthomas/kong"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// completeCommand is the hidden command the completion scripts call:
// mcparcel __complete <words...>, the words after mcparcel with the word
// being completed last (empty at a fresh word).
const completeCommand = "__complete"

// completionWait bounds the configuration read: a writer holding the lock
// means no connection candidates, never a stalled prompt.
const completionWait = 300 * time.Millisecond

// writeCandidates prints the candidates for words, one per line. It reads
// the configuration offline and never writes, starts the runtime, reads
// 1Password or touches the network; a failed read only means fewer
// candidates.
func writeCandidates(ctx context.Context, w io.Writer, app *kong.Application, words []string) int {
	load := func() (config.State, bool) {
		paths, err := commandPaths()
		if err != nil {
			return config.State{}, false
		}
		ctx, cancel := context.WithTimeout(ctx, completionWait)
		defer cancel()
		state, err := config.ReadState(ctx, paths)
		return state, err == nil
	}
	for _, candidate := range completeWords(app.Node, words, load) {
		if _, err := fmt.Fprintln(w, candidate); err != nil {
			return ExitInternal
		}
	}
	return ExitOK
}

// completer answers one completion request; it loads the configuration at
// most once, and only for a word that takes a connection, tool, catalog or
// profile.
type completer struct {
	load      func() (config.State, bool)
	loaded    bool
	ok        bool
	state     config.State
	effective config.EffectiveConfig
}

func (c *completer) config() (config.State, config.EffectiveConfig, bool) {
	if !c.loaded {
		c.loaded = true
		if state, ok := c.load(); ok {
			if effective, err := config.Resolve(state); err == nil {
				c.state, c.effective, c.ok = state, effective, true
			}
		}
	}
	return c.state, c.effective, c.ok
}

// completeWords walks the command tree along words and lists what may come
// at the last one: subcommands, flags or a positional's values, filtered
// by the typed prefix and sorted.
func completeWords(root *kong.Node, words []string, load func() (config.State, bool)) []string {
	if len(words) == 0 {
		words = []string{""}
	}
	current := words[len(words)-1]
	node, positionals, dashes, valueFlag := walkWords(root, words[:len(words)-1])
	c := &completer{load: load}
	var candidates []string
	switch {
	case valueFlag:
		// A flag value: paths and JSON are the shell's to complete.
		return nil
	case !dashes && strings.HasPrefix(current, "-"):
		if strings.Contains(current, "=") {
			return nil
		}
		candidates = flagNames(node)
	case commandPath(node) == "help":
		// help takes a command path: complete it as a command line.
		target, _, _, _ := walkWords(root, positionals)
		if target.Type != kong.ArgumentNode {
			candidates = commandNames(target)
		}
	default:
		if len(positionals) == 0 {
			candidates = commandNames(node)
		}
		if value := nextPositional(node, positionals); value != nil {
			candidates = append(candidates, c.values(commandPath(node), value.Name, positionals, current)...)
		}
	}
	var out []string
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, current) {
			out = append(out, candidate)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// walkWords follows the typed words to the node they select. positionals
// are the words given to that node's arguments; dashes is a -- seen;
// valueFlag is a last word that is a flag still waiting for its value.
func walkWords(root *kong.Node, words []string) (node *kong.Node, positionals []string, dashes, valueFlag bool) {
	node = root
	for i := 0; i < len(words); i++ {
		word := words[i]
		switch {
		case dashes:
			positionals = append(positionals, word)
		case word == "--":
			dashes = true
		case strings.HasPrefix(word, "-") && len(word) > 1:
			if flag := findFlag(node, word); flag != nil && !flag.IsBool() && !flag.IsCounter() && !strings.Contains(word, "=") {
				if i == len(words)-1 {
					valueFlag = true
				}
				i++
			}
		case len(positionals) == 0 && childCommand(node, word) != nil:
			node = childCommand(node, word)
		case len(positionals) == 0 && argumentChild(node) != nil:
			// A branching argument (tools <mcp>, auth <mcp>) takes the word.
			node = argumentChild(node)
		default:
			positionals = append(positionals, word)
		}
	}
	return node, positionals, dashes, valueFlag
}

// findFlag is the flag a word names (--name, --name=value or -x) on node or
// an ancestor.
func findFlag(node *kong.Node, word string) *kong.Flag {
	name, _, _ := strings.Cut(word, "=")
	for n := node; n != nil; n = n.Parent {
		for _, flag := range n.Flags {
			if name == "--"+flag.Name || flag.Short != 0 && name == "-"+string(flag.Short) || slices.Contains(flag.Aliases, strings.TrimPrefix(name, "--")) {
				return flag
			}
		}
	}
	return nil
}

func flagNames(node *kong.Node) []string {
	var names []string
	for n := node; n != nil; n = n.Parent {
		for _, flag := range n.Flags {
			if flag.Hidden {
				continue
			}
			names = append(names, "--"+flag.Name)
			if flag.Short != 0 {
				names = append(names, "-"+string(flag.Short))
			}
		}
	}
	return names
}

func childCommand(node *kong.Node, word string) *kong.Node {
	for _, child := range node.Children {
		if child.Type == kong.CommandNode && (child.Name == word || slices.Contains(child.Aliases, word)) {
			return child
		}
	}
	return nil
}

func argumentChild(node *kong.Node) *kong.Node {
	for _, child := range node.Children {
		if child.Type == kong.ArgumentNode {
			return child
		}
	}
	return nil
}

func commandNames(node *kong.Node) []string {
	var names []string
	for _, child := range node.Children {
		if child.Type == kong.CommandNode && !child.Hidden {
			names = append(names, child.Name)
		}
	}
	return names
}

// commandPath is a node's command words ("tools enable"); a branching
// argument counts as its parent command.
func commandPath(node *kong.Node) string {
	var words []string
	for n := node; n != nil && n.Type != kong.ApplicationNode; n = n.Parent {
		if n.Type == kong.CommandNode {
			words = append([]string{n.Name}, words...)
		}
	}
	return strings.Join(words, " ")
}

// nextPositional is the argument the next word fills: the branching
// argument before any word, else the next positional, else a trailing
// repeated one (<mcp> ...).
func nextPositional(node *kong.Node, given []string) *kong.Value {
	if arg := argumentChild(node); arg != nil && len(given) == 0 {
		return arg.Argument
	}
	if len(given) < len(node.Positional) {
		return node.Positional[len(given)]
	}
	if n := len(node.Positional); n > 0 && node.Positional[n-1].IsSlice() {
		return node.Positional[n-1]
	}
	return nil
}

// values lists what an argument takes, by command and argument name.
func (c *completer) values(path, argument string, given []string, current string) []string {
	switch {
	case path == "completion" && argument == "shell":
		return []string{"bash", "fish", "zsh"}
	case argument == "mcp":
		var want func(config.EffectiveConnection) bool
		switch path {
		case "enable":
			want = func(row config.EffectiveConnection) bool { return row.Available && !row.Enabled }
		case "disable", "tools", "tools list", "tools enable", "tools disable":
			want = func(row config.EffectiveConnection) bool { return row.Enabled }
		}
		return c.connections(want)
	case path == "call" && argument == "target":
		return c.callTargets(current)
	case argument == "tool" && (path == "tools enable" || path == "tools disable") && len(given) > 0:
		return c.tools(given[0], path == "tools enable")
	case argument == "repository" && (path == "remove" || path == "sync"):
		state, _, ok := c.config()
		if !ok {
			return nil
		}
		var repos []string
		for _, source := range state.Local.Sources {
			repos = append(repos, source.Owner+"/"+source.Repo)
		}
		return repos
	case argument == "id" && (path == "local update" || path == "local remove"):
		state, _, ok := c.config()
		if !ok {
			return nil
		}
		var ids []string
		for id := range state.Personal.Connections {
			if output.SafeConnectionName(id) {
				ids = append(ids, id)
			}
		}
		return ids
	case argument == "profile" && path == "config profile bind":
		state, _, ok := c.config()
		if !ok {
			return nil
		}
		var names []string
		for name := range state.Local.CredentialProfiles {
			// Profile IDs share the short connection ID grammar.
			if output.SafeConnectionName(name) {
				names = append(names, name)
			}
		}
		return names
	}
	return nil
}

// connections names the connections want accepts (every one when want is
// nil) as a user types them: the short ID when no other connection shares
// it, else the canonical ID, plus the aliases that point at them.
func (c *completer) connections(want func(config.EffectiveConnection) bool) []string {
	_, effective, ok := c.config()
	if !ok {
		return nil
	}
	count := map[string]int{}
	for id := range effective.Connections {
		count[config.ConnectionBase(id)]++
	}
	var names []string
	picked := map[string]bool{}
	for id, row := range effective.Connections {
		if want != nil && !want(row) {
			continue
		}
		picked[id] = true
		if base := config.ConnectionBase(id); count[base] == 1 {
			names = append(names, base)
		} else {
			names = append(names, id)
		}
	}
	for alias, id := range effective.Aliases {
		if picked[id] {
			names = append(names, alias)
		}
	}
	return slices.DeleteFunc(names, func(name string) bool { return !output.SafeConnectionName(name) })
}

// callTargets completes call's <mcp>.<tool>: enabled connections as
// "<mcp>." until a dot is typed, then that connection's tools as far as the
// configuration names them (a finite tool policy); else just "<mcp>.".
func (c *completer) callTargets(current string) []string {
	// A canonical GitHub ID's repository may hold dots: the tool's dot
	// comes after its #.
	start := 0
	if strings.HasPrefix(current, "github:") {
		start = strings.IndexByte(current, '#') + 1
	}
	dot := -1
	if start > 0 || !strings.HasPrefix(current, "github:") {
		dot = strings.IndexByte(current[start:], '.')
	}
	if dot < 0 {
		var targets []string
		for _, name := range c.connections(func(row config.EffectiveConnection) bool { return row.Enabled }) {
			targets = append(targets, name+".")
		}
		return targets
	}
	connection := current[:start+dot]
	targets := []string{connection + "."}
	for _, tool := range c.tools(connection, false) {
		targets = append(targets, connection+"."+tool)
	}
	return targets
}

// tools lists a connection's tool names from its configuration, never from
// the server: the tools it turned off (off), or the tools its finite tool
// policy allows and it did not turn off.
func (c *completer) tools(name string, off bool) []string {
	state, effective, ok := c.config()
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(effective.Connections))
	for id := range effective.Connections {
		ids = append(ids, id)
	}
	id, err := config.ResolveID(name, effective.Aliases, ids)
	if err != nil {
		return nil
	}
	var tools []string
	if off {
		tools = slices.Clone(state.Selections.Connections[id].DisabledTools)
	} else if policy := effective.Connections[id].Policy; policy.Allow != nil {
		for _, tool := range *policy.Allow {
			if config.ToolAllowed(policy, tool) {
				tools = append(tools, tool)
			}
		}
	}
	return slices.DeleteFunc(tools, func(tool string) bool { return !output.SafeToolName(tool) })
}
