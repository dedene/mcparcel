package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const headlessConfig = `{"schemaVersion":1,"runtime":{"mode":"headless","stateRoot":"/var/lib/mcparcel"}}`

func configDirNames(t *testing.T, p Paths) []string {
	t.Helper()
	entries, err := os.ReadDir(p.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// readOnlyConfigDir makes the config directory 0555 like a read-only mount,
// restoring it so cleanup can remove it.
func readOnlyConfigDir(t *testing.T, p Paths) {
	t.Helper()
	if err := os.Chmod(p.ConfigDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p.ConfigDir, 0o700) })
}

func TestHeadlessUpdateRefusedBeforeLock(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 3, false)
	testWrite(t, p.ConfigFile, []byte(headlessConfig), 0o644)
	readOnlyConfigDir(t, p)
	before := stateFiles(t, p)
	store := NewStore(p)
	called := false
	mutate := func(s *State) error { called = true; return nil }
	if _, err := store.Update(context.Background(), 3, mutate); !errors.Is(err, ErrConfigReadOnly) {
		t.Fatal(err)
	}
	if _, err := store.UpdateAccepted(context.Background(), 3, []string{"local:paper"}, mutate); !errors.Is(err, ErrConfigReadOnly) {
		t.Fatal(err)
	}
	// A stale revision is still read-only, not a conflict: nothing was read under a lock.
	if _, err := store.Update(context.Background(), 99, mutate); !errors.Is(err, ErrConfigReadOnly) {
		t.Fatal(err)
	}
	if called {
		t.Fatal("mutate ran in headless mode")
	}
	if _, err := os.Lstat(filepath.Join(p.ConfigDir, ".mcparcel.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lock file created", err)
	}
	if names := configDirNames(t, p); !slices.Equal(names, []string{"config.json", "personal.json", "selections.json"}) {
		t.Fatal(names)
	}
	if after := stateFiles(t, p); !mapsEqual(before, after) {
		t.Fatal(before, after)
	}
	if _, err := os.Stat(p.DataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("data directory created", err)
	}
}

func TestHeadlessLegacySelectionsNotMigrated(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 0, true)
	testWrite(t, p.ConfigFile, []byte(headlessConfig), 0o644)
	readOnlyConfigDir(t, p)
	store := NewStore(p)
	state, err := store.Read(context.Background())
	if err != nil || !state.Legacy || !state.Selections.Connections["local:paper"].Enabled {
		t.Fatal(state, err)
	}
	if _, err = store.Update(context.Background(), 0, func(*State) error { return nil }); !errors.Is(err, ErrConfigReadOnly) {
		t.Fatal(err)
	}
	if _, err = Load(p); err != nil {
		t.Fatal(err)
	}
	if names := configDirNames(t, p); !slices.Equal(names, []string{"config.json", "personal.json"}) {
		t.Fatal("legacy selections migrated or lock created", names)
	}
}

func TestHeadlessReadWithoutLockFile(t *testing.T) {
	p := storePaths(t)
	seedStore(t, p, 5, false)
	testWrite(t, p.ConfigFile, []byte(headlessConfig), 0o644)
	readOnlyConfigDir(t, p)
	state, err := ReadState(context.Background(), p)
	if err != nil || state.Selections.Revision != 5 || !state.Local.Headless() {
		t.Fatal(state, err)
	}
	if err = refuseHeadlessWrite(context.Background(), p); !errors.Is(err, ErrConfigReadOnly) {
		t.Fatal(err)
	}
	if names := configDirNames(t, p); !slices.Equal(names, []string{"config.json", "personal.json", "selections.json"}) {
		t.Fatal(names)
	}
}

func TestDesktopUpdateUnchanged(t *testing.T) {
	for _, raw := range []string{"", `{"schemaVersion":1,"runtime":{"mode":"desktop","keepAlive":true}}`} {
		p := storePaths(t)
		seedStore(t, p, 2, false)
		if raw != "" {
			testWrite(t, p.ConfigFile, []byte(raw), 0o644)
		}
		if err := refuseHeadlessWrite(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		state, err := NewStore(p).Update(context.Background(), 2, func(s *State) error {
			sel := s.Selections.Connections["local:paper"]
			sel.Enabled = false
			s.Selections.Connections["local:paper"] = sel
			return nil
		})
		if err != nil || state.Selections.Revision != 3 || state.Selections.Connections["local:paper"].Enabled {
			t.Fatal(state, err)
		}
		if _, err := os.Lstat(filepath.Join(p.ConfigDir, ".mcparcel.lock")); err != nil {
			t.Fatal("desktop update takes the lock", err)
		}
	}
	// An invalid config.json fails before the lock, as reading it would.
	p := storePaths(t)
	seedStore(t, p, 1, false)
	testWrite(t, p.ConfigFile, []byte(`{"schemaVersion":1,"runtime":{"mode":"server"}}`), 0o644)
	if _, err := NewStore(p).Update(context.Background(), 1, func(*State) error { return nil }); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
