package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// configListing records every entry below dir: name, type, size and mtime.
func configListing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, fmt.Sprintf("%s %v %d %d", path, info.Mode(), info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHeadlessConfigReadOnlyBlackBox(t *testing.T) {
	r, api, _, _ := catalogFixture(t)
	_, err := config.NewStore(r.paths).Update(context.Background(), 0, func(s *config.State) error {
		stdio := config.Transport{Stdio: &config.Stdio{Command: config.Literal("fixture")}}
		s.Personal.Connections["alpha"] = config.Connection{Transport: stdio}
		s.Personal.Connections["beta"] = config.Connection{Transport: stdio, Inputs: map[string]config.Input{"endpoint": {Kind: "url", Description: "Endpoint"}}}
		s.Selections.Connections["local:alpha"] = config.Selection{Enabled: false}
		s.Selections.Connections["local:beta"] = config.Selection{Enabled: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(r.paths.Home), "state-root")
	if err = os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "runtime": map[string]any{"mode": "headless", "stateRoot": root}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(r.paths.ConfigFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// The lock file of the desktop seed stays; a ConfigMap has none, and
	// headless never needs one.
	if err = os.Remove(filepath.Join(r.paths.ConfigDir, ".mcparcel.lock")); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(r.paths.ConfigDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.paths.ConfigDir, 0o700) })
	definition := r.file("definition.json", []byte(`{"id":"gamma","transport":{"type":"stdio","command":"fixture"}}`))
	mcporter := r.fixture()
	before := configListing(t, r.paths.ConfigDir)
	for _, argv := range [][]string{
		{"enable", "alpha"},
		{"disable", "beta"},
		{"tools", "disable", "beta", "read"},
		{"local", "add", "--file", definition},
		{"config", "input", "set", "beta", "endpoint", "https://fixture.invalid"},
		{"import", "mcporter", "--file", mcporter, "--only", "paper", "--only", "context7", "--apply"},
		{"add", "fixture-owner/fixture.repo"},
		{"sync"},
		{"sync", "--apply"},
	} {
		v := r.run(2, "config_read_only", append(argv, "--json")...)
		var e struct {
			Error struct{ Message, NextAction string } `json:"error"`
		}
		if json.Unmarshal([]byte(v.stdout), &e) != nil || e.Error.Message != "This configuration is read-only (headless mode)." {
			t.Fatal(argv, v.stdout)
		}
	}
	if after := configListing(t, r.paths.ConfigDir); !reflect.DeepEqual(before, after) {
		t.Fatalf("config directory changed:\nbefore %v\nafter  %v", before, after)
	}
	if requests := api.Requests(); len(requests) != 0 {
		t.Fatal("catalog requests in headless mode", requests)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("offline commands wrote below the state root", entries, err)
	}
	// Reads keep working on the read-only directory.
	r.run(0, "", "list", "--json")
	r.run(0, "", "inspect", "beta", "--json")
}
