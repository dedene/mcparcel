//go:build linux

package auth

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/dedene/mcparcel/internal/output"
)

// These tests swap package seams and the process environment: never parallel.

// fakeBackend is a go-keyring stand-in; a nil func answers ErrNotFound (Get,
// Delete) or success (Set).
type fakeBackend struct {
	get func(s, u string) (string, error)
	set func(s, u, p string) error
	del func(s, u string) error
}

func (f fakeBackend) Get(s, u string) (string, error) {
	if f.get == nil {
		return "", keyring.ErrNotFound
	}
	return f.get(s, u)
}

func (f fakeBackend) Set(s, u, p string) error {
	if f.set == nil {
		return nil
	}
	return f.set(s, u, p)
}

func (f fakeBackend) Delete(s, u string) error {
	if f.del == nil {
		return keyring.ErrNotFound
	}
	return f.del(s, u)
}

// isolateKeyring clears the inherited bus address and the runtime dir and
// installs b (nil keeps go-keyring); everything is restored afterwards.
func isolateKeyring(t *testing.T, b keyringBackend) {
	t.Helper()
	dir, env, old := runUserDir, lookupEnv, backend
	t.Cleanup(func() {
		runUserDir, lookupEnv, backend = dir, env, old
		wedged.Store(false)
	})
	t.Setenv(busEnv, "")
	if err := os.Unsetenv(busEnv); err != nil {
		t.Fatal(err)
	}
	runUserDir = ""
	if b != nil {
		backend = b
	}
}

// listenBus creates a listening unix socket at dir/bus with dir at mode.
func listenBus(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "bus")
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return p
}

// withRunUserBus points runUserDir at a temp dir holding <uid>/bus (0700).
func withRunUserBus(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	p := listenBus(t, filepath.Join(root, strconv.Itoa(os.Getuid())), 0o700)
	runUserDir = root
	return p
}

