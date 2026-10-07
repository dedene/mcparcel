package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func envMap(list []string) map[string]string {
	out := map[string]string{}
	for _, kv := range list {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

func headlessPaths(t *testing.T) config.Paths {
	t.Helper()
	p, _ := testutil.IsolatedPaths(t)
	root := filepath.Join(filepath.Dir(p.Home), "state-root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// kubelet creates an emptyDir world-writable without the sticky bit.
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	h, err := config.ApplyStateRoot(p, root)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestDaemonEnvironmentHeadlessOmitsStateDirs(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	desktop := envMap(DaemonEnvironment(p))
	for k, want := range map[string]string{"XDG_CONFIG_HOME": filepath.Dir(p.ConfigDir), "XDG_DATA_HOME": filepath.Dir(p.DataDir), "XDG_CACHE_HOME": filepath.Dir(p.CacheDir), "XDG_STATE_HOME": filepath.Dir(p.StateDir), "MCPARCEL_RUNTIME_DIR": p.RuntimeDir, "HOME": p.Home} {
		if desktop[k] != want {
			t.Fatal(k, desktop[k], want)
		}
	}
	h := headlessPaths(t)
	headless := envMap(DaemonEnvironment(h))
	if headless["XDG_CONFIG_HOME"] != filepath.Dir(h.ConfigDir) || headless["HOME"] != h.Home {
		t.Fatal(headless)
	}
	for _, k := range []string{"XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "MCPARCEL_RUNTIME_DIR"} {
		if v, ok := headless[k]; ok {
			t.Fatalf("headless daemon environment carries %s=%s; the state root comes from config", k, v)
		}
	}
}

func TestHeadlessStateRootWorldWritable(t *testing.T) {
	p := headlessPaths(t)
	if held, err := lockHeld(p); err != nil || held {
		t.Fatal(held, err)
	}
	log, err := OpenLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = log.Close(); err != nil {
		t.Fatal(err)
	}
	dir, err := config.OpenPrivateDirUnder(p.StateRoot, p.RuntimeDir, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = dir.Close()
	if held, err := lockHeld(p); err != nil || held {
		t.Fatal(held, err)
	}
	for _, dir := range []string{p.StateDir, p.RuntimeDir} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatal(dir, info, err)
		}
	}
	// Without the trusted root, the same layout is refused.
	p.StateRoot = ""
	if _, err := OpenLog(p); err == nil {
		t.Fatal("world-writable state root accepted without trust")
	}
}
