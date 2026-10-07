package testutil

import (
	"context"
	"sync"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
)

type FakeProvider struct {
	BootstrapFunc func(context.Context, config.Profile) (auth.SecretClient, error)
}

func (p FakeProvider) Bootstrap(ctx context.Context, profile config.Profile) (auth.SecretClient, error) {
	if p.BootstrapFunc == nil {
		return nil, auth.ErrProvider
	}
	return p.BootstrapFunc(ctx, profile)
}

type FakeSecretClient struct {
	ResolveFunc func(context.Context, string) (string, error)
}

func (c FakeSecretClient) Resolve(ctx context.Context, ref string) (string, error) {
	if c.ResolveFunc == nil {
		return "", auth.ErrProvider
	}
	return c.ResolveFunc(ctx, ref)
}

// FakeVault is an auth.Provider over an in-memory vault. It counts
// bootstraps and reads per reference. When Block is set, Bootstrap waits on it
// and ignores its context, the way a hung SDK call or an open prompt does.
type FakeVault struct {
	Block chan struct{}

	mu        sync.Mutex
	values    map[string]string
	failures  map[string]error
	bootError error
	boots     int
	reads     map[string]int
}

// Set stores value under ref.
func (v *FakeVault) Set(ref, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.values == nil {
		v.values = map[string]string{}
	}
	v.values[ref] = value
}

// Fail makes reads of ref return err; a nil err clears the failure.
func (v *FakeVault) Fail(ref string, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.failures == nil {
		v.failures = map[string]error{}
	}
	v.failures[ref] = err
}

// FailBootstrap makes Bootstrap return err; a nil err clears the failure.
func (v *FakeVault) FailBootstrap(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.bootError = err
}

// Boots returns how many times Bootstrap was entered.
func (v *FakeVault) Boots() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.boots
}

// Reads returns how many times ref was read.
func (v *FakeVault) Reads(ref string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.reads[ref]
}

func (v *FakeVault) Bootstrap(ctx context.Context, _ config.Profile) (auth.SecretClient, error) {
	v.mu.Lock()
	v.boots++
	block := v.Block
	v.mu.Unlock()
	if block != nil {
		<-block
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.bootError != nil {
		return nil, v.bootError
	}
	return FakeSecretClient{ResolveFunc: v.read}, nil
}

func (v *FakeVault) read(_ context.Context, ref string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.reads == nil {
		v.reads = map[string]int{}
	}
	v.reads[ref]++
	if err := v.failures[ref]; err != nil {
		return "", err
	}
	value, ok := v.values[ref]
	if !ok {
		return "", auth.ErrProvider
	}
	return value, nil
}
