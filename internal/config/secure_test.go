package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

// secureRoot returns a canonical private directory for path-walk tests.
func secureRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(DefaultTempDir(), "mcp-secure-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func mkdirMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// setMode fills Stat_t.Mode, which is uint16 on darwin and uint32 on linux.
func setMode(st *unix.Stat_t, mode uint32) { reflect.ValueOf(&st.Mode).Elem().SetUint(uint64(mode)) }

func withReadOnlyMount(t *testing.T, readOnly bool) {
	t.Helper()
	saved := isReadOnlyMount
	isReadOnlyMount = func(int) bool { return readOnly }
	t.Cleanup(func() { isReadOnlyMount = saved })
}

func openOK(t *testing.T, f *os.File, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func openUnsafe(t *testing.T, f *os.File, err error) {
	t.Helper()
	if f != nil {
		_ = f.Close()
	}
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatal(err)
	}
}

func TestStateRootWorldWritableAccepted(t *testing.T) {
	root := filepath.Join(secureRoot(t), "state-root")
	mkdirMode(t, root, 0o777)
	run := filepath.Join(root, "run")
	f, err := OpenPrivateDir(run, true)
	openUnsafe(t, f, err)
	if _, err := os.Stat(run); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("OpenPrivateDir created below an untrusted world-writable root", err)
	}
	f, err = OpenPrivateDirUnder(root, run, true)
	openOK(t, f, err)
	info, err := os.Stat(run)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal(info, err)
	}
	f, err = OpenPrivateDirUnder(root, run, false)
	openOK(t, f, err)
	// The root itself is never the opened directory, and a path outside it is refused.
	f, err = OpenPrivateDirUnder(root, root, false)
	openUnsafe(t, f, err)
	f, err = OpenPrivateDirUnder(root, filepath.Dir(root), false)
	openUnsafe(t, f, err)
	f, err = OpenPrivateDirUnder(root+"/", run, false)
	openUnsafe(t, f, err)
	// The "" root keeps OpenPrivateDir's behaviour.
	f, err = OpenPrivateDirUnder("", run, false)
	openUnsafe(t, f, err)
	// The directory below the root still has to be private.
	if err := os.Chmod(run, 0o750); err != nil {
		t.Fatal(err)
	}
	f, err = OpenPrivateDirUnder(root, run, false)
	openUnsafe(t, f, err)
}

