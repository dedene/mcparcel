package config

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func storePaths(t *testing.T) Paths {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "mcp-store-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	env := map[string]string{"XDG_CONFIG_HOME": root + "/config", "XDG_DATA_HOME": root + "/data", "XDG_STATE_HOME": root + "/state", "XDG_CACHE_HOME": root + "/cache", "MCPARCEL_RUNTIME_DIR": root + "/run"}
	p, err := ResolvePaths(func(k string) string { return env[k] }, root+"/home", root, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func testBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestStoreAtomicWrite(t *testing.T) {
	p := storePaths(t)
	testWrite(t, p.PersonalFile, []byte(`{"old":true}`), 0o600)
	sentinel := errors.New("before")
	renamed, err := atomicReplace(context.Background(), p.PersonalFile, []byte(`{"new":true}`), atomicHooks{BeforeRename: func() error { return sentinel }})
	if renamed || !errors.Is(err, sentinel) || string(testBytes(t, p.PersonalFile)) != `{"old":true}` {
		t.Fatal(renamed, err)
	}
	entries, e := os.ReadDir(p.ConfigDir)
	if e != nil || len(entries) != 1 {
		t.Fatal(entries, e)
	}
}

func TestAtomicCrashBeforeRename(t *testing.T) {
	if path := os.Getenv("MCP_ATOMIC_CRASH"); path != "" {
		_, _ = atomicReplace(context.Background(), path, []byte(`{"new":true}`), atomicHooks{BeforeRename: func() error { os.Exit(23); return nil }})
		os.Exit(24)
	}
	p := storePaths(t)
	testWrite(t, p.PersonalFile, []byte(`{"schemaVersion":1,"connections":{}}`), 0o600)
	cmd := exec.Command(os.Args[0], "-test.run=^TestAtomicCrashBeforeRename$")
	cmd.Env = append(storeChildEnv(p), "MCP_ATOMIC_CRASH="+p.PersonalFile)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := DecodeCatalog(testBytes(t, p.PersonalFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadState(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(p.ConfigDir)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
}

func TestAtomicAfterRenameFailure(t *testing.T) {
	p := storePaths(t)
	testWrite(t, p.PersonalFile, []byte("old"), 0o600)
	sentinel := errors.New("after")
	renamed, err := atomicReplace(context.Background(), p.PersonalFile, []byte("new"), atomicHooks{AfterRename: func() error { return sentinel }})
	if !renamed || !errors.Is(err, ErrDurability) || !errors.Is(err, sentinel) || string(testBytes(t, p.PersonalFile)) != "new" {
		t.Fatal(renamed, err)
	}
}

func TestAtomicConfigPermissions(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "writable", "dangling", "fifo", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			p := storePaths(t)
			path := p.PersonalFile
			testWrite(t, path, []byte("old"), 0o644)
			switch kind {
			case "symlink":
				path = p.ConfigDir + "/alias"
				if err := os.Symlink(p.PersonalFile, path); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(path, 0o664); err != nil {
					t.Fatal(err)
				}
			case "dangling":
				path = p.ConfigDir + "/alias"
				if err := os.Symlink(p.ConfigDir+"/absent", path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				path = p.ConfigDir + "/fifo"
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, p.ConfigDir+"/linked"); err != nil {
					t.Fatal(err)
				}
			}
			renamed, err := atomicReplace(context.Background(), path, []byte("new"), atomicHooks{})
			if kind == "regular" || kind == "symlink" {
				if !renamed || err != nil {
					t.Fatal(renamed, err)
				}
				st, _ := os.Stat(p.PersonalFile)
				if st.Mode().Perm() != 0o644 || string(testBytes(t, p.PersonalFile)) != "new" {
					t.Fatal(st)
				}
				if kind == "symlink" {
					st, _ := os.Lstat(path)
					if st.Mode()&os.ModeSymlink == 0 {
						t.Fatal(st)
					}
				}
			} else if renamed || !errors.Is(err, ErrUnsafePath) || string(testBytes(t, p.PersonalFile)) != "old" {
				t.Fatal(renamed, err)
			}
		})
	}
}

func TestAtomicModes(t *testing.T) {
	for _, mode := range []os.FileMode{0o700, 0o755, 0o775} {
		t.Run(mode.String(), func(t *testing.T) {
			p := storePaths(t)
			if err := os.MkdirAll(p.ConfigDir, mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p.ConfigDir, mode); err != nil {
				t.Fatal(err)
			}
			renamed, err := atomicReplace(context.Background(), p.PersonalFile, []byte("new"), atomicHooks{})
			if mode == 0o775 {
				if renamed || !errors.Is(err, ErrUnsafePath) {
					t.Fatal(renamed, err)
				}
				return
			}
			if !renamed || err != nil {
				t.Fatal(renamed, err)
			}
			st, _ := os.Stat(p.PersonalFile)
			if st.Mode().Perm() != 0o600 {
				t.Fatal(st)
			}
			st, _ = os.Stat(p.ConfigDir)
			if st.Mode().Perm() != mode {
				t.Fatal(st)
			}
		})
	}
	p := storePaths(t)
	if _, err := atomicReplace(context.Background(), p.PersonalFile, nil, atomicHooks{}); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicCancellationAndTargetRaces(t *testing.T) {
	for _, kind := range []string{"before-cancel", "after-cancel", "replace", "appear", "unsafe-file", "unsafe-parent"} {
		t.Run(kind, func(t *testing.T) {
			p := storePaths(t)
			testWrite(t, p.PersonalFile, []byte("old"), 0o600)
			path := p.PersonalFile
			if kind == "appear" {
				path = p.ConfigDir + "/absent"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hooks := atomicHooks{BeforeRename: func() error {
				switch kind {
				case "before-cancel":
					cancel()
				case "replace":
					other := p.ConfigDir + "/replacement"
					testWrite(t, other, []byte("editor"), 0o600)
					return os.Rename(other, path)
				case "appear":
					testWrite(t, path, []byte("editor"), 0o600)
				case "unsafe-file":
					return os.Chmod(path, 0o664)
				case "unsafe-parent":
					return os.Chmod(p.ConfigDir, 0o775)
				}
				return nil
			}, AfterRename: func() error {
				if kind == "after-cancel" {
					cancel()
				}
				return nil
			}}
			renamed, e := atomicReplace(ctx, path, []byte("new"), hooks)
			switch kind {
			case "before-cancel":
				if renamed || !errors.Is(e, context.Canceled) || string(testBytes(t, path)) != "old" {
					t.Fatal(renamed, e)
				}
			case "after-cancel":
				if !renamed || e != nil || string(testBytes(t, path)) != "new" {
					t.Fatal(renamed, e)
				}
			case "replace", "appear":
				if renamed || !errors.Is(e, ErrConfigConflict) || string(testBytes(t, path)) != "editor" {
					t.Fatal(renamed, e)
				}
			default:
				if renamed || !errors.Is(e, ErrUnsafePath) || string(testBytes(t, path)) != "old" {
					t.Fatal(renamed, e)
				}
			}
		})
	}
}
