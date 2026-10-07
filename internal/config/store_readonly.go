package config

import (
	"context"
	"errors"
)

// ErrConfigReadOnly refuses a configuration write in headless mode.
var ErrConfigReadOnly = errors.New("configuration is read-only in headless mode")

// refuseHeadlessWrite returns ErrConfigReadOnly when config.json selects
// headless mode, where the configuration (a ConfigMap) is the source of truth
// and is never written: no lock file, no snapshot, no legacy migration. It
// reads config.json through ReadRuntime and never creates anything.
func refuseHeadlessWrite(ctx context.Context, p Paths) error {
	rt, err := ReadRuntime(ctx, p)
	if err != nil {
		return err
	}
	if rt.Mode == ModeHeadless {
		return ErrConfigReadOnly
	}
	return nil
}