func TestSystemKeyringRefusesUnsafeBus(t *testing.T) {
	isolateKeyring(t, nil)
	sticky := listenBus(t, filepath.Join(t.TempDir(), "tmp"), 0o777|os.ModeSticky)
	private := listenBus(t, filepath.Join(t.TempDir(), "private"), 0o700)
	notSocket := filepath.Join(filepath.Dir(private), "file")
	if err := os.WriteFile(notSocket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// godbus execs dbus-launch when it has no address; a fake one first on
	// PATH proves the gate never lets it get that far.
	bin, marker := t.TempDir(), filepath.Join(t.TempDir(), "dbus-launch-ran")
	if err := os.WriteFile(filepath.Join(bin, "dbus-launch"), []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, addr := range []string{
		"", "autolaunch:", "tcp:host=x,port=1", "unix:abstract=x", "unix:path=" + sticky,
		"unix:path=" + notSocket, "unix:path=" + private + ";unix:path=" + private, "unix:path=" + private + ",guid=0",
		"unix:path=" + filepath.Dir(private) + "/./bus", "unix:path=bus",
	} {
		t.Run(addr, func(t *testing.T) {
			if addr == "" {
				if err := os.Unsetenv(busEnv); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv(busEnv, addr)
			}
			start := time.Now()
			_, err := LoadOAuth(context.Background(), SystemKeyring{}, "local:a")
			wantKeychain(t, err, output.NewError("keychain_unavailable", nil).Message)
			if time.Since(start) > time.Second {
				t.Fatal("refusal took", time.Since(start))
			}
			if got, _ := os.LookupEnv(busEnv); got != addr {
				t.Fatalf("bus address changed to %q", got)
			}
			if KeyringReachable() || BusAddressFrom(addr) {
				t.Fatal("unsafe bus accepted")
			}
		})
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dbus-launch ran:", err)
	}
	if !BusAddressFrom("unix:path=" + private) {
		t.Fatal("a private socket must be accepted")
	}
}

func TestSystemKeyringUsesRunUserBus(t *testing.T) {
	isolateKeyring(t, fakeBackend{})
	p := withRunUserBus(t)
	for _, inherited := range []string{"", "tcp:host=x,port=1"} {
		if inherited == "" {
			_ = os.Unsetenv(busEnv)
		} else {
			t.Setenv(busEnv, inherited)
		}
		if !KeyringReachable() {
			t.Fatal("run user bus not reachable")
		}
		if _, err := LoadOAuth(context.Background(), SystemKeyring{}, "local:a"); !errors.Is(err, ErrNoSession) {
			t.Fatal(err)
		}
		if got := os.Getenv(busEnv); got != "unix:path="+p {
			t.Fatalf("%q: bus address %q", inherited, got)
		}
	}
	// A safe inherited address is used as it is.
	other := listenBus(t, filepath.Join(t.TempDir(), "session"), 0o700)
	t.Setenv(busEnv, "unix:path="+other)
	if _, err := (SystemKeyring{}).Get(KeyringService, "local:a"); !errors.Is(err, ErrNoSession) {
		t.Fatal(err)
	}
	if got := os.Getenv(busEnv); got != "unix:path="+other {
		t.Fatal(got)
	}
}

func TestSystemKeyringReadBack(t *testing.T) {
	isolateKeyring(t, nil)
	withRunUserBus(t)
	ctx := context.Background()
	generic := output.NewError("keychain_unavailable", nil).Message
	stored := func(v string) func(string, string) (string, error) {
		return func(string, string) (string, error) { return v, nil }
	}

	backend = fakeBackend{}
	if _, err := LoadOAuth(ctx, SystemKeyring{}, "local:a"); !errors.Is(err, ErrNoSession) {
		t.Fatal("not found:", err)
	}

	backend = fakeBackend{get: stored("other")}
	if err := (SystemKeyring{}).Set(KeyringService, "local:a", "secret"); !errors.Is(err, errKeyringNotStored) {
		t.Fatal("mismatch:", err)
	}
	wantKeychain(t, SaveOAuth(ctx, SystemKeyring{}, "local:a", OAuthState{Version: 1}), generic)
	backend = fakeBackend{}
	if err := (SystemKeyring{}).Set(KeyringService, "local:a", "secret"); !errors.Is(err, errKeyringNotStored) {
		t.Fatal("dismissed create:", err)
	}
	backend = fakeBackend{get: stored("secret")}
	if err := (SystemKeyring{}).Set(KeyringService, "local:a", "secret"); err != nil {
		t.Fatal(err)
	}

	backend = fakeBackend{del: func(string, string) error { return nil }, get: stored("secret")}
	if err := (SystemKeyring{}).Delete(KeyringService, "local:a"); !errors.Is(err, errKeyringNotDeleted) {
		t.Fatal("dismissed delete:", err)
	}
	if _, err := DeleteOAuth(ctx, SystemKeyring{}, "local:a"); err == nil {
		t.Fatal("a delete that left the item must fail")
	} else {
		wantKeychain(t, err, generic)
	}
	backend = fakeBackend{del: func(string, string) error { return nil }}
	if removed, err := DeleteOAuth(ctx, SystemKeyring{}, "local:a"); !removed || err != nil {
		t.Fatal(removed, err)
	}
	backend = fakeBackend{}
	if removed, err := DeleteOAuth(ctx, SystemKeyring{}, "local:a"); removed || err != nil {
		t.Fatal("missing:", removed, err)
	}
}

func TestSystemKeyringPanicIsUnavailable(t *testing.T) {
	isolateKeyring(t, fakeBackend{get: func(string, string) (string, error) { panic("nil prompt signal") }})
	withRunUserBus(t)
	_, err := LoadOAuth(context.Background(), SystemKeyring{}, "local:a")
	wantKeychain(t, err, output.NewError("keychain_unavailable", nil).Message)
	// The panicking call gave its slot back.
	backend = fakeBackend{}
	if _, err := LoadOAuth(context.Background(), SystemKeyring{}, "local:a"); !errors.Is(err, ErrNoSession) {
		t.Fatal(err)
	}
}

func TestSystemKeyringPromptPending(t *testing.T) {
	unblock := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(unblock) }) })
	calls := 0
	var mu sync.Mutex
	isolateKeyring(t, fakeBackend{get: func(string, string) (string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-unblock
		return "", keyring.ErrNotFound
	}})
	withRunUserBus(t)
	pending := output.KeyringPromptPendingError().Message

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := LoadOAuth(ctx, SystemKeyring{}, "local:a")
	wantKeychain(t, err, pending)
	if !KeyringWedged(SystemKeyring{}) {
		t.Fatal("the abandoned call must wedge the keyring")
	}

	before := goruntime.NumGoroutine()
	start := time.Now()
	_, err = LoadOAuth(context.Background(), SystemKeyring{}, "local:a")
	wantKeychain(t, err, pending)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatal("wedged call took", d)
	}
	if after := goruntime.NumGoroutine(); after > before {
		t.Fatalf("goroutines %d -> %d", before, after)
	}
	mu.Lock()
	if calls != 1 {
		t.Fatal("backend calls", calls)
	}
	mu.Unlock()

	once.Do(func() { close(unblock) })
	for deadline := time.Now().Add(2 * time.Second); KeyringWedged(SystemKeyring{}); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the returning call did not clear the wedge")
		}
	}
	if _, err := LoadOAuth(context.Background(), SystemKeyring{}, "local:a"); !errors.Is(err, ErrNoSession) {
		t.Fatal(err)
	}
}