func TestWorldWritableAncestorStillRejectedBelowRoot(t *testing.T) {
	base := secureRoot(t)
	root := filepath.Join(base, "state-root")
	mkdirMode(t, root, 0o777)
	mkdirMode(t, root+"/shared", 0o777)
	f, err := OpenPrivateDirUnder(root, root+"/shared/run", true)
	openUnsafe(t, f, err)
	if _, err := os.Stat(root + "/shared/run"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	// A world-writable directory above the root is not trusted either.
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	mkdirMode(t, root+"/inner", 0o777)
	if err := os.Chmod(base, 0o777); err != nil {
		t.Fatal(err)
	}
	f, err = OpenPrivateDirUnder(root+"/inner", root+"/inner/run", true)
	openUnsafe(t, f, err)
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err = OpenPrivateDirUnder(root+"/inner", root+"/inner/run", true)
	openOK(t, f, err)
}

func TestConfigFileOwnedByRootAccepted(t *testing.T) {
	uid := 501
	never := func() bool { t.Fatal("read-only check ran for a non-writable file"); return false }
	for _, c := range []struct {
		owner uint32
		mode  uint32
		ok    bool
	}{
		{uint32(uid), unix.S_IFREG | 0o644, true},
		{0, unix.S_IFREG | 0o644, true},
		{0, unix.S_IFREG | 0o444, true},
		{1000, unix.S_IFREG | 0o644, false},
		{0, unix.S_IFDIR | 0o755, false},
	} {
		var st unix.Stat_t
		st.Uid = c.owner
		setMode(&st, c.mode)
		if got := configFileOK(&st, uid, never); got != c.ok {
			t.Fatal(c, got)
		}
	}
	for _, c := range []struct {
		owner uint32
		mode  uint32
		ok    bool
	}{
		{0, unix.S_IFDIR | 0o755, true},
		{uint32(uid), unix.S_IFDIR | 0o700, true},
		{0, unix.S_IFDIR | 0o1777, true},
		{1000, unix.S_IFDIR | 0o755, false},
	} {
		var st unix.Stat_t
		st.Uid = c.owner
		setMode(&st, c.mode)
		if got := configDirOK(&st, uid, never); got != c.ok {
			t.Fatal(c, got)
		}
	}
}

func TestConfigFileGroupWritableRejected(t *testing.T) {
	withReadOnlyMount(t, false)
	dir := filepath.Join(secureRoot(t), "config")
	mkdirMode(t, dir, 0o755)
	file := filepath.Join(dir, "config.json")
	for _, c := range []struct {
		mode os.FileMode
		ok   bool
	}{{0o644, true}, {0o600, true}, {0o664, false}, {0o646, false}, {0o666, false}} {
		testWrite(t, file, []byte("{}"), c.mode)
		if err := os.Chmod(file, c.mode); err != nil {
			t.Fatal(err)
		}
		f, err := OpenConfigFile(file)
		if c.ok {
			openOK(t, f, err)
		} else {
			openUnsafe(t, f, err)
		}
	}
	for _, mode := range []os.FileMode{0o775, 0o757, 0o777} {
		if err := os.Chmod(file, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		f, err := OpenConfigFile(file)
		openUnsafe(t, f, err)
	}
	var st unix.Stat_t
	setMode(&st, unix.S_IFREG|0o664)
	if configFileOK(&st, 0, func() bool { return false }) {
		t.Fatal("group-writable root file accepted on a writable mount")
	}
}

func TestConfigOnReadOnlyMountAccepted(t *testing.T) {
	dir := filepath.Join(secureRoot(t), "mount")
	mkdirMode(t, dir, 0o700)
	file := filepath.Join(dir, "config.json")
	testWrite(t, file, []byte("{}"), 0o666)
	if err := os.Chmod(file, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	withReadOnlyMount(t, false)
	f, err := OpenConfigFile(file)
	openUnsafe(t, f, err)
	withReadOnlyMount(t, true)
	f, err = OpenConfigFile(file)
	openOK(t, f, err)
	// The real check sees the test's writable temporary directory as writable.
	isReadOnlyMount = readOnlyMount
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if readOnlyMount(fd) {
		t.Fatal("temporary directory reported as a read-only mount")
	}
	if readOnlyMount(-1) {
		t.Fatal("a failed statfs must not count as read-only")
	}
}

// TestConfigMapSymlinkLayout mirrors a Kubernetes ConfigMap volume:
// config.json -> ..data/config.json, ..data -> ..2026_10_07/.
func TestConfigMapSymlinkLayout(t *testing.T) {
	withReadOnlyMount(t, false)
	dir := filepath.Join(secureRoot(t), "mcparcel")
	mkdirMode(t, dir, 0o755)
	mkdirMode(t, dir+"/..2026_10_07", 0o755)
	for _, name := range []string{"config.json", "personal.json", "selections.json"} {
		testWrite(t, dir+"/..2026_10_07/"+name, []byte(`{"schemaVersion":1}`), 0o644)
	}
	if err := os.Symlink("..2026_10_07", dir+"/..data"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "personal.json", "selections.json"} {
		if err := os.Symlink("..data/"+name, dir+"/"+name); err != nil {
			t.Fatal(err)
		}
		f, err := OpenConfigFile(dir + "/" + name)
		if err != nil {
			t.Fatal(name, err)
		}
		if f.Name() != dir+"/..2026_10_07/"+name {
			t.Fatal(f.Name())
		}
		openOK(t, f, nil)
	}
	p := Paths{ConfigDir: dir, ConfigFile: dir + "/config.json", PersonalFile: dir + "/personal.json", SelectionsFile: dir + "/selections.json"}
	if rt, err := ReadRuntime(t.Context(), p); err != nil || rt != (RuntimeDefaults{}) {
		t.Fatal(rt, err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	f, err := OpenConfigFile(dir + "/config.json")
	openUnsafe(t, f, err)
	withReadOnlyMount(t, true)
	f, err = OpenConfigFile(dir + "/config.json")
	openOK(t, f, err)
}
