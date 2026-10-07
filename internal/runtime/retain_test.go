package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// fakeExe writes an "executable" with body into the isolated home.
func fakeExe(t *testing.T, p config.Paths, name, body string) string {
	t.Helper()
	path := filepath.Join(p.Home, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// retain is retainExecutable that releases retain.lock at once.
func retain(t *testing.T, p config.Paths, version, exe string) (string, error) {
	t.Helper()
	got, release, err := retainExecutable(testCtx(t), p, version, exe)
	if err == nil {
		release()
	}
	return got, err
}

func inode(t *testing.T, path string) (uint64, time.Time) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino, info.ModTime()
}

func TestRetainExecutableCopiesOnce(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	exe := fakeExe(t, p, "npx cache mcparcel", "#!/bin/sh\necho one\n")
	got, err := retain(t, p, "0.1.0-rc.1+abc", exe)
	want := filepath.Join(p.DataDir, "runtime", "0.1.0-rc.1+abc", "mcparcel")
	if err != nil || got != want {
		t.Fatal(got, err)
	}
	info, err := os.Lstat(got)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o700 {
		t.Fatal(info, err)
	}
	if b, _ := os.ReadFile(got); string(b) != "#!/bin/sh\necho one\n" {
		t.Fatalf("copy %q", b)
	}
	ino, mtime := inode(t, got)
	if again, err := retain(t, p, "0.1.0-rc.1+abc", exe); err != nil || again != want {
		t.Fatal(again, err)
	}
	if ino2, mtime2 := inode(t, got); ino2 != ino || !mtime2.Equal(mtime) {
		t.Fatal("an identical copy was replaced")
	}
	entries, _ := os.ReadDir(filepath.Dir(got))
	if len(entries) != 1 {
		t.Fatal("temporary files left:", entries)
	}
}

func TestRetainExecutableReplacesChangedBytes(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	exe := fakeExe(t, p, "mcparcel", "old bytes\n")
	got, err := retain(t, p, "dev", exe)
	if err != nil {
		t.Fatal(err)
	}
	running, err := os.Open(got) // what a running daemon executes
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	ino, _ := inode(t, got)
	if err = os.WriteFile(exe, []byte("new bytes, longer\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = retain(t, p, "dev", exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(running); string(b) != "old bytes\n" {
		t.Fatalf("the running copy changed: %q", b)
	}
	if b, _ := os.ReadFile(got); string(b) != "new bytes, longer\n" {
		t.Fatalf("not replaced: %q", b)
	}
	if ino2, _ := inode(t, got); ino2 == ino {
		t.Fatal("replaced in place, not renamed")
	}
	// A copy with another mode is replaced too.
	if err = os.Chmod(got, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = retain(t, p, "dev", exe); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Lstat(got); info.Mode().Perm() != 0o700 {
		t.Fatal(info.Mode())
	}
}

func TestRetainExecutableNoOpWhenAlreadyRetained(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	target, _ := RetainedPath(p, "dev")
	// The daemon's own retained path is returned without being opened.
	if got, err := retain(t, p, "dev", target); err != nil || got != target {
		t.Fatal(got, err)
	}
	if _, err := os.Stat(filepath.Dir(target)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created something:", err)
	}
}

func TestRetainExecutableRejectsUnsafeDir(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	exe := fakeExe(t, p, "mcparcel", "bytes\n")
	dir := filepath.Join(p.DataDir, "runtime", "dev")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := retain(t, p, "dev", exe); !errors.Is(err, config.ErrUnsafePath) {
		t.Fatal("0755 version dir:", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := fakeExe(t, p, "elsewhere", "bytes\n")
	if err := os.Symlink(elsewhere, filepath.Join(dir, "mcparcel")); err != nil {
		t.Fatal(err)
	}
	if _, err := retain(t, p, "dev", exe); !errors.Is(err, config.ErrUnsafePath) {
		t.Fatal("symlinked target:", err)
	}
	if b, _ := os.ReadFile(elsewhere); string(b) != "bytes\n" {
		t.Fatal("followed the symlink")
	}
}

func TestRetainedPathRejectsBadVersion(t *testing.T) {
	p := config.Paths{DataDir: "/data/mcparcel"}
	for _, v := range []string{"", ".", "..", "a/b", "a b", strings.Repeat("a", 129)} {
		if got, err := RetainedPath(p, v); err == nil {
			t.Fatalf("%q accepted: %s", v, got)
		}
	}
	for _, v := range []string{"0.1.0-rc.1+abc", "dev", "5319954-dirty", strings.Repeat("a", 128)} {
		if got, err := RetainedPath(p, v); err != nil || got != "/data/mcparcel/runtime/"+v+"/mcparcel" {
			t.Fatal(v, got, err)
		}
	}
}

func TestRetainPrunesKeepsOneOther(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	runtimeDir := filepath.Join(p.DataDir, "runtime")
	now := time.Now()
	for i, v := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		dir := filepath.Join(runtimeDir, v)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mcparcel"), []byte(v), 0o700); err != nil {
			t.Fatal(err)
		}
		// 0.2.0 is the most recently modified other version.
		at := now.Add(-time.Duration(3-i) * time.Hour)
		if v == "0.2.0" {
			at = now.Add(-time.Minute)
		}
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}
	}
	// A symlinked version directory is neither followed nor removed.
	outside := filepath.Join(p.Home, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "mcparcel"), []byte("keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(runtimeDir, "9.9.9")); err != nil {
		t.Fatal(err)
	}
	if _, err := retain(t, p, "0.4.0", fakeExe(t, p, "mcparcel", "0.4.0")); err != nil {
		t.Fatal(err)
	}
	var names []string
	entries, _ := os.ReadDir(runtimeDir)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "0.2.0 0.4.0 9.9.9 retain.lock" {
		t.Fatal(names)
	}
	if b, err := os.ReadFile(filepath.Join(outside, "mcparcel")); err != nil || string(b) != "keep" {
		t.Fatal("symlink target touched:", err)
	}
}

func TestStartDaemonRetainsUnderLock(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Paths: p, Version: "dev", Executable: exe}
	t.Cleanup(func() { cleanupDaemon(t, c) })
	calls, released := 0, 0
	hook := func(_ context.Context, got string) (string, func(), error) {
		calls++
		if held, err := lockHeld(p); err != nil || !held {
			t.Error("retain ran without the daemon lock:", held, err)
		}
		if got != exe {
			t.Error("retain got", got)
		}
		return exe, func() { released++ }, nil
	}
	if started, err := startDaemon(testCtx(t), p, exe, env, config.OpenPrivateFile, hook); err != nil || !started || calls != 1 || released != 1 {
		t.Fatal(started, err, calls, released)
	}
	waitStatus(t, c)
	// A second starter loses the lock and never copies or prunes.
	lost := func(context.Context, string) (string, func(), error) {
		t.Error("retain ran without the lock")
		return "", nil, nil
	}
	if started, err := startDaemon(testCtx(t), p, exe, env, config.OpenPrivateFile, lost); err != nil || started {
		t.Fatal(started, err)
	}
}

func TestRetainFailureFallsBackToOriginal(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Paths: p, Version: "dev", Executable: exe}
	t.Cleanup(func() { cleanupDaemon(t, c) })
	// An unsafe path fails closed: nothing starts and the lock is free.
	unsafe := func(context.Context, string) (string, func(), error) { return "", nil, config.ErrUnsafePath }
	if started, err := startDaemon(testCtx(t), p, exe, env, config.OpenPrivateFile, unsafe); !errors.Is(err, config.ErrUnsafePath) || started {
		t.Fatal(started, err)
	}
	if held, err := lockHeld(p); err != nil || held {
		t.Fatal("lock still held:", held, err)
	}
	// Any other failure (disk full, read-only data dir) starts the original.
	full := func(context.Context, string) (string, func(), error) {
		return "", nil, errors.New("no space left on device")
	}
	if started, err := startDaemon(testCtx(t), p, exe, env, config.OpenPrivateFile, full); err != nil || !started {
		t.Fatal(started, err)
	}
	if s := waitStatus(t, c); s.Executable != exe {
		t.Fatal("daemon runs from", s.Executable)
	}
}

