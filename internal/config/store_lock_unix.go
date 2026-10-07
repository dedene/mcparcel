//go:build darwin || linux

package config

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/lockfile"
)

func acquireConfigLock(ctx context.Context, paths Paths, exclusive, create bool) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonical, err := canonicalConfigPath(paths.ConfigDir)
	if err != nil {
		return nil, err
	}
	name := ".mcparcel.lock"
	dir, err := openConfigDir(canonical, create)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	file, err := lockfile.Open(ctx, create, func() (*os.File, error) {
		return OpenPrivateFile(dir, name, create)
	})
	if err != nil {
		return nil, err
	}
	operation := unix.LOCK_SH
	if exclusive {
		operation = unix.LOCK_EX
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		err = unix.Flock(int(file.Fd()), operation|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func releaseConfigLock(lock *os.File) error {
	if lock == nil {
		return nil
	}
	err := unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	return errors.Join(err, lock.Close())
}
