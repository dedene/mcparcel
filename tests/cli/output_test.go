package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/output"
)

// humanExecute runs argv without --json and fails unless it exits 0 with
// stdout want and nothing on stderr.
func humanExecute(t *testing.T, r *metadataRig, want string, argv ...string) {
	t.Helper()
	if v := catalogExecute(t, r, argv...); v.code != 0 || v.stderr != "" || v.stdout != want {
		t.Fatalf("%v: %d %q %q, want %q", argv, v.code, v.stdout, v.stderr, want)
	}
}

// pipedEmpty runs list or catalog in a pipe with no rows to print: stdout
// stays empty, so `wc -l` gives 0, and the sentence goes to stderr.
func pipedEmpty(t *testing.T, r *metadataRig, want string, argv ...string) {
	t.Helper()
	if v := catalogExecute(t, r, argv...); v.code != 0 || v.stdout != "" || v.stderr != want {
		t.Fatalf("%v: %d %q %q, want stderr %q", argv, v.code, v.stdout, v.stderr, want)
	}
}

// TestRemoveDryRunBlackBox: remove --dry-run prints what removing the
// catalog would do and writes nothing; remove then says the same in the past
// tense.
func TestRemoveDryRunBlackBox(t *testing.T) {
	r, _, _, _ := catalogFixture(t)
	r.run(0, "", "add", "fixture-owner/fixture.repo", "--json")
	r.run(0, "", "enable", "paper", "--json")
	before := catalogFiles(t, r)
	humanExecute(t, r, "Would remove fixture-owner/fixture.repo. paper is on and would become unavailable.\nNothing was saved.\n", "remove", "fixture-owner/fixture.repo", "--dry-run")
	data := catalogEnvelope(t, catalogExecute(t, r, "remove", "fixture-owner/fixture.repo", "--dry-run", "--json"), 0, "", "dryRun", "revision", "source", "changes")
	var changes []output.PlanChange
	_ = json.Unmarshal(data["changes"], &changes)
	want := []output.PlanChange{{Connection: "github:fixture-owner/fixture.repo#paper", Change: "unavailable", Enabled: true}, {Connection: "github:fixture-owner/fixture.repo#retired", Change: "remove"}}
	if string(data["dryRun"]) != "true" || !reflect.DeepEqual(changes, want) {
		t.Fatal(data)
	}
	catalogEnvelope(t, catalogExecute(t, r, "remove", "other-owner/other.repo", "--dry-run", "--json"), 4, "catalog_unavailable")
	if after := catalogFiles(t, r); !reflect.DeepEqual(before, after) {
		t.Fatal("dry run wrote files")
	}
	humanExecute(t, r, "Removed fixture-owner/fixture.repo.\npaper was on and is unavailable now.\n", "remove", "fixture-owner/fixture.repo")
	humanExecute(t, r, "github:fixture-owner/fixture.repo#paper\t\tUnavailable\n", "list")
}

// TestListEmptyStatesAndInspectBlackBox: list and catalog in a pipe print
// tab-separated rows with no header or revision, empty states say what to
// run, and inspect prints labelled lines without a JSON dump.
func TestListEmptyStatesAndInspectBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	pipedEmpty(t, r, "No connections yet. Run mcparcel setup, or mcparcel add <owner/repo>.\n", "list")
	pipedEmpty(t, r, "No connections yet. Run mcparcel setup, or mcparcel add <owner/repo>.\n", "catalog")
	file := r.file("echo.json", []byte(`{"id":"echo","transport":{"type":"stdio","command":"never-execute","args":["ARG-CANARY"],"env":{"TOKEN":"ENV-CANARY"}}}`))
	humanExecute(t, r, "Added personal connection echo. It is off: run mcparcel enable local:echo to turn it on.\n", "local", "add", "--file", file)
	pipedEmpty(t, r, "Nothing is on. 1 connection is available. Run mcparcel catalog to see it.\n", "list")
	humanExecute(t, r, "local:echo\techo\tDisabled\n", "catalog")
	humanExecute(t, r, "echo is on.\n", "enable", "echo")
	humanExecute(t, r, "local:echo\techo\tReady\n", "list")
	// On a terminal (the fixture's marker stands in for one): a header row
	// and aligned columns.
	marker := filepath.Join(r.paths.StateDir, "fixture-stdout-terminal")
	if err := os.MkdirAll(r.paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	humanExecute(t, r, "ID          Label  State\nlocal:echo  echo   Ready\n", "list")
	humanExecute(t, r, "Would turn off echo.\nNothing was saved.\n", "disable", "echo", "--dry-run")
	humanExecute(t, r, "echo is off.\n", "disable", "echo")
	// There the empty state is the output itself.
	humanExecute(t, r, "Nothing is on. 1 connection is available. Run mcparcel catalog to see it.\n", "list")
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	humanExecute(t, r, "echo was already off.\n", "disable", "echo")
	v := catalogExecute(t, r, "inspect", "echo")
	want := "ID: local:echo\nLabel: echo\nSource: Personal\nState: Disabled\nEnabled: no\nBlockers: connection_disabled\nDomains: other\nTransport: stdio, never-execute, 1 argument\nEnvironment:\n  TOKEN: set (value hidden)\nCall timeout: 120s\n"
	if v.code != 0 || v.stderr != "" || v.stdout != want || strings.Contains(v.stdout, "CANARY") {
		t.Fatalf("%d %q %q", v.code, v.stdout, v.stderr)
	}
	humanExecute(t, r, "Would remove personal connection echo.\nNothing was saved.\n", "local", "remove", "echo", "--dry-run")
	humanExecute(t, r, "Removed personal connection echo.\n", "local", "remove", "echo")
}
