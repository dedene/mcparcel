package lockfile

import (
	"context"
	"errors"
	"os"
	"time"
)

// Open retries transient ENOENT from concurrent Darwin O_CREAT opens.
// The supplied opener remains responsible for path and file safety checks.
func Open(ctx context.Context, create bool, open func() (*os.File, error)) (*os.File, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := open()
		if !create || !errors.Is(err, os.ErrNotExist) {
			return file, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
