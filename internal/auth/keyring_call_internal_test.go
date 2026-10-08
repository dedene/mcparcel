package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/output"
)

// gatedKeyring is a keyringGate whose Get waits for unblock (when set) and
// which records acquire, release and wedge.
type gatedKeyring struct {
	unblock               chan struct{}
	refuse                bool
	acquired, released    atomic.Int32
	wedgedNow, everWedged atomic.Bool
}

func (k *gatedKeyring) Get(string, string) (string, error) {
	if k.unblock != nil {
		<-k.unblock
	}
	return "", ErrNoSession
}
func (k *gatedKeyring) Set(string, string, string) error { return nil }
func (k *gatedKeyring) Delete(string, string) error      { return nil }
func (k *gatedKeyring) stuck() bool                      { return k.wedgedNow.Load() }

func (k *gatedKeyring) acquire(context.Context) (func(), func(), error) {
	if k.refuse {
		return nil, nil, errors.New("slot busy")
	}
	k.acquired.Add(1)
	return func() { k.released.Add(1); k.wedgedNow.Store(false) }, func() { k.wedgedNow.Store(true); k.everWedged.Store(true) }, nil
}

// wantKeychain asserts err is keychain_unavailable with message.
func wantKeychain(t *testing.T, err error, message string) {
	t.Helper()
	var e *output.Error
	if !errors.As(err, &e) || e.Code != "keychain_unavailable" || e.Message != message {
		t.Fatalf("error = %v, want keychain_unavailable %q", err, message)
	}
}

func TestKeyringCallGate(t *testing.T) {
	pending := output.KeyringPromptPendingError().Message
	generic := output.NewError("keychain_unavailable", nil).Message
	get := func(k Keyring) func() (string, error) {
		return func() (string, error) { return k.Get(KeyringService, "local:a") }
	}

	k := &gatedKeyring{}
	if _, err := keyringCall(context.Background(), k, time.Second, get(k)); !errors.Is(err, ErrNoSession) {
		t.Fatal(err)
	}
	if k.acquired.Load() != 1 || k.released.Load() != 1 || k.everWedged.Load() {
		t.Fatal("finished call: acquired", k.acquired.Load(), "released", k.released.Load())
	}

	refused := &gatedKeyring{refuse: true}
	_, err := keyringCall(context.Background(), refused, time.Second, get(refused))
	wantKeychain(t, err, pending)

	blocked := &gatedKeyring{unblock: make(chan struct{})}
	_, err = keyringCall(context.Background(), blocked, 50*time.Millisecond, get(blocked))
	wantKeychain(t, err, pending)
	if !KeyringWedged(blocked) || blocked.released.Load() != 0 {
		t.Fatal("an abandoned call must wedge the keyring until it returns")
	}
	close(blocked.unblock)
	for deadline := time.Now().Add(2 * time.Second); KeyringWedged(blocked); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("release did not clear the wedge")
		}
	}

	// An ungated keyring keeps the opaque text on a timeout.
	slow := make(chan struct{})
	defer close(slow)
	_, err = keyringCall(context.Background(), Keyring(nil), time.Second, get(nil))
	wantKeychain(t, err, generic)
	_, err = keyringCall(context.Background(), &ungated{slow}, 50*time.Millisecond, func() (string, error) { <-slow; return "", nil })
	wantKeychain(t, err, generic)
	if KeyringWedged(&ungated{}) {
		t.Fatal("an ungated keyring is never wedged")
	}
}

type ungated struct{ unblock chan struct{} }

func (ungated) Get(string, string) (string, error) { return "", ErrNoSession }
func (ungated) Set(string, string, string) error   { return nil }
func (ungated) Delete(string, string) error        { return nil }
