//go:build darwin || linux

package runtime

import (
	"errors"
	"os"

	"github.com/dedene/mcparcel/internal/config"
)

// supervisedMarker in the runtime directory records that a supervisor runs
// this user's runtime (mcparcel runtime serve). It outlives serve on purpose:
// while serve is down (start ordering, a crash, a supervisor restart), a CLI
// must wait for it instead of auto-starting a daemon from its own
// environment, which would hold the lock and keep the supervised runtime out.
const supervisedMarker = "supervised"

// MarkSupervised records the runtime directory as supervisor-owned. The
// caller holds the daemon lock, which created the directory.
func MarkSupervised(paths config.Paths) error {
	dir, err := config.OpenPrivateDir(paths.RuntimeDir, false)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	f, err := config.OpenPrivateFile(dir, supervisedMarker, true)
	if err != nil {
		return err
	}
	return f.Close()
}

// Supervised reports whether a supervisor owns the runtime directory.
func Supervised(paths config.Paths) (bool, error) {
	dir, err := config.OpenPrivateDir(paths.RuntimeDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = dir.Close() }()
	f, err := config.OpenPrivateFile(dir, supervisedMarker, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, f.Close()
}
