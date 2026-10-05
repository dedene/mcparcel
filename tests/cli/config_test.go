package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

type metadataRig struct {
	t     *testing.T
	paths config.Paths
	env   []string
}

func newMetadataRig(t *testing.T) *metadataRig {
	t.Helper()
	p, e := testutil.IsolatedPaths(t)
	return &metadataRig{t, p, e}
}

func (r *metadataRig) file(name string, body []byte) string {
	r.t.Helper()
	p := filepath.Join(r.paths.Home, name)
	if e := os.WriteFile(p, body, 0o600); e != nil {
		r.t.Fatal(e)
	}
	return p
}

func (r *metadataRig) run(exit int, code string, argv ...string) result {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, binaryA, argv...)
	c.Env = r.env
	var out, errOut bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errOut
	e := c.Run()
	got := 0
	if e != nil {
		if x, ok := e.(*exec.ExitError); ok {
			got = x.ExitCode()
		} else {
			r.t.Fatal(e)
		}
	}
	v := result{stdout: out.String(), stderr: errOut.String()}
	if e = json.Unmarshal(out.Bytes(), &v.envelope); e != nil {
		r.t.Fatal(e, v.stdout, v.stderr)
	}
	if got != exit || v.envelope.Error.Code != code || v.stderr != "" {
		r.t.Fatalf("exit=%d stdout=%s stderr=%s", got, v.stdout, v.stderr)
	}
	return v
}

func (r *metadataRig) offline() {
	r.t.Helper()
	for _, p := range []string{r.paths.SocketFile, r.paths.LogFile, r.paths.StateDir + "/fixture-auth-events"} {
		if _, e := os.Stat(p); !os.IsNotExist(e) {
			r.t.Fatalf("metadata effect: %s %v", p, e)
		}
	}
}

func (r *metadataRig) fixture() string {
	r.t.Helper()
	b, e := os.ReadFile("../../testdata/mcporter/all-32.json")
	if e != nil {
		r.t.Fatal(e)
	}
	return r.file("source.json", b)
}

func reportData(t *testing.T, v result) config.ImportReport {
	t.Helper()
	var r config.ImportReport
	if e := json.Unmarshal(v.envelope.Data, &r); e != nil {
		t.Fatal(e)
	}
	return r
}

func TestConfigValidateBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	for i, b := range []string{`{"schemaVersion":1,"connections":{}}`, `{"schemaVersion":1,"connections":{},"extra":true}`, `{"schemaVersion":1,"schemaVersion":1,"connections":{}}`} {
		p := r.file("catalog.json", []byte(b))
		exit, code := 2, "invalid_config"
		if i == 0 {
			exit, code = 0, ""
		}
		v := r.run(exit, code, "config", "validate", "--file", p, "--json")
		if i == 0 && string(v.envelope.Data) != `{"valid":true}` {
			t.Fatal(v.stdout)
		}
	}
	r.offline()
}

func TestImportPreviewBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	v := r.run(0, "", "import", "mcporter", "--file", r.fixture(), "--json", "--no-input")
	report := reportData(t, v)
	a := 0
	for _, row := range report.Entries {
		if row.Applicable {
			a++
		}
	}
	if len(report.Entries) != 32 || a != 32 || report.Applied || report.Revision == nil || *report.Revision != 0 {
		t.Fatal(v.stdout)
	}
	for _, p := range []string{r.paths.ConfigDir, r.paths.StateDir, r.paths.RuntimeDir} {
		if entries, e := os.ReadDir(p); e != nil || len(entries) != 0 {
			t.Fatal(p, entries, e)
		}
	}
	r.offline()
}

// One literal OAuth client secret blocks the whole apply.
const blockedImport = `{"mcpServers":{"bad":{"baseUrl":"https://x.invalid/mcp","auth":"oauth","oauthClientSecret":"literal-canary"},"good":{"command":"fixture"}}}`

func TestImportApplyBlockedBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	v := r.run(2, "import_blocked", "import", "mcporter", "--file", r.file("blocked.json", []byte(blockedImport)), "--apply", "--json")
	if string(v.envelope.Data) != "null" || strings.Contains(v.stdout+v.stderr, "canary") {
		t.Fatal(v.stdout)
	}
	var raw struct {
		Error struct {
			Details struct {
				Report config.ImportReport `json:"importReport"`
			} `json:"details"`
		} `json:"error"`
	}
	if e := json.Unmarshal([]byte(v.stdout), &raw); e != nil || len(raw.Error.Details.Report.Entries) != 2 {
		t.Fatal(v.stdout, e)
	}
	s, e := config.ReadState(context.Background(), r.paths)
	if e != nil || len(s.Personal.Connections) != 0 || s.Selections.Revision != 0 {
		t.Fatal(s, e)
	}
	r.offline()
}

func TestImportApplyOnlyBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	p := r.fixture()
	before, _ := os.ReadFile(p)
	v := r.run(0, "", "import", "mcporter", "--file", p, "--only", "paper", "--only", "context7", "--apply", "--json")
	report := reportData(t, v)
	a, o := 0, 0
	for _, row := range report.Entries {
		if row.Applied {
			a++
		}
		if !row.Selected {
			o++
		}
	}
	if a != 2 || o != 30 || !report.Applied {
		t.Fatal(v.stdout)
	}
	s, e := config.ReadState(context.Background(), r.paths)
	if e != nil || !s.Selections.Connections["local:paper"].Enabled || !s.Selections.Connections["local:context7"].Enabled {
		t.Fatal(s, e)
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("source changed")
	}
	r.offline()
}

func TestConfigProfileCommandsBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	p := r.file("profile.json", []byte(`{"mode":"desktop-service-account","account":"Fixture account","bootstrapRef":"op://Private/fixture/token"}`))
	r.run(0, "", "config", "profile", "set", "work", "--file", p, "--json")
	s, e := config.ReadState(context.Background(), r.paths)
	if e != nil {
		t.Fatal(e)
	}
	s, e = config.NewStore(r.paths).Update(context.Background(), s.Selections.Revision, func(s *config.State) error {
		s.Personal.CredentialProfiles["need"] = config.ProfileRequirement{}
		s.Personal.Connections["protected"] = config.Connection{CredentialProfile: "need", Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}}}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	r.run(0, "", "config", "profile", "bind", "protected", "work", "--json")
	after, e := config.ReadState(context.Background(), r.paths)
	sel := after.Selections.Connections["local:protected"]
	if e != nil || after.Selections.Revision != s.Selections.Revision+1 || sel.CredentialProfile != "work" || sel.Enabled {
		t.Fatal(after, e)
	}
	r.offline()
}

func TestConfigInputBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	_, e := config.NewStore(r.paths).Update(context.Background(), 0, func(s *config.State) error {
		s.Personal.Connections["test"] = config.Connection{Inputs: map[string]config.Input{"endpoint": {Kind: "url", Description: "Endpoint"}, "path": {Kind: "path", Description: "Path"}}, Transport: config.Transport{HTTP: &config.HTTP{URL: config.Value{Input: &config.InputRef{Input: "endpoint"}}}}}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	r.run(0, "", "config", "input", "set", "test", "endpoint", "https://fixture.invalid", "--json")
	s, e := config.ReadState(context.Background(), r.paths)
	if e != nil || s.Selections.Revision != 2 || s.Selections.Connections["local:test"].Enabled {
		t.Fatal(s, e)
	}
	for _, a := range [][]string{{"unknown", "value"}, {"path", "relative"}} {
		r.run(2, "invalid_config", append([]string{"config", "input", "set", "test"}, append(a, "--json")...)...)
	}
	after, _ := config.ReadState(context.Background(), r.paths)
	if !reflect.DeepEqual(s, after) {
		t.Fatal("failed mutation changed state")
	}
	r.offline()
}

func TestStage2SurvivesImportBlackBox(t *testing.T) {
	r := newRig(t)
	first := r.call("fixture.counter")
	pid := r.status().PID
	if structured(t, first)["count"] != float64(1) {
		t.Fatal(first.stdout)
	}
	before, e := os.ReadFile(r.paths.PersonalFile)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(map[string]any{"mcpServers": map[string]any{"imported": map[string]any{"command": fixtureBinary, "env": map[string]string{"MCPARCEL_FIXTURE_STDIO": "1", "MCPARCEL_FIXTURE_ROOT": r.root}}}})
	if e != nil {
		t.Fatal(e)
	}
	source := r.root + "/source.json"
	r.write(source, string(raw), 0o644)
	r.check(r.run("import", "mcporter", "--file", source, "--json"), 0, "")
	after, _ := os.ReadFile(r.paths.PersonalFile)
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed personal")
	}
	r.check(r.run("import", "mcporter", "--file", source, "--apply", "--json"), 0, "")
	if v := r.call("fixture.counter"); structured(t, v)["count"] != float64(2) || r.status().PID != pid {
		t.Fatal("old session replaced", v.stdout)
	}
	r.check(r.run("tools", "imported", "--json"), 0, "")
	if v := r.call("imported.counter"); structured(t, v)["count"] != float64(1) {
		t.Fatal(v.stdout)
	}
	sourceAfter, _ := os.ReadFile(source)
	if !bytes.Equal(raw, sourceAfter) {
		t.Fatal("source changed")
	}
	state, e := config.ReadState(context.Background(), r.paths)
	if e != nil {
		t.Fatal(e)
	}
	original, e := config.DecodeCatalog(before)
	if e != nil || !reflect.DeepEqual(state.Personal.Connections["fixture"], original.Connections["fixture"]) || !state.Selections.Connections["local:fixture"].Enabled {
		t.Fatal(state, e)
	}
}

