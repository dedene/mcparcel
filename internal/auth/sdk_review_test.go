package auth

import (
	"context"
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
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct{}, 2)
	create := func() (*onepassword.Client, error) { entered <- struct{}{}; <-release; return nil, nil }
	go func() { _, _ = constructSDKClient(create); done <- struct{}{} }()
	<-entered
	attempting := make(chan struct{})
	go func() { close(attempting); _, _ = constructSDKClient(create); done <- struct{}{} }()
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
