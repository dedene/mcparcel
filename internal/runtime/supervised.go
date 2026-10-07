//go:build darwin || linux

package runtime

import (
	"errors"
	"os"

	"github.com/dedene/mcparcel/internal/config"
)

// supervisedMarker in the runtime directory records that a supervisor runs
// this user's runtime (mcparcel runtime serve). It outlives serve on purpose:
// once serve has started, while it is down (a crash, a supervisor restart) a
// CLI must wait for it instead of auto-starting a daemon from its own
// environment, which would hold the lock and keep the supervised runtime out.
// It cannot cover the time before serve first takes the lock, such as a
// fresh emptyDir on every pod start; runtime.supervised in config.json does.
const supervisedMarker = "supervised"

// MarkSupervised records the runtime directory as supervisor-owned. The
// caller holds the daemon lock, which created the directory.
func MarkSupervised(paths config.Paths) error {
	dir, err := config.OpenPrivateDirUnder(paths.StateRoot, paths.RuntimeDir, false)
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

// Supervised reports whether a supervisor owns the runtime: config.json
// says so (runtime.supervised), or runtime serve marked the directory.
func Supervised(paths config.Paths) (bool, error) {
	if paths.Supervised {
		return true, nil
	}
	dir, err := config.OpenPrivateDirUnder(paths.StateRoot, paths.RuntimeDir, false)
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