func TestSelectionChangeBlocksNextCallBlackBox(t *testing.T) {
	for _, code := range []string{"connection_disabled", "review_required", "tool_denied"} {
		t.Run(code, func(t *testing.T) {
			r := newRig(t)
			r.call("fixture.counter")
			r.waitFile(r.root+"/started", "counter")
			before, e := os.ReadFile(r.root + "/started")
			if e != nil {
				t.Fatal(e)
			}
			store := config.NewStore(r.paths)
			state, e := store.Read(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			_, e = store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
				sel := s.Selections.Connections["local:fixture"]
				switch code {
				case "connection_disabled":
					sel.Enabled = false
				case "review_required":
					sel.ReviewRequired = true
				case "tool_denied":
					sel.DisabledTools = []string{"counter"}
				}
				s.Selections.Connections["local:fixture"] = sel
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			r.check(r.run("call", "fixture.counter", "--json"), 4, code)
			after, e := os.ReadFile(r.root + "/started")
			if e != nil || !bytes.Equal(before, after) {
				t.Fatal("blocked invocation dispatched", string(after), e)
			}
		})
	}
}

func TestImportPrivacyBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	source := r.file("privacy.json", []byte(`{"mcpServers":{"bad":{"command":"fixture","env":{"API_KEY":"ENV-CANARY"},"headers":{"Authorization":"HEADER-CANARY"},"oauthClientSecret":"OAUTH-CANARY","args":["--password","ARG-CANARY"],"unknown":"UNKNOWN-CANARY"},"good":{"command":"fixture"}}}`))
	v := r.run(2, "import_blocked", "import", "mcporter", "--file", source, "--apply", "--json")
	if strings.Contains(v.stdout+v.stderr, "CANARY") {
		t.Fatal("JSON leak")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, binaryA, "import", "mcporter", "--file", source, "--apply")
	c.Env = r.env
	var out, errOut bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errOut
	e := c.Run()
	x, ok := e.(*exec.ExitError)
	if !ok || x.ExitCode() != 2 || !strings.Contains(out.String(), "bad: blocked") || !strings.Contains(errOut.String(), "Selected import entries require changes.") || strings.Contains(out.String()+errOut.String(), "CANARY") {
		t.Fatal(e, out.String(), errOut.String())
	}
	ioFailure := r.run(2, "invalid_config", "import", "mcporter", "--file", r.paths.Home+"/IO-CANARY", "--json")
	if strings.Contains(ioFailure.stdout+ioFailure.stderr, "CANARY") {
		t.Fatal("I/O error leak")
	}
	r.run(0, "", "import", "mcporter", "--file", source, "--only", "good", "--apply", "--json")
	for _, dir := range []string{r.paths.ConfigDir, r.paths.StateDir, r.paths.RuntimeDir} {
		e := filepath.WalkDir(dir, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() {
				raw, e := os.ReadFile(path)
				if e != nil {
					return e
				}
				if strings.Contains(string(raw), "CANARY") {
					t.Error("destination/log leak", path)
				}
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	r.offline()
}

func TestImportAssignmentPrivacyBlackBox(t *testing.T) {
	r := newMetadataRig(t)
	for _, apply := range []bool{false, true} {
		source := r.file("assignments.json", []byte(`{"mcpServers":{"bad":{"command":"fixture","args":["--endpoint=https://x.invalid?token=IMPORT_SECRET_CANARY","SERVICE_URL=https://user:IMPORT_SECRET_CANARY@x.invalid","--env=API_KEY=IMPORT_SECRET_CANARY"]}}}`))
		args := []string{"import", "mcporter", "--file", source, "--json"}
		exit, code := 0, ""
		if apply {
			args = append(args, "--apply")
			exit, code = 2, "import_blocked"
		}
		v := r.run(exit, code, args...)
		if strings.Contains(v.stdout+v.stderr, "IMPORT_SECRET_CANARY") {
			t.Error("output leaked credential")
		}
		// The input necessarily contains the canary; dispose of it before auditing all output roots.
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{r.paths.Home, r.paths.ConfigDir, r.paths.DataDir, r.paths.StateDir, r.paths.CacheDir, r.paths.RuntimeDir} {
			if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				data, err := os.ReadFile(path)
				if strings.Contains(string(data), "IMPORT_SECRET_CANARY") {
					t.Errorf("file leaked credential: %s", path)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	r.offline()
}

func TestConfigImportStage4Regression(t *testing.T) {
	r := newMetadataRig(t)
	state := seedMetadata(t, r, true)
	store := config.NewStore(r.paths)
	state, e := store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		s.Local.Aliases["preserved"] = "local:personal"
		s.Personal.CredentialProfiles["team"] = config.ProfileRequirement{}
		s.Personal.Connections["protected"] = config.Connection{CredentialProfile: "team", Inputs: map[string]config.Input{"arg": {Kind: "string", Description: "Argument"}}, Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture"), Args: []config.Value{{Input: &config.InputRef{Input: "arg"}}}}}}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	source := state.Local.Sources[0]
	snapshot := filepath.Join(r.paths.DataDir, "catalogs", source.ID, source.Commit+".json")
	before, e := os.ReadFile(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	valid := r.file("catalog.json", []byte(`{"schemaVersion":1,"connections":{}}`))
	if v := r.run(0, "", "config", "validate", "--file", valid, "--json"); string(v.envelope.Data) != `{"valid":true}` {
		t.Fatal(v.stdout)
	}
	profile := r.file("profile.json", []byte(`{"mode":"desktop","account":"Fixture"}`))
	r.run(0, "", "config", "profile", "set", "work", "--file", profile, "--json")
	r.run(0, "", "config", "profile", "bind", "protected", "work", "--json")
	r.run(0, "", "config", "input", "set", "protected", "arg", "first", "--json")
	r.run(0, "", "enable", "protected", "--json")
	r.run(0, "", "config", "input", "set", "protected", "arg", "second", "--json")
	current, e := store.Read(context.Background())
	if e != nil || !current.Selections.Connections["local:protected"].ReviewRequired {
		t.Fatal(current, e)
	}
	raw := r.fixture()
	preview := reportData(t, r.run(0, "", "import", "mcporter", "--file", raw, "--json"))
	counts := map[string]int{}
	oauth, refs := 0, 0
	for _, row := range preview.Entries {
		counts[row.Transport]++
		if row.OAuth {
			oauth++
		}
		for _, issue := range row.Unresolved {
			if issue.Code == "unresolved_credential" {
				refs++
			}
		}
	}
	if len(preview.Entries) != 32 || counts["stdio"] != 17 || counts["http"] != 15 || oauth != 10 || refs != 0 || preview.Revision == nil || *preview.Revision != current.Selections.Revision {
		t.Fatal(preview, counts, oauth, refs)
	}
	blocked := r.run(2, "import_blocked", "import", "mcporter", "--file", r.file("blocked.json", []byte(blockedImport)), "--apply", "--json")
	if string(blocked.envelope.Data) != "null" || blocked.envelope.Error.Details["importReport"] == nil {
		t.Fatal(blocked.stdout)
	}
	applied := reportData(t, r.run(0, "", "import", "mcporter", "--file", raw, "--only", "paper", "--only", "context7", "--apply", "--json"))
	n := 0
	for _, row := range applied.Entries {
		if row.Applied {
			n++
		}
	}
	if n != 2 || !applied.Applied || applied.Revision == nil || *applied.Revision != current.Selections.Revision+1 {
		t.Fatal(applied)
	}
	after, e := store.Read(context.Background())
	if e != nil || !reflect.DeepEqual(after.Local.Sources, state.Local.Sources) || after.Local.Aliases["preserved"] != "local:personal" || !after.Selections.Connections["local:protected"].ReviewRequired {
		t.Fatal(after, e)
	}
	bytesAfter, e := os.ReadFile(snapshot)
	if e != nil || !bytes.Equal(before, bytesAfter) {
		t.Fatal("snapshot changed", e)
	}
	r.offline()
}
