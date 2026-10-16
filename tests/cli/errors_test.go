package cli_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// namedError is the envelope error with the fields that name what failed.
type namedError struct {
	Code, Message, NextAction string
	Details                   struct{ Connection, Tool, Argument, Suggestion string }
}

func errorOf(t *testing.T, v result) namedError {
	t.Helper()
	var envelope struct{ Error namedError }
	if err := json.Unmarshal([]byte(v.stdout), &envelope); err != nil {
		t.Fatal(err, v.stdout)
	}
	return envelope.Error
}

// tools and call name the connection or tool that does not exist, with the
// nearest name: through the runtime for tools and the tool list, offline for
// call's connection check.
func TestUnknownNamesSuggestClosest(t *testing.T) {
	r := newRig(t)
	for _, argv := range [][]string{{"tools", "fixtur"}, {"tools", "list", "fixtur"}, {"tools", "enable", "fixtur", "echo"}, {"tools", "disable", "fixtur", "echo"}} {
		e := errorOf(t, r.check(r.run(append(argv, "--json")...), 4, "connection_unavailable"))
		if e.Message != "No connection is named fixtur. Did you mean fixture?" || e.Details.Connection != "fixtur" || e.Details.Suggestion != "fixture" ||
			e.NextAction != "Run mcparcel list to see enabled connections, or mcparcel catalog for all of them." {
			t.Fatalf("%v: %+v", argv, e)
		}
	}
	e := errorOf(t, r.check(r.run("call", "fixtur.echo", "--json"), 4, "connection_unavailable"))
	if e.Message != "No connection is named fixtur. Did you mean fixture?" {
		t.Fatalf("%+v", e)
	}
	e = errorOf(t, r.check(r.run("call", "fixture.ech", "--json", "text=x"), 2, "tool_not_found"))
	if e.Message != "Connection fixture has no tool named ech. Did you mean echo?" || e.NextAction != "Run mcparcel tools fixture." ||
		e.Details.Connection != "fixture" || e.Details.Tool != "ech" || e.Details.Suggestion != "echo" {
		t.Fatalf("%+v", e)
	}
	v := r.run("call", "fixture.ech", "text=x")
	if v.code != 2 || v.stdout != "" || v.stderr != "Connection fixture has no tool named ech. Did you mean echo?\nRun mcparcel tools fixture.\n" {
		t.Fatalf("%+v", v)
	}
	if b, _ := os.ReadFile(r.root + "/started"); len(b) > 0 {
		t.Fatalf("tool effect %q", b)
	}
}

// A schema mismatch is one sentence naming the argument and its type, and
// points at the tool list of the connection that was called.
func TestArgumentErrorIsASentence(t *testing.T) {
	r := newRig(t)
	e := errorOf(t, r.check(r.run("call", "fixture.typed", "--json", "limit=bad", "enabled=true", "queries=[]"), 2, "invalid_arguments"))
	if e.Message != `Argument "limit" must be integer.` || e.NextAction != "Run mcparcel tools fixture to see the tool's arguments." {
		t.Fatalf("%+v", e)
	}
}

// On an install with no connections, tools says so plainly and points at
// setup rather than at configuration files.
func TestFreshInstallPointsAtSetup(t *testing.T) {
	r := newRig(t)
	r.personal.Connections = map[string]config.Connection{}
	r.save()
	for _, argv := range [][]string{{"tools", "nope"}, {"call", "nope.x"}} {
		e := errorOf(t, r.check(r.run(append(argv, "--json")...), 2, "config_required"))
		if e.Message != "No connections are set up yet." || e.NextAction != "Run mcparcel setup, or mcparcel add <owner/repo>." {
			t.Fatalf("%v: %+v", argv, e)
		}
	}
	v := r.run("tools", "nope")
	if v.code != 2 || !strings.Contains(v.stderr, "No connections are set up yet.\nRun mcparcel setup, or mcparcel add <owner/repo>.\n") {
		t.Fatalf("%+v", v)
	}
}

// A connection the install still lists but cannot use (its definition was
// removed, the selection kept) is not reported as missing. local update and
// local remove take only personal IDs and suggest only those.
func TestUnavailableAndPersonalNamesBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	seedMetadata(t, r, false)
	for _, c := range []struct {
		name string
		argv []string
	}{
		{"removed", []string{"enable", "removed"}},
		{"local:removed", []string{"enable", "local:removed"}},
		{"removed", []string{"call", "removed.x"}},
		{"removed", []string{"config", "input", "set", "removed", "x", "y"}},
	} {
		e := errorOf(t, r.run(4, "connection_unavailable", append(c.argv, "--json")...))
		if e.Message != "Connection "+c.name+" is unavailable: its definition is gone." || e.Details.Connection != c.name || e.Details.Suggestion != "" ||
			e.NextAction != "Run mcparcel inspect "+c.name+" to see it, or mcparcel catalog for connections you can use." {
			t.Fatalf("%v: %+v", c.argv, e)
		}
	}
	// personal exists only in the catalog: local remove cannot take it, nor
	// suggest it.
	for _, name := range []string{"personal", "persona"} {
		e := errorOf(t, r.run(4, "connection_unavailable", "local", "remove", name, "--json"))
		if e.Message != "No personal connection is named "+name+"." || e.Details.Connection != name || e.Details.Suggestion != "" {
			t.Fatalf("%s: %+v", name, e)
		}
	}
}

// --cached is accepted before or after <mcp>, and by tools list; enable and
// disable refuse it as an unknown flag.
func TestToolsCachedEitherSideOfTheConnection(t *testing.T) {
	r := newMetadataRig(t)
	seedMetadata(t, r, true)
	for _, argv := range [][]string{{"tools", "--cached", "personal"}, {"tools", "personal", "--cached"}, {"tools", "list", "--cached", "personal"}, {"tools", "--cached", "list", "personal"}} {
		r.run(6, "schema_cache_miss", append(argv, "--json")...)
	}
	for argv, help := range map[string]string{"tools enable --cached personal t": "tools enable", "tools --cached disable personal t": "tools disable"} {
		e := errorOf(t, r.run(2, "invalid_arguments", append(strings.Fields(argv), "--json")...))
		if e.Message != "Unknown flag --cached." || e.Details.Argument != "--cached" || e.NextAction != "Run mcparcel "+help+" --help." {
			t.Fatalf("%s: %+v", argv, e)
		}
	}
}
