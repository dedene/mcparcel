package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
	"github.com/dedene/mcparcel/internal/testutil"
)

// docs builds a State from JSON documents, as the files would hold them.
type docs struct {
	personal, local, selections string
}

func (d docs) state(t *testing.T) config.State {
	t.Helper()
	personal, err := config.DecodeCatalog([]byte(d.personal))
	if err != nil {
		t.Fatal(err)
	}
	if personal.Domains == nil {
		personal.Domains = map[string]config.Domain{}
	}
	if personal.CredentialProfiles == nil {
		personal.CredentialProfiles = map[string]config.ProfileRequirement{}
	}
	local := config.Local{SchemaVersion: 1}
	if d.local != "" {
		if local, err = config.DecodeLocal([]byte(d.local)); err != nil {
			t.Fatal(err)
		}
	}
	selections, err := config.DecodeSelections([]byte(d.selections))
	if err != nil {
		t.Fatal(err)
	}
	return config.State{Personal: personal, Local: local, Selections: selections, Catalogs: map[string]config.Catalog{}}
}

// tempDir is a short temp directory without symlinks in its path, so state
// roots below it fit the socket path limit.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(testutil.TempRoot(), "doctor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// mkdir creates dir and returns it.
func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// executable writes an executable file named name into dir.
func executable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// inputFor is a desktop Input over d with every document present and valid,
// a stopped runtime and paths under a fresh temp directory.
func inputFor(t *testing.T, d docs) Input {
	t.Helper()
	root := tempDir(t)
	state := d.state(t)
	snapshot, err := config.NewSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	return Input{
		Version:            "1.2.3",
		DesktopSupported:   true,
		DesktopSession:     true,
		DesktopOnePassword: true,
		Paths: config.Paths{
			Home: root + "/home", ConfigDir: root + "/config/mcparcel", DataDir: root + "/data/mcparcel",
			StateDir: root + "/state/mcparcel", RuntimeDir: root + "/run", LogFile: root + "/state/mcparcel/daemon.log",
		},
		Files: config.FileReport{
			Files: []config.FileStatus{{Name: "config.json", Present: true}, {Name: "personal.json", Present: true}, {Name: "selections.json", Present: true}},
			State: &state,
		},
		Snapshot:  &snapshot,
		Probe:     runtimeclient.Probe{State: runtimeclient.ProbeStopped},
		Retain:    true,
		LookupEnv: func(string) bool { return false },
		PATH:      root + "/bin",
		AppDirs:   []string{root + "/apps"},
	}
}

// headlessInput turns in into headless mode with a state root.
func headlessInput(in Input) Input {
	root := filepath.Join(filepath.Dir(in.Paths.Home), "stateroot")
	in.Runtime = config.RuntimeDefaults{Mode: config.ModeHeadless, StateRoot: root}
	paths, err := config.ApplyStateRoot(in.Paths, root)
	if err != nil {
		panic(err)
	}
	in.Paths = paths
	in.Snapshot.Local.Runtime = &in.Runtime
	return in
}

func find(t *testing.T, checks []output.DoctorCheck, id, subject string) output.DoctorCheck {
	t.Helper()
	for _, c := range checks {
		if c.ID == id && c.Subject == subject {
			return c
		}
	}
	t.Fatalf("no %s row for %q in %v", id, subject, ids(checks))
	return output.DoctorCheck{}
}

func absent(t *testing.T, checks []output.DoctorCheck, id string) {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			t.Fatalf("unexpected %s row: %+v", id, c)
		}
	}
}

func ids(checks []output.DoctorCheck) []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, strings.TrimSuffix(c.ID+" "+c.Subject, " "))
	}
	return out
}

func want(t *testing.T, c output.DoctorCheck, status, code string) {
	t.Helper()
	if c.Status != status || c.Code != code {
		t.Fatalf("%s %s: status %s code %q, want %s %q: %+v", c.ID, c.Subject, c.Status, c.Code, status, code, c)
	}
}

const stdioPaper = `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"stdio","command":"paper-mcp"}}}}`

const enabledPaper = `{"schemaVersion":1,"revision":1,"connections":{"local:paper":{"enabled":true}}}`