// Ensure with Retain starts the daemon from its retained copy, and status
// reports that path.
func TestEnsureStartsFromRetainedCopy(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	p, _ := testutil.IsolatedPaths(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Paths: p, Version: "dev", Executable: exe, Retain: true}
	t.Cleanup(func() { cleanupDaemon(t, c) })
	if err = c.Ensure(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	want, _ := RetainedPath(p, "dev")
	if s := waitStatus(t, c); s.Executable != want {
		t.Fatalf("daemon runs from %q, want %q", s.Executable, want)
	}
	a, _ := os.ReadFile(exe)
	b, _ := os.ReadFile(want)
	if !bytes.Equal(a, b) {
		t.Fatal("retained copy differs")
	}
}

func TestEnsureNoRetainHeadless(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	if (&Client{Paths: p, Retain: true}).retainHook() == nil {
		t.Fatal("desktop with Retain has no hook")
	}
	if (&Client{Paths: p}).retainHook() != nil {
		t.Fatal("Retain off still retains")
	}
	headless, err := config.ApplyStateRoot(p, filepath.Join(filepath.Dir(p.Home), "stateroot"))
	if err != nil {
		t.Fatal(err)
	}
	if (&Client{Paths: headless, Retain: true}).retainHook() != nil {
		t.Fatal("headless mode retains")
	}
}

func TestStatusReportsExecutable(t *testing.T) {
	c, _ := serviceWith(t, DaemonOptions{Handler: &daemonHandler{}, Executable: "/fixture/data/runtime/dev/mcparcel"})
	s, err := c.Status(testCtx(t))
	if err != nil || s.Executable != "/fixture/data/runtime/dev/mcparcel" {
		t.Fatal(s.Executable, err)
	}
}

// A data directory MCParcel cannot create (read-only, full, or a parent the
// user cannot write, as a root-owned ~/.local/share) is not unsafe: retain
// fails plainly and the daemon starts from the CLI binary.
func TestRetainUnwritableDataDirFallsBack(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	p, env := testutil.IsolatedPaths(t)
	parent := filepath.Dir(p.DataDir)
	if err := os.Remove(p.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = retainExecutable(testCtx(t), p, "dev", exe)
	if err == nil || errors.Is(err, config.ErrUnsafePath) {
		t.Fatal("want a plain error, got", err)
	}
	c := &Client{Paths: p, Version: "dev", Executable: exe, Retain: true}
	t.Cleanup(func() { cleanupDaemon(t, c) })
	if started, err := startDaemon(testCtx(t), p, exe, env, config.OpenPrivateFile, c.retainHook()); err != nil || !started {
		t.Fatal(started, err)
	}
	if s := waitStatus(t, c); s.Executable != exe {
		t.Fatal("daemon runs from", s.Executable)
	}
}

// retain.lock lives in the data directory, so a CLI with another runtime
// directory (and its own daemon lock) cannot prune a copy between another
// CLI's retain and its start.
func TestRetainLockSpansRuntimeDirs(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	other := p
	other.RuntimeDir = filepath.Join(filepath.Dir(p.RuntimeDir), "other-runtime")
	// Two older versions, so a new copy would prune one of them.
	for _, v := range []string{"0.1.0", "0.2.0"} {
		if _, err := retain(t, p, v, fakeExe(t, p, "exe-"+v, v)); err != nil {
			t.Fatal(err)
		}
	}
	held, release, err := retainExecutable(testCtx(t), p, "0.1.0", fakeExe(t, p, "exe-0.1.0", "0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := retain(t, other, "0.3.0", fakeExe(t, p, "exe-0.3.0", "0.3.0"))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatal("pruned while another CLI held a copy it had not started:", err)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := os.Stat(held); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(testCtx(t))
	_, release, err = retainExecutable(ctx, p, "0.3.0", fakeExe(t, p, "exe-0.3.0", "0.3.0"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cancel()
	if _, _, err := retainExecutable(ctx, other, "0.3.0", fakeExe(t, p, "exe-0.3.0", "0.3.0")); !errors.Is(err, context.Canceled) {
		t.Fatal("a canceled wait for retain.lock:", err)
	}
}
