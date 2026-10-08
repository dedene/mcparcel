package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestTokenFileRule(t *testing.T) {
	uid := 501
	never := func() bool { t.Fatal("read-only check ran for a file without group bits"); return false }
	for _, c := range []struct {
		owner    uint32
		mode     uint32
		readOnly bool
		ok       bool
	}{
		{uint32(uid), unix.S_IFREG | 0o600, false, true},
		{uint32(uid), unix.S_IFREG | 0o400, false, true},
		{0, unix.S_IFREG | 0o600, false, true},
		// A Kubernetes Secret with defaultMode 0440 and fsGroup.
		{0, unix.S_IFREG | 0o440, true, true},
		{uint32(uid), unix.S_IFREG | 0o660, true, true},
		{0, unix.S_IFREG | 0o440, false, false},
		{uint32(uid), unix.S_IFREG | 0o640, false, false},
		{uint32(uid), unix.S_IFREG | 0o620, false, false},
		{uint32(uid), unix.S_IFREG | 0o644, false, false},
		// A read-only mount never makes an other-readable token private.
		{0, unix.S_IFREG | 0o444, true, false},
		{0, unix.S_IFREG | 0o644, true, false},
		{uint32(uid), unix.S_IFREG | 0o604, true, false},
		{uint32(uid), unix.S_IFREG | 0o602, false, false},
		{1000, unix.S_IFREG | 0o600, false, false},
		{uint32(uid), unix.S_IFDIR | 0o700, false, false},
		{uint32(uid), unix.S_IFIFO | 0o600, false, false},
	} {
		var st unix.Stat_t
		st.Uid = c.owner
		setMode(&st, c.mode)
		readOnly := func() bool { return c.readOnly }
		if c.mode&0o060 == 0 {
			readOnly = never
		}
		if got := tokenFileOK(&st, uid, readOnly); got != c.ok {
			t.Fatalf("owner %d mode %o read-only %t: got %t", c.owner, c.mode, c.readOnly, got)
		}
	}
}

