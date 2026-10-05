package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestLegacySelectionBridge(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	writeFile(t, p.PersonalFile, protectedPersonal, 0o644)
	writeFile(t, p.ConfigFile, localTeam, 0o644)
	before, _ := os.ReadDir(p.ConfigDir)
	s, e := config.ReadState(context.Background(), p)
	if e != nil || !s.Legacy || s.Selections.Revision != 0 || !s.Selections.Connections["local:protected"].Enabled || s.Selections.Connections["local:protected"].CredentialProfile != "team" {
		t.Fatal(s, e)
	}
	after, _ := os.ReadDir(p.ConfigDir)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("wrote state")
	}
	snap, e := config.Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = snap.RuntimeConnection("protected"); e != nil {
		t.Fatal(e)
	}
}

func TestExplicitSelectionsDisableByDefault(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	writeFile(t, p.PersonalFile, protectedPersonal, 0o644)
	writeFile(t, p.ConfigFile, localTeam, 0o644)
	writeFile(t, p.SelectionsFile, `{"schemaVersion":1,"revision":1,"connections":{}}`, 0o644)
	s, e := config.ReadState(context.Background(), p)
	if e != nil || s.Legacy {
		t.Fatal(s, e)
	}
	snap, e := config.Load(p)
	if e != nil || snap.Revision != 1 {
		t.Fatal(snap, e)
	}
	if _, _, e = snap.Connection("paper"); !errors.Is(e, config.ErrDisabled) {
		t.Fatal(e)
	}
}

func TestReadStateSnapshots(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	s, e := config.ReadState(context.Background(), p)
	if e != nil || s.Personal.SchemaVersion != 1 || s.Personal.Connections == nil || s.Local.Aliases == nil || s.Catalogs == nil {
		t.Fatal(s, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = config.ReadState(ctx, p); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	commit := strings.Repeat("a", 40)
	writeFile(t, p.ConfigFile, `{"schemaVersion":1,"sources":[{"id":"github-1","repositoryId":1,"owner":"example","repo":"tools.v2","path":"catalog.json","ref":"main","commit":"`+commit+`","pinned":false}]}`, 0o644)
	if _, e = config.ReadState(context.Background(), p); !errors.Is(e, config.ErrConfig) {
		t.Fatal(e)
	}
	path := filepath.Join(p.DataDir, "catalogs", "github-1", commit+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"stdio","command":"fixture"}}}}`, 0o644)
	s, e = config.ReadState(context.Background(), p)
	if e != nil || len(s.Catalogs) != 1 || len(s.Selections.Connections) != 0 {
		t.Fatal(s, e)
	}
	snap, e := config.Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = snap.Connection("paper"); !errors.Is(e, config.ErrDisabled) {
		t.Fatal(e)
	}
	writeFile(t, p.SelectionsFile, `{"schemaVersion":1,"revision":0,"connections":null}`, 0o644)
	if _, e = config.ReadState(context.Background(), p); !errors.Is(e, config.ErrConfig) {
		t.Fatal(e)
	}
}

func TestResolvedHashScope(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	personal := `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{"paper":{"credentialProfile":"team","inputs":{"cmd":{"kind":"path","description":"Command"}},"transport":{"type":"stdio","command":{"input":"cmd"}}},"other":{"transport":{"type":"stdio","command":"fixture"}}}}`
	writeFile(t, p.PersonalFile, personal, 0o644)
	writeFile(t, p.ConfigFile, localTeam, 0o644)
	sel := config.Selections{SchemaVersion: 1, Revision: 1, Connections: map[string]config.Selection{"local:paper": {Enabled: true, Inputs: map[string]string{"cmd": "/fixture"}, CredentialProfile: "team"}}}
	save := func() {
		b, e := json.Marshal(sel)
		if e != nil {
			t.Fatal(e)
		}
		writeFile(t, p.SelectionsFile, string(b), 0o644)
	}
	save()
	first, e := config.Load(p)
	if e != nil {
		t.Fatal(e)
	}
	hash, e := first.ConnectionHash("paper")
	if e != nil {
		t.Fatal(e)
	}
	check := func(same bool) {
		s, e := config.Load(p)
		if e != nil {
			t.Fatal(e)
		}
		h, e := s.ConnectionHash("paper")
		if e != nil || (h == hash) != same {
			t.Fatal(h, hash, e)
		}
	}
	writeFile(t, p.PersonalFile, strings.Replace(personal, `"credentialProfile":"team"`, `"label":"Changed","description":"Changed","credentialProfile":"team"`, 1), 0o644)
	check(true)
	writeFile(t, p.PersonalFile, strings.Replace(personal, `"command":"fixture"`, `"command":"other"`, 1), 0o644)
	check(true)
	v := sel.Connections["local:paper"]
	v.Inputs["cmd"] = "/new"
	sel.Connections["local:paper"] = v
	save()
	check(false)
	v.Inputs["cmd"] = "/fixture"
	v.DisabledTools = []string{"write"}
	sel.Connections["local:paper"] = v
	save()
	check(false)
	v.DisabledTools = nil
	sel.Connections["local:paper"] = v
	save()
	writeFile(t, p.ConfigFile, strings.Replace(localTeam, "Fixture", "Other", 1), 0o644)
	check(false)
	_, c, e := first.Connection("paper")
	if e != nil {
		t.Fatal(e)
	}
	c.Transport.Stdio.Command = config.Literal("/changed")
	again, e := first.ConnectionHash("paper")
	if e != nil || again != hash {
		t.Fatal("mutable snapshot", e)
	}
}

func TestReadStateFreshRootCreatesNothing(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	beforeConfig, e := os.ReadDir(p.ConfigDir)
	if e != nil {
		t.Fatal(e)
	}
	beforeState, e := os.ReadDir(p.StateDir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := config.NewStore(p).Read(context.Background()); e != nil {
		t.Fatal(e)
	}
	afterConfig, _ := os.ReadDir(p.ConfigDir)
	afterState, _ := os.ReadDir(p.StateDir)
	if !reflect.DeepEqual(beforeConfig, afterConfig) || !reflect.DeepEqual(beforeState, afterState) {
		t.Fatal(afterConfig, afterState)
	}
}

func TestDanglingSelectionsSymlinkFailsClosed(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	writeFile(t, p.PersonalFile, protectedPersonal, 0o644)
	writeFile(t, p.ConfigFile, localTeam, 0o644)
	target := filepath.Join(p.Home, "removed-selections.json")
	if err := os.Symlink(target, p.SelectionsFile); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(p); !errors.Is(err, config.ErrUnsafePath) {
		t.Fatalf("dangling selections must fail closed: %v", err)
	}
}

func TestDanglingDocumentsFailClosed(t *testing.T) {
	for _, document := range []string{"config", "personal", "source"} {
		t.Run(document, func(t *testing.T) {
			p, _ := testutil.IsolatedPaths(t)
			path := p.ConfigFile
			if document == "personal" {
				path = p.PersonalFile
			}
			if document == "source" {
				commit := strings.Repeat("a", 40)
				writeFile(t, p.ConfigFile, `{"schemaVersion":1,"sources":[{"id":"github-1","repositoryId":1,"owner":"example","repo":"tools","path":"catalog.json","ref":"main","commit":"`+commit+`","pinned":false}]}`, 0o644)
				path = filepath.Join(p.DataDir, "catalogs", "github-1", commit+".json")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join(p.Home, "missing.json"), path); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(p); !errors.Is(err, config.ErrUnsafePath) {
				t.Fatalf("dangling %s must be unsafe: %v", document, err)
			}
		})
	}
}
