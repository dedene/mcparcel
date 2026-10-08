package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zalando/go-keyring"
	"golang.org/x/sys/unix"
)

const (
	// busEnv is the variable godbus reads the session bus address from.
	busEnv = "DBUS_SESSION_BUS_ADDRESS"
	// busPathBytes are the path bytes D-Bus never escapes.
	busPathBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-"
)

// keyringBackend is the part of go-keyring SystemKeyring uses.
type keyringBackend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type goKeyring struct{}

func (goKeyring) Get(s, u string) (string, error) { return keyring.Get(s, u) }
func (goKeyring) Set(s, u, p string) error        { return keyring.Set(s, u, p) }
func (goKeyring) Delete(s, u string) error        { return keyring.Delete(s, u) }

// Test seams: the per-user runtime directory, the environment and go-keyring.
var (
	runUserDir                = "/run/user"
	lookupEnv                 = os.LookupEnv
	backend    keyringBackend = goKeyring{}
)

// sem is the one call slot of the Secret Service; wedged marks it as held by
// a call keyringCall abandoned (an unanswered prompt), so later calls fail
// fast instead of queueing behind it.
var (
	sem    = make(chan struct{}, 1)
	wedged atomic.Bool
)

var (
	errNoSessionBus         = errors.New("no usable D-Bus session bus")
	errKeyringPromptPending = errors.New("keyring prompt pending")
	errKeyringPanic         = errors.New("keyring call panicked")
	errKeyringNotStored     = errors.New("keyring did not store the item")
	errKeyringNotDeleted    = errors.New("keyring did not delete the item")
)

// SystemKeyring is the Secret Service (GNOME Keyring, KWallet) through
// go-keyring. Every call first pins godbus to a checked session bus socket,
// and Set and Delete read the item back, since go-keyring ignores a dismissed
// prompt.
type SystemKeyring struct{}

func (SystemKeyring) Get(service, account string) (v string, err error) {
	defer recoverKeyring(&err)
	if err = busReady(); err != nil {
		return "", err
	}
	v, err = backend.Get(service, account)
	return v, notFound(err)
}

func (SystemKeyring) Set(service, account, secret string) (err error) {
	defer recoverKeyring(&err)
	if err = busReady(); err != nil {
		return err
	}
	if err = backend.Set(service, account, secret); err != nil {
		return err
	}
	got, err := backend.Get(service, account)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return errKeyringNotStored
	case err != nil:
		return err
	case got != secret:
		return errKeyringNotStored
	}
	return nil
}

func (SystemKeyring) Delete(service, account string) (err error) {
	defer recoverKeyring(&err)
	if err = busReady(); err != nil {
		return err
	}
	if err = backend.Delete(service, account); err != nil {
		return notFound(err)
	}
	if _, err = backend.Get(service, account); errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return errKeyringNotDeleted
}

// acquire takes the call slot, waiting at most until ctx ends; a wedged slot
// fails at once.
func (SystemKeyring) acquire(ctx context.Context) (release, wedge func(), err error) {
	if wedged.Load() {
		return nil, nil, errKeyringPromptPending
	}
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, errKeyringPromptPending
	}
	if ctx.Err() != nil {
		<-sem
		return nil, nil, errKeyringPromptPending
	}
	var mu sync.Mutex
	held := true
	release = func() {
		mu.Lock()
		defer mu.Unlock()
		held = false
		wedged.Store(false)
		<-sem
	}
	wedge = func() {
		mu.Lock()
		defer mu.Unlock()
		if held {
			wedged.Store(true)
		}
	}
	return release, wedge, nil
}

func (SystemKeyring) stuck() bool { return wedged.Load() }

// recoverKeyring turns a go-keyring panic (a nil prompt signal after the bus
// closed) into an error.
func recoverKeyring(err *error) {
	if recover() != nil {
		*err = errKeyringPanic
	}
}

// KeyringReachable reports whether a usable session bus exists. It does not
// check that a Secret Service provider runs on it or is unlocked.
func KeyringReachable() bool {
	_, err := busAddress()
	return err == nil
}

// BusAddressFrom reports whether value is a session bus address the daemon
// may inherit: one unix:path= socket owned by this user in a private
// directory.
func BusAddressFrom(value string) bool {
	p, ok := busPath(value)
	return ok && busSocketOK(p)
}

// busReady points godbus at a checked session bus before a keyring call, so
// neither its first connect nor a reconnect ever uses another address or
// starts dbus-launch.
func busReady() error {
	addr, err := busAddress()
	if err != nil {
		return err
	}
	if v, ok := lookupEnv(busEnv); !ok || v != addr {
		return os.Setenv(busEnv, addr)
	}
	return nil
}

// busAddress picks the session bus without side effects: the inherited
// address when it is a safe unix:path= socket, else runUserDir/<uid>/bus.
// Abstract, TCP, autolaunch and launchd addresses are never used.
func busAddress() (string, error) {
	if v, ok := lookupEnv(busEnv); ok && BusAddressFrom(v) {
		return v, nil
	}
	if runUserDir != "" {
		p := filepath.Join(runUserDir, strconv.Itoa(os.Getuid()), "bus")
		if busSocketOK(p) {
			return "unix:path=" + p, nil
		}
	}
	return "", errNoSessionBus
}

// busPath returns the socket of an address that is exactly one unix:path=
// entry with a clean absolute path that needs no D-Bus escaping.
func busPath(addr string) (string, bool) {
	p, ok := strings.CutPrefix(addr, "unix:path=")
	if !ok || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return "", false
	}
	if strings.Trim(p, busPathBytes) != "" {
		return "", false
	}
	return p, true
}

// busSocketOK accepts a socket owned by this user whose directory is owned
// by this user and writable by no one else, so no other user can have put it
// there (a sticky /tmp is refused).
func busSocketOK(p string) bool {
	uid := uint32(os.Getuid())
	var sock, dir unix.Stat_t
	if unix.Lstat(p, &sock) != nil || sock.Mode&unix.S_IFMT != unix.S_IFSOCK || sock.Uid != uid {
		return false
	}
	return unix.Lstat(filepath.Dir(p), &dir) == nil && dir.Mode&unix.S_IFMT == unix.S_IFDIR && dir.Uid == uid && dir.Mode&0o022 == 0
}