// writeToken (re)writes a token file at path and gives it mode.
func writeToken(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	testWrite(t, path, []byte("ops_fixture\n"), 0o600)
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestOpenTokenFile(t *testing.T) {
	withReadOnlyMount(t, false)
	dir := filepath.Join(secureRoot(t), "secrets")
	mkdirMode(t, dir, 0o700)
	file := filepath.Join(dir, "op-token")
	for _, mode := range []os.FileMode{0o600, 0o400} {
		writeToken(t, file, mode)
		f, err := OpenTokenFile(file)
		openOK(t, f, err)
	}
	for _, mode := range []os.FileMode{0o640, 0o644, 0o620, 0o604} {
		writeToken(t, file, mode)
		f, err := OpenTokenFile(file)
		openUnsafe(t, f, err)
	}
	// A symbolic link to an unsafe file is unsafe; to a private one it opens.
	if err := os.Symlink("op-token", dir+"/link"); err != nil {
		t.Fatal(err)
	}
	f, err := OpenTokenFile(dir + "/link")
	openUnsafe(t, f, err)
	writeToken(t, file, 0o600)
	f, err = OpenTokenFile(dir + "/link")
	openOK(t, f, err)
	// A directory and a FIFO are unsafe, and the FIFO open never blocks.
	mkdirMode(t, dir+"/sub", 0o700)
	f, err = OpenTokenFile(dir + "/sub")
	openUnsafe(t, f, err)
	if err := unix.Mkfifo(dir+"/fifo", 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := OpenTokenFile(dir + "/fifo")
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO blocked")
	}
	// A writable directory on the way is unsafe as for config files.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	f, err = OpenTokenFile(file)
	openUnsafe(t, f, err)
}

// TestTokenFileSecretVolume mirrors a Kubernetes Secret volume: op-token ->
// ..data/op-token, ..data -> ..2026_10_08/, mounted read-only with
// defaultMode 0440.
func TestTokenFileSecretVolume(t *testing.T) {
	withReadOnlyMount(t, false)
	dir := filepath.Join(secureRoot(t), "secrets")
	mkdirMode(t, dir, 0o755)
	mkdirMode(t, dir+"/..2026_10_08", 0o755)
	writeToken(t, dir+"/..2026_10_08/op-token", 0o600)
	if err := os.Symlink("..2026_10_08", dir+"/..data"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data/op-token", dir+"/op-token"); err != nil {
		t.Fatal(err)
	}
	f, err := OpenTokenFile(dir + "/op-token")
	if err != nil {
		t.Fatal(err)
	}
	if f.Name() != dir+"/..2026_10_08/op-token" {
		t.Fatal(f.Name())
	}
	openOK(t, f, nil)
	if err := os.Chmod(dir+"/..2026_10_08/op-token", 0o440); err != nil {
		t.Fatal(err)
	}
	f, err = OpenTokenFile(dir + "/op-token")
	openUnsafe(t, f, err)
	withReadOnlyMount(t, true)
	f, err = OpenTokenFile(dir + "/op-token")
	openOK(t, f, err)
	// Kubernetes' default 0644 stays unsafe on the read-only mount.
	if err := os.Chmod(dir+"/..2026_10_08/op-token", 0o644); err != nil {
		t.Fatal(err)
	}
	f, err = OpenTokenFile(dir + "/op-token")
	openUnsafe(t, f, err)
}

// A missing file and a dangling link are not-exist, a file the user may not
// read is permission denied: neither is unsafe. OpenConfigFile keeps
// reporting both as unsafe.
func TestTokenFileUnavailable(t *testing.T) {
	withReadOnlyMount(t, false)
	dir := filepath.Join(secureRoot(t), "secrets")
	mkdirMode(t, dir, 0o700)
	if _, err := OpenTokenFile(dir + "/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing:", err)
	}
	if err := os.Symlink("missing", dir+"/dangling"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTokenFile(dir + "/dangling"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dangling:", err)
	}
	if _, err := OpenConfigFile(dir + "/dangling"); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("dangling config:", err)
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores file modes")
	}
	writeToken(t, dir+"/sealed", 0o000)
	if _, err := OpenTokenFile(dir + "/sealed"); !errors.Is(err, os.ErrPermission) || errors.Is(err, ErrUnsafePath) {
		t.Fatal("unreadable:", err)
	}
	if _, err := OpenConfigFile(dir + "/sealed"); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("unreadable config:", err)
	}
	mkdirMode(t, dir+"/closed", 0o700)
	writeToken(t, dir+"/closed/op-token", 0o600)
	if err := os.Chmod(dir+"/closed", 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir+"/closed", 0o700) })
	if _, err := OpenTokenFile(dir + "/closed/op-token"); !errors.Is(err, os.ErrPermission) {
		t.Fatal("unsearchable directory:", err)
	}
	// A real root-owned private file, where the system has one.
	for _, path := range []string{"/etc/master.passwd", "/etc/shadow", "/etc/sudoers"} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || unix.Access(path, unix.R_OK) == nil {
			continue
		}
		if _, err := OpenTokenFile(path); !errors.Is(err, os.ErrPermission) {
			t.Fatal(path, err)
		}
		break
	}
}

func TestCheckTokenFile(t *testing.T) {
	withReadOnlyMount(t, false)
	dir := filepath.Join(secureRoot(t), "secrets")
	mkdirMode(t, dir, 0o700)
	file := filepath.Join(dir, "op-token")
	writeToken(t, file, 0o600)
	if err := CheckTokenFile(file); err != nil {
		t.Fatal(err)
	}
	testWrite(t, file, nil, 0o600)
	if err := CheckTokenFile(file); !errors.Is(err, ErrTokenEmpty) {
		t.Fatal("empty:", err)
	}
	writeToken(t, file, 0o644)
	if err := CheckTokenFile(file); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("unsafe:", err)
	}
	if err := CheckTokenFile(dir + "/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing:", err)
	}
}
