package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func localFile(t *testing.T, p config.Paths, body string) string {
	t.Helper()
	path := filepath.Join(p.Home, "definition.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const localPaper = `{"id":"paper","transport":{"type":"stdio","command":"nonexistent-fixture","args":["old"]}}`

func TestLocalAddDisabled(t *testing.T) {
	p := metadataEnv(t)
	if err := os.WriteFile(p.PersonalFile, []byte(`{"schemaVersion":1,"connections":{"old":{"transport":{"type":"stdio","command":"fixture"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	file := localFile(t, p, localPaper)
	selectionRun(t, 0, "", "local", "add", "--file", file)
	store := config.NewStore(p)
	state := selectionRead(t, store)
	if state.Legacy || state.Selections.Revision != 1 || !state.Selections.Connections["local:old"].Enabled || state.Selections.Connections["local:paper"].Enabled || len(state.Local.Aliases) != 0 {
		t.Fatal(state)
	}
	selectionRun(t, 0, "", "local", "add", "--file", file)
	if !reflect.DeepEqual(state, selectionRead(t, store)) {
		t.Fatal("identical add changed selection or revision")
	}
	localFile(t, p, strings.Replace(localPaper, "old", "new", 1))
	selectionRun(t, 2, "invalid_config", "local", "add", "--file", file)
	if !reflect.DeepEqual(state, selectionRead(t, store)) {
		t.Fatal("conflicting add changed state")
	}
}

func TestLocalDefinitionStrict(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	state := selectionRead(t, store)
	for _, raw := range []string{
		`{"id":"paper","id":"paper","transport":{"type":"stdio","command":"fixture"}}`,
		`{"id":"paper","transport":{"type":"stdio","command":"fixture"},"transport":{"type":"stdio","command":"fixture"}}`,
		`{"id":"paper","transport":{"type":"stdio","command":"fixture","command":"fixture"}}`,
		`{"id":"paper","enabled":true,"transport":{"type":"stdio","command":"fixture"}}`,
		`{"transport":{"type":"stdio","command":"fixture"}}`,
		`{"ID":"paper","transport":{"type":"stdio","command":"fixture"}}`,
		`{"id":"local:paper","transport":{"type":"stdio","command":"fixture"}}`,
		`{"id":"github:fixture-owner/fixture.repo#paper","transport":{"type":"stdio","command":"fixture"}}`,
		`{"id":null,"transport":{"type":"stdio","command":"fixture"}}`,
		`{"id":1,"transport":{"type":"stdio","command":"fixture"}}`,
		`{"id":"paper","transport":null}`,
		`{"id":"paper","Transport":{"type":"stdio","command":"fixture"}}`,
		`{"id":"paper","domains":["other"],"transport":{"type":"stdio","command":"fixture"}}`,
		`{"id":"paper","inputs":null,"transport":{"type":"stdio","command":"fixture"}}`,
	} {
		file := localFile(t, p, raw)
		selectionRun(t, 2, "invalid_config", "local", "add", "--file", file)
		if !reflect.DeepEqual(state, selectionRead(t, store)) {
			t.Fatal("invalid file changed state", raw)
		}
	}
	for _, name := range []string{"paper", "local:paper"} {
		id, e := localID(name)
		if e != nil || id != "paper" {
			t.Fatal(id, e)
		}
	}
	for _, name := range []string{"github:fixture-owner/fixture.repo#paper", "Paper", "local:", "local:local:paper", "bad/name"} {
		if _, e := localID(name); !errors.Is(e, config.ErrConfig) {
			t.Fatal(name, e)
		}
	}
	file := localFile(t, p, localPaper)
	selectionRun(t, 0, "", "local", "add", "--file", file)
	before := selectionRead(t, store)
	selectionRun(t, 2, "invalid_config", "local", "update", "github:fixture-owner/fixture.repo#paper", "--file", file)
	localFile(t, p, strings.Replace(localPaper, `"id":"paper"`, `"id":"other"`, 1))
	selectionRun(t, 2, "invalid_config", "local", "update", "paper", "--file", file)
	if !reflect.DeepEqual(before, selectionRead(t, store)) {
		t.Fatal("mismatch changed state")
	}
}

func TestLocalDomainsAndRequirement(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	file := localFile(t, p, `{"id":"paper","domains":["docs"],"credentialProfile":"team","transport":{"type":"stdio","command":"fixture","env":{"TOKEN":{"secret":"op://Fixture/item/token"}}}}`)
	selectionRun(t, 0, "", "local", "add", "--file", file)
	state := selectionRead(t, store)
	if state.Personal.Domains["docs"].Label != "docs" || state.Personal.CredentialProfiles["team"] != (config.ProfileRequirement{}) || len(state.Local.CredentialProfiles) != 0 || state.Selections.Connections["local:paper"].CredentialProfile != "" || state.Selections.Connections["local:paper"].Enabled {
		t.Fatal(state)
	}
	effective, e := config.Resolve(state)
	if e != nil || !reflect.DeepEqual(effective.Connections["local:paper"].Blockers, []string{"connection_disabled", "config_required"}) {
		t.Fatal(effective, e)
	}
	state, e = store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		s.Personal.Domains["docs"] = config.Domain{Label: "Existing docs"}
		s.Personal.CredentialProfiles["team"] = config.ProfileRequirement{Description: "Existing team"}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	id, cat, e := decodeLocalDefinition([]byte(strings.Replace(localPaper, `"transport"`, `"domains":["docs"],"credentialProfile":"team","transport"`, 1)), state.Personal)
	if e != nil || id != "paper" || cat.Domains["docs"].Label != "Existing docs" || cat.CredentialProfiles["team"].Description != "Existing team" {
		t.Fatal(cat, e)
	}
	cat.Domains["docs"] = config.Domain{Label: "Changed copy"}
	cat.Connections["paper"].Transport.Stdio.Args[0] = config.Literal("changed")
	if state.Personal.Domains["docs"].Label != "Existing docs" {
		t.Fatal("decoder aliases input")
	}
}

func TestLocalUpdateReview(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	file := localFile(t, p, localPaper)
	selectionRun(t, 0, "", "local", "add", "--file", file)
	selectionRun(t, 0, "", "enable", "paper")
	selectionRun(t, 0, "", "tools", "disable", "paper", "write")
	before := selectionRead(t, store)
	localFile(t, p, strings.Replace(localPaper, "old", "new", 1))
	selectionRun(t, 0, "", "local", "update", "local:paper", "--file", file)
	state := selectionRead(t, store)
	want := before.Selections.Connections["local:paper"]
	want.ReviewRequired = true
	if !reflect.DeepEqual(want, state.Selections.Connections["local:paper"]) {
		t.Fatal(state)
	}
	selectionRun(t, 0, "", "enable", "paper")
	before = selectionRead(t, store)
	localFile(t, p, strings.Replace(strings.Replace(localPaper, "old", "new", 1), `"transport"`, `"description":"Revised","transport"`, 1))
	selectionRun(t, 0, "", "local", "update", "paper", "--file", file)
	state = selectionRead(t, store)
	if !reflect.DeepEqual(before.Selections.Connections["local:paper"], state.Selections.Connections["local:paper"]) {
		t.Fatal(state)
	}
	if _, e := os.Stat(p.SocketFile); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}

func TestLocalRemoveAlias(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	report, e := config.ImportMcporter([]byte(`{"mcpServers":{"paper":{"command":"fixture"}}}`), nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = config.ApplyImport(context.Background(), store, 0, report, nil); e != nil {
		t.Fatal(e)
	}
	state := selectionRead(t, store)
	source := config.Source{ID: "github-42", RepositoryID: 42, Owner: "fixture-owner", Repo: "fixture.repo", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("a", 40)}
	_, e = store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		s.Local.Sources = []config.Source{source}
		s.Catalogs[source.ID] = config.Catalog{SchemaVersion: 1, Connections: map[string]config.Connection{"paper": s.Personal.Connections["paper"]}}
		s.Selections.Connections["github:fixture-owner/fixture.repo#paper"] = config.Selection{Enabled: true}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	before := selectionRead(t, store)
	selectionRun(t, 0, "", "local", "remove", "paper")
	state = selectionRead(t, store)
	if state.Local.Aliases["paper"] != "local:paper" || !reflect.DeepEqual(before.Selections.Connections, state.Selections.Connections) || !reflect.DeepEqual(before.Local, state.Local) || !reflect.DeepEqual(before.Catalogs, state.Catalogs) {
		t.Fatal(state)
	}
	loaded, e := config.Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = loaded.Connection("paper"); !errors.Is(e, config.ErrNotFound) {
		t.Fatal(e)
	}
	selectionRun(t, 0, "", "inspect", "paper")
	selectionRun(t, 4, "connection_unavailable", "local", "remove", "local:paper")
	selectionRun(t, 2, "invalid_config", "local", "remove", "github:fixture-owner/fixture.repo#paper")
}

func TestLocalFileSafety(t *testing.T) {
	p := metadataEnv(t)
	file := localFile(t, p, localPaper)
	if e := os.Chmod(file, 0o644); e != nil {
		t.Fatal(e)
	}
	selectionRun(t, 0, "", "local", "add", "--file", file)
	link := filepath.Join(p.Home, "safe-link")
	if e := os.Symlink(file, link); e != nil {
		t.Fatal(e)
	}
	selectionRun(t, 0, "", "local", "add", "--file", link)
	fifo := filepath.Join(p.Home, "fifo")
	if e := syscall.Mkfifo(fifo, 0o600); e != nil {
		t.Fatal(e)
	}
	dangling := filepath.Join(p.Home, "dangling")
	if e := os.Symlink(filepath.Join(p.Home, "absent"), dangling); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{fifo, dangling} {
		selectionRun(t, 2, "unsafe_local_path", "local", "add", "--file", path)
	}
	if e := os.Chmod(file, 0o664); e != nil {
		t.Fatal(e)
	}
	selectionRun(t, 2, "unsafe_local_path", "local", "add", "--file", file)
	if e := os.Chmod(file, 0o600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(file, bytes.Repeat([]byte("x"), 2*1024*1024+1), 0o600); e != nil {
		t.Fatal(e)
	}
	selectionRun(t, 2, "invalid_config", "local", "add", "--file", file)
}

func TestLocalDoesNotAcceptByUpdate(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	file := localFile(t, p, localPaper)
	selectionRun(t, 0, "", "local", "add", "--file", file)
	selectionRun(t, 0, "", "enable", "paper")
	state := selectionRead(t, store)
	_, e := store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		sel := s.Selections.Connections["local:paper"]
		sel.ReviewRequired = true
		s.Selections.Connections["local:paper"] = sel
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	before := selectionRead(t, store)
	selectionRun(t, 0, "", "local", "update", "paper", "--file", file)
	if !reflect.DeepEqual(before, selectionRead(t, store)) {
		t.Fatal("identical update accepted or incremented")
	}
}

func TestLocalIncompatibleBindings(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	raw := `{"id":"paper","inputs":{"arg":{"kind":"string","description":"Argument"}},"credentialProfile":"team","transport":{"type":"stdio","command":"fixture"}}`
	file := localFile(t, p, raw)
	selectionRun(t, 0, "", "local", "add", "--file", file)
	profile := filepath.Join(p.Home, "profile.json")
	if e := os.WriteFile(profile, []byte(`{"mode":"desktop","account":"Fixture"}`), 0o600); e != nil {
		t.Fatal(e)
	}
	selectionRun(t, 0, "", "config", "profile", "set", "work", "--file", profile)
	selectionRun(t, 0, "", "config", "profile", "bind", "paper", "work")
	selectionRun(t, 0, "", "config", "input", "set", "paper", "arg", "relative")
	selectionRun(t, 0, "", "enable", "paper")
	before := selectionRead(t, store)
	for _, bad := range []string{strings.Replace(raw, `"kind":"string"`, `"kind":"path"`, 1), strings.Replace(raw, `"inputs":{"arg":{"kind":"string","description":"Argument"}},`, "", 1), strings.Replace(raw, `"credentialProfile":"team",`, "", 1)} {
		localFile(t, p, bad)
		selectionRun(t, 2, "config_required", "local", "update", "paper", "--file", file)
		if !reflect.DeepEqual(before, selectionRead(t, store)) {
			t.Fatal("bindings changed")
		}
	}
}

func TestLocalOrphanAdd(t *testing.T) {
	p, store, state := metadataSeed(t)
	state, e := store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error {
		s.Selections.Connections["local:removed"] = config.Selection{Enabled: true, Inputs: map[string]string{"arg": "retained"}, DisabledTools: []string{"write"}}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	file := localFile(t, p, `{"id":"removed","inputs":{"arg":{"kind":"string","description":"Argument"}},"transport":{"type":"stdio","command":"fixture"}}`)
	selectionRun(t, 0, "", "local", "add", "--file", file)
	want := state.Selections.Connections["local:removed"]
	want.Enabled = false
	want.ReviewRequired = true
	if !reflect.DeepEqual(want, selectionRead(t, store).Selections.Connections["local:removed"]) {
		t.Fatal("orphan settings lost")
	}
}

func TestLocalDecoderCopy(t *testing.T) {
	p, _, state := metadataSeed(t)
	_ = p
	before, e := json.Marshal(state.Personal)
	if e != nil {
		t.Fatal(e)
	}
	_, cat, e := decodeLocalDefinition([]byte(localPaper), state.Personal)
	if e != nil {
		t.Fatal(e)
	}
	cat.Connections["disabled"].Transport.Stdio.Command = config.Literal("changed")
	after, _ := json.Marshal(state.Personal)
	if !bytes.Equal(before, after) {
		t.Fatal("decoder shares nested pointers")
	}
}

// An invalid definition names the failing field, never a value.
func TestLocalAddNamesField(t *testing.T) {
	p := metadataEnv(t)
	for raw, want := range map[string]string{
		`{"id":"paper","domains":["other"],"transport":{"type":"stdio","command":"fixture"}}`:                  `domains: other is reserved; omit domains to list the connection under Other`,
		`{"id":"paper","transport":{"type":"stdio","command":"fixture"},"callTimeout":"soon"}`:                 `connections.paper.callTimeout: positive duration required`,
		`{"id":"paper","transport":{"type":"stdio","command":"fixture","env":{"TOKEN":"secret-value"}},"x":1}`: `connections.paper.x: unknown field`,
	} {
		file := localFile(t, p, raw)
		got, out, _ := run(t, "local", "add", "--file", file, "--json")
		if got != 2 || !strings.Contains(out, `"code":"invalid_config"`) || !strings.Contains(out, `"message":"Invalid definition: `+want+`."`) || !strings.Contains(out, "Fix the definition file") || strings.Contains(out, "secret-value") {
			t.Fatal(raw, out)
		}
	}
	file := localFile(t, p, localPaper)
	selectionRun(t, 0, "", "local", "add", "--file", file)
	localFile(t, p, strings.Replace(localPaper, "old", "new", 1))
	if got, _, errOut := run(t, "local", "add", "--file", file); got != 2 || !strings.Contains(errOut, "id: a different connection with this ID exists; use local update") {
		t.Fatal(got, errOut)
	}
	localFile(t, p, strings.Replace(localPaper, `"id":"paper"`, `"id":"other"`, 1))
	if got, out, _ := run(t, "local", "update", "paper", "--file", file, "--json"); got != 2 || !strings.Contains(out, "id: does not match the connection being updated") {
		t.Fatal(got, out)
	}
}
