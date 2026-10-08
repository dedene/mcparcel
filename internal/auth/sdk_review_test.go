package auth

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/1password/onepassword-sdk-go"
)

type fixtureSecrets struct{ onepassword.SecretsAPI }

func (fixtureSecrets) Resolve(context.Context, string) (string, error) { return "fixture", nil }

func TestSDKSecretsRetainsOwner(t *testing.T) {
	finalized := make(chan struct{})
	owner := &onepassword.Client{SecretsAPI: fixtureSecrets{}}
	runtime.SetFinalizer(owner, func(*onepassword.Client) { close(finalized) })
	client := sdkSecrets(owner)
	owner = nil
	for range 3 {
		runtime.GC()
	}
	select {
	case <-finalized:
		t.Fatal("SDK owner finalized while secret adapter is live")
	case <-time.After(100 * time.Millisecond):
	}
	value, err := client.Resolve(context.Background(), "ref")
	if err != nil || value != "fixture" {
		t.Fatal(value, err)
	}
	runtime.KeepAlive(client)
}

func TestSDKClientConstructionSerialized(t *testing.T) {
	gate := make(sdkGate, 1)
	ctx := context.Background()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct{}, 2)
	create := func() (*onepassword.Client, error) { entered <- struct{}{}; <-release; return nil, nil }
	go func() { _, _ = constructSDKClient(ctx, gate, create); done <- struct{}{} }()
	<-entered
	attempting := make(chan struct{})
	go func() { close(attempting); _, _ = constructSDKClient(ctx, gate, create); done <- struct{}{} }()
	<-attempting
	overlap := false
	select {
	case <-entered:
		overlap = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-done
	<-done
	if overlap {
		t.Fatal("SDK constructors overlapped")
	}
}

// A token client is built on its own core, so a desktop construction stuck on
// an authorization prompt does not hold it up.
func TestSDKTokenConstructionNotBlockedByDesktop(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_, _ = constructSDKClient(context.Background(), desktopSDKGate, func() (*onepassword.Client, error) {
			close(entered)
			<-release
			return nil, nil
		})
		close(done)
	}()
	<-entered
	defer func() { close(release); <-done }()
	built := make(chan struct{})
	go func() {
		_, _ = constructSDKClient(context.Background(), tokenSDKGate, func() (*onepassword.Client, error) { return nil, nil })
		close(built)
	}()
	select {
	case <-built:
	case <-time.After(5 * time.Second):
		t.Fatal("token construction waited behind the desktop construction")
	}
}

// A construction waiting on a busy core returns when its ctx ends instead of
// waiting for the holder.
func TestSDKConstructionWaitHonorsContext(t *testing.T) {
	gate := make(sdkGate, 1)
	gate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	called := false
	_, err := constructSDKClient(ctx, gate, func() (*onepassword.Client, error) { called = true; return nil, nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatal(err, called)
	}
}
