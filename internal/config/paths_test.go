package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestPathsOverrides(t *testing.T) {
	root, err := os.MkdirTemp(testutil.TempRoot(), "mcp-path-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	env := map[string]string{"XDG_CONFIG_HOME": root + "/config", "XDG_DATA_HOME": root + "/data", "XDG_CACHE_HOME": root + "/cache", "XDG_STATE_HOME": root + "/state", "MCPARCEL_RUNTIME_DIR": root + "/run"}
	p, err := config.ResolvePaths(func(k string) string { return env[k] }, root+"/home", root, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	want := config.Paths{Home: root + "/home", ConfigDir: root + "/config/mcparcel", DataDir: root + "/data/mcparcel", CacheDir: root + "/cache/mcparcel", StateDir: root + "/state/mcparcel", RuntimeDir: root + "/run", PersonalFile: root + "/config/mcparcel/personal.json", ConfigFile: root + "/config/mcparcel/config.json", SelectionsFile: root + "/config/mcparcel/selections.json", SocketFile: root + "/run/daemon.sock", LockFile: root + "/run/daemon.lock", LogFile: root + "/state/mcparcel/daemon.log"}
	if p != want {
		t.Fatalf("%#v", p)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	defaults, err := config.ResolvePaths(func(string) string { return "" }, root+"/home", root, 42)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.ConfigDir != root+"/home/.config/mcparcel" || defaults.DataDir != root+"/home/.local/share/mcparcel" || defaults.CacheDir != root+"/home/.cache/mcparcel" || defaults.StateDir != root+"/home/.local/state/mcparcel" || defaults.RuntimeDir != root+"/mcp-42" {
		t.Fatal(defaults)
	}
}

func TestLongSocketPath(t *testing.T) {
	runtime := "/" + strings.Repeat("a", 88)
	if len(runtime+"/daemon.sock") != 101 {
		t.Fatal("bad test")
	}
	_, err := config.ResolvePaths(func(k string) string {
		if k == "MCPARCEL_RUNTIME_DIR" {
			return runtime
		}
		return ""
	}, "/fixture", testutil.TempRoot(), 1)
	if !errors.Is(err, config.ErrUnsafePath) {
		t.Fatal(err)
	}
}

func TestPrivatePathModes(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	bad := filepath.Join(p.Home, "bad")
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if f, err := config.OpenPrivateDir(bad, false); !errors.Is(err, config.ErrUnsafePath) {
		if f != nil {
			f.Close()
		}
		t.Fatal(err)
	}
	dir, err := config.OpenPrivateDir(p.RuntimeDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	file := filepath.Join(p.RuntimeDir, "unsafe")
	writeFile(t, file, "private", 0o644)
	if f, err := config.OpenPrivateFile(dir, "unsafe", false); !errors.Is(err, config.ErrUnsafePath) {
		if f != nil {
			f.Close()
		}
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(p.RuntimeDir, "good"), "private", 0o600)
	if err := os.Symlink(filepath.Join(p.RuntimeDir, "good"), filepath.Join(p.RuntimeDir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(p.RuntimeDir, "good"), filepath.Join(p.RuntimeDir, "hard")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(p.RuntimeDir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link", "hard", "fifo", "../good", "."} {
		if f, err := config.OpenPrivateFile(dir, name, false); !errors.Is(err, config.ErrUnsafePath) {
			if f != nil {
				f.Close()
			}
			t.Fatal(name, err)
		}
	}
	link := filepath.Join(p.Home, "linked-runtime")
	if err := os.Symlink(p.RuntimeDir, link); err != nil {
		t.Fatal(err)
	}
	if f, err := config.OpenPrivateDir(link+"/child", true); !errors.Is(err, config.ErrUnsafePath) {
		if f != nil {
			f.Close()
		}
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.RuntimeDir, "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if f, err := config.OpenPrivateFile(dir, "new", true); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
}

func TestConfigFileRules(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	file := filepath.Join(p.ConfigDir, "regular.json")
	for _, tc := range []struct {
		mode os.FileMode
		ok   bool
	}{{0o644, true}, {0o664, false}, {0o666, false}} {
		writeFile(t, file, "{}", tc.mode)
		f, err := config.OpenConfigFile(file)
		if f != nil {
			f.Close()
		}
		if (err == nil) != tc.ok || (!tc.ok && !errors.Is(err, config.ErrUnsafePath)) {
			t.Fatal(tc, err)
		}
	}
	link := filepath.Join(p.ConfigDir, "link.json")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode os.FileMode
		ok   bool
	}{{0o644, true}, {0o664, false}} {
		if err := os.Chmod(file, tc.mode); err != nil {
			t.Fatal(err)
		}
		f, err := config.OpenConfigFile(link)
		if f != nil {
			f.Close()
		}
		if (err == nil) != tc.ok {
			t.Fatal(tc, err)
		}
	}
	fifo := filepath.Join(p.ConfigDir, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if f, err := config.OpenConfigFile(fifo); !errors.Is(err, config.ErrUnsafePath) {
		if f != nil {
			f.Close()
		}
		t.Fatal(err)
	}
	writeFile(t, file, "{}", 0o644)
	for _, tc := range []struct {
		mode os.FileMode
		ok   bool
	}{{0o755, true}, {0o775, false}} {
		if err := os.Chmod(p.ConfigDir, tc.mode); err != nil {
			t.Fatal(err)
		}
		f, err := config.OpenConfigFile(file)
		if f != nil {
			f.Close()
		}
		if (err == nil) != tc.ok {
			t.Fatal(tc, err)
		}
	}
}

func TestMissingDirectories(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	p.ConfigDir = filepath.Join(p.Home, "absent")
	p.PersonalFile = filepath.Join(p.ConfigDir, "personal.json")
	p.ConfigFile = filepath.Join(p.ConfigDir, "config.json")
	if _, err := config.Load(p); !errors.Is(err, config.ErrConfigRequired) {
		t.Fatal(err)
	}
	if f, err := config.OpenPrivateDir(filepath.Join(p.Home, "absent-runtime"), false); !errors.Is(err, os.ErrNotExist) {
		if f != nil {
			f.Close()
		}
		t.Fatal(err)
	}
	entries, err := os.ReadDir(p.Home)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func writeFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func symlinkRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(testutil.TempRoot(), "mcp-path-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	if err := os.Mkdir(root+"/real", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root+"/real", root+"/link"); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolvePathsResolvesSymlinks(t *testing.T) {
	root := symlinkRoot(t)
	env := map[string]string{"MCPARCEL_RUNTIME_DIR": root + "/link/run", "XDG_STATE_HOME": root + "/link/state"}
	p, err := config.ResolvePaths(func(k string) string { return env[k] }, root+"/home", testutil.TempRoot(), os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	if p.RuntimeDir != root+"/real/run" || p.SocketFile != root+"/real/run/daemon.sock" || p.LockFile != root+"/real/run/daemon.lock" || p.StateDir != root+"/real/state/mcparcel" || p.LogFile != root+"/real/state/mcparcel/daemon.log" {
		t.Fatalf("%#v", p)
	}
	if _, err := os.Stat(root + "/real/run"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	// $TMPDIR ends in "/", so MCPARCEL_RUNTIME_DIR=$TMPDIR/x is not clean.
	for _, runtime := range []string{root + "/link//run", root + "/link/run/"} {
		env["MCPARCEL_RUNTIME_DIR"] = runtime
		got, err := config.ResolvePaths(func(k string) string { return env[k] }, root+"/home", testutil.TempRoot(), os.Getuid())
		if err != nil || got.RuntimeDir != root+"/real/run" {
			t.Fatal(runtime, got.RuntimeDir, err)
		}
	}
	dir, err := config.OpenPrivateDir(p.RuntimeDir, true)
	if err != nil {
		t.Fatal(err)
	}
	dir.Close()
	if info, err := os.Stat(root + "/real/run"); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal(info, err)
	}
	if f, err := config.OpenPrivateDir(root+"/link/run2", true); !errors.Is(err, config.ErrUnsafePath) {
		if f != nil {
			f.Close()
		}
		t.Fatal(err)
	}
}

func TestResolvePathsSymlinkFailsClosed(t *testing.T) {
	root := symlinkRoot(t)
	resolve := func(runtime string) (config.Paths, error) {
		return config.ResolvePaths(func(k string) string {
			if k == "MCPARCEL_RUNTIME_DIR" {
				return runtime
			}
			return ""
		}, root+"/home", testutil.TempRoot(), os.Getuid())
	}
	if err := os.Symlink(root+"/missing", root+"/dangling"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(root + "/dangling/run"); !errors.Is(err, config.ErrUnsafePath) {
		t.Fatal("dangling ancestor:", err)
	}
	long := root + "/" + strings.Repeat("a", 70)
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(long, root+"/s"); err != nil {
		t.Fatal(err)
	}
	if len(root+"/s/run/daemon.sock") > 100 || len(long+"/run/daemon.sock") <= 100 {
		t.Fatal("bad test")
	}
	if _, err := resolve(root + "/s/run"); !errors.Is(err, config.ErrUnsafePath) {
		t.Fatal("long resolved socket:", err)
	}
	// The leaf is never followed: a planted /tmp/mcp-<uid> symlink stays refused.
	p, err := resolve(root + "/link")
	if err != nil || p.RuntimeDir != root+"/link" {
		t.Fatal(p.RuntimeDir, err)
	}
	if f, err := config.OpenPrivateDir(p.RuntimeDir, true); !errors.Is(err, config.ErrUnsafePath) {
		if f != nil {
			f.Close()
		}
		t.Fatal(err)
	}
}
