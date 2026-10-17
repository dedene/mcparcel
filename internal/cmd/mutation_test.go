package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// humanRun runs argv without --json and fails unless it exits 0 with stdout
// want and nothing on stderr.
func humanRun(t *testing.T, want string, argv ...string) {
	t.Helper()
	code, out, errOut := run(t, argv...)
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("%v: %d %q %q, want %q", argv, code, out, errOut, want)
	}
}

// TestMutationsSayWhatHappened: selection commands print what changed in
// words, by label (the short ID when there is none), and tell a repeat from
// a change; JSON keeps the revision.
func TestMutationsSayWhatHappened(t *testing.T) {
	_, store, state := metadataSeed(t)
	_, err := store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		c := s.Personal.Connections["paper"]
		c.Label = "Paper Docs"
		s.Personal.Connections["paper"] = c
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	humanRun(t, "Paper Docs is off.\n", "disable", "paper")
	humanRun(t, "Paper Docs was already off.\n", "disable", "local:paper")
	humanRun(t, "Paper Docs is on.\ndisabled is on.\n", "enable", "paper", "disabled", "paper")
	humanRun(t, "Paper Docs was already on.\nreview is on.\n", "enable", "paper", "review")
	code, out, _ := run(t, "disable", "paper", "--json")
	var v struct{ Data ConfigMutationData }
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || v.Data.Revision != selectionRead(t, store).Selections.Revision {
		t.Fatal(code, out)
	}
}

func TestToolAndConfigMutationMessages(t *testing.T) {
	p, store, state := metadataSeed(t)
	_, err := store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		c := s.Personal.Connections["paper"]
		c.ToolPolicy = &config.ToolPolicy{Deny: []string{"delete"}}
		s.Personal.Connections["paper"] = c
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	humanRun(t, "Turned off read and write for paper.\n", "tools", "disable", "paper", "read", "write", "read")
	humanRun(t, "Turned off counter for paper.\nread was already off for paper.\n", "tools", "disable", "paper", "read", "counter")
	humanRun(t, "Turned on read for paper.\ndelete stays off for paper: its tool policy does not allow it.\n", "tools", "enable", "paper", "read", "delete")
	humanRun(t, "read was already on for paper.\n", "tools", "enable", "paper", "read")
	// A name outside the tool grammar could be a pasted secret: count it.
	humanRun(t, "Turned off 1 tool for paper.\n", "tools", "disable", "paper", "sk-secret value")
	humanRun(t, "Set input required for missing.\n", "config", "input", "set", "missing", "required", "secret-input-value")
	profile := filepath.Join(p.Home, "profile.json")
	if err := os.WriteFile(profile, []byte(`{"mode":"desktop","account":"Secret Account"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	humanRun(t, "Saved credential profile team.\n", "config", "profile", "set", "team", "--file", profile)
	humanRun(t, "Linked credential profile team to missing.\n", "config", "profile", "bind", "missing", "team")
}

func TestLocalMutationMessages(t *testing.T) {
	p := metadataEnv(t)
	file := localFile(t, p, `{"id":"echo","transport":{"type":"stdio","command":"nonexistent-fixture","args":["old"]}}`)
	humanRun(t, "Added personal connection echo. It is off: run mcparcel enable local:echo to turn it on.\n", "local", "add", "--file", file)
	humanRun(t, "Personal connection echo already exists with this definition. Nothing changed.\n", "local", "add", "--file", file)
	humanRun(t, "Updated personal connection echo.\n", "local", "update", "echo", "--file", file)
	humanRun(t, "echo is on.\n", "enable", "echo")
	localFile(t, p, `{"id":"echo","label":"Echo","transport":{"type":"stdio","command":"other-fixture"}}`)
	humanRun(t, "Updated personal connection Echo.\nIts command or sign-in changed, so it needs review: run mcparcel enable local:echo to accept it.\n", "local", "update", "echo", "--file", file)
	humanRun(t, "Removed personal connection Echo. It is off now.\n", "local", "remove", "echo")
	humanRun(t, "Added personal connection Echo. It is off: run mcparcel enable local:echo to turn it on.\n", "local", "add", "--file", file)
	humanRun(t, "Removed personal connection Echo.\n", "local", "remove", "echo")
}

// TestDryRunSavesNothing: --dry-run runs the command's checks, prints the
// plan and leaves every file as it was.
func TestDryRunSavesNothing(t *testing.T) {
	p, store, state := metadataSeed(t)
	before := selectionRead(t, store)
	files := func() map[string]string {
		out := map[string]string{}
		for _, path := range []string{p.SelectionsFile, p.PersonalFile, p.ConfigFile} {
			raw, _ := os.ReadFile(path)
			out[path] = string(raw)
		}
		return out
	}
	saved := files()
	humanRun(t, "Would turn off paper.\ndisabled is already off.\nNothing was saved.\n", "disable", "paper", "disabled", "--dry-run")
	humanRun(t, "Would remove personal connection paper and turn it off.\nNothing was saved.\n", "local", "remove", "paper", "--dry-run")
	humanRun(t, "Would remove personal connection disabled.\nNothing was saved.\n", "local", "remove", "disabled", "--dry-run")
	code, out, _ := run(t, "disable", "paper", "disabled", "--dry-run", "--json")
	var v struct{ Data output.PlanData }
	want := output.PlanData{DryRun: true, Revision: state.Selections.Revision, Changes: []output.PlanChange{{Connection: "local:paper", Change: "turn_off", Enabled: true}, {Connection: "local:disabled", Change: "none"}}}
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || !reflect.DeepEqual(v.Data, want) {
		t.Fatal(code, out)
	}
	code, out, _ = run(t, "local", "remove", "paper", "--dry-run", "--json")
	want = output.PlanData{DryRun: true, Revision: state.Selections.Revision, Changes: []output.PlanChange{{Connection: "local:paper", Change: "remove", Enabled: true}}}
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || !reflect.DeepEqual(v.Data, want) {
		t.Fatal(code, out)
	}
	selectionRun(t, 4, "connection_unavailable", "disable", "absent", "--dry-run")
	selectionRun(t, 4, "connection_unavailable", "local", "remove", "absent", "--dry-run")
	if !reflect.DeepEqual(before, selectionRead(t, store)) || !reflect.DeepEqual(saved, files()) {
		t.Fatal("dry run saved a change")
	}
	if strings.Contains(out, "nonexistent-fixture") {
		t.Fatal("plan carries a command", out)
	}
}
