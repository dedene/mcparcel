package testutil

import (
	"errors"
	"sync"

	"github.com/dedene/mcparcel/internal/auth"
)

// MemKeyring is an in-memory auth.Keyring. Err fails every call; FailSets
// fails that many Set calls; BeforeSet runs before each Set.
type MemKeyring struct {
	mu        sync.Mutex
	items     map[string]string
	gets      int
	Err       error
	FailSets  int
	BeforeSet func()
}

// Gets counts Get calls.
func (k *MemKeyring) Gets() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.gets
}

func (k *MemKeyring) Get(service, account string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.gets++
	if k.Err != nil {
		return "", k.Err
	}
	v, ok := k.items[service+"\x00"+account]
	if !ok {
		return "", auth.ErrNoSession
	}
	return v, nil
}

func (k *MemKeyring) Set(service, account, secret string) error {
	k.mu.Lock()
	before := k.BeforeSet
	k.mu.Unlock()
	if before != nil {
		before()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.Err != nil {
		return k.Err
	}
	if k.FailSets > 0 {
		k.FailSets--
		return errors.New("fixture keyring set failure")
	}
	if k.items == nil {
		k.items = map[string]string{}
	}
	k.items[service+"\x00"+account] = secret
	return nil
}

func (k *MemKeyring) Delete(service, account string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.Err != nil {
		return k.Err
	}
	if _, ok := k.items[service+"\x00"+account]; !ok {
		return auth.ErrNoSession
	}
	delete(k.items, service+"\x00"+account)
	return nil
}
