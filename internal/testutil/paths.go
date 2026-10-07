package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func IsolatedPaths(t testing.TB) (config.Paths, []string) {
	t.Helper()
	// TempRoot avoids Darwin's long per-user temporary directory and symlink alias.
	root, err := os.MkdirTemp(TempRoot(), "mcp-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	env := map[string]string{
		"HOME": filepath.Join(root, "home"), "TMPDIR": filepath.Join(root, "tmp"), "SHELL": "/bin/sh",
		"XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_STATE_HOME": filepath.Join(root, "state"), "MCPARCEL_RUNTIME_DIR": filepath.Join(root, "run"),
	}
	p, err := config.ResolvePaths(func(k string) string { return env[k] }, env["HOME"], root, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p.Home, env["TMPDIR"], p.ConfigDir, p.DataDir, p.CacheDir, p.StateDir, p.RuntimeDir} {
		dir, err := config.OpenPrivateDir(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if err = dir.Close(); err != nil {
			t.Fatal(err)
		}
	}
	list := make([]string, 0, 11)
	for _, name := range []string{"HOME", "TMPDIR", "SHELL", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "MCPARCEL_RUNTIME_DIR"} {
		list = append(list, name+"="+env[name])
	}
	list = append(list, "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C")
	return p, list
}
