package cmd

import (
	"context"

	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

type DaemonCmd struct {
	LockFD int `name:"lock-fd" default:"-1"`
}

func (c *DaemonCmd) Run(ctx context.Context, _ *Streams) error {
	if c.LockFD != 3 {
		return output.NewError("unsafe_local_path", nil)
	}
	paths, err := commandPaths()
	if err != nil {
		return err
	}
	lock, err := runtimeclient.AdoptLock(paths, uintptr(c.LockFD))
	if err != nil {
		return err
	}
	defer lock.Close()
	log, err := runtimeclient.OpenLog(paths)
	if err != nil {
		return err
	}
	defer log.Close()
	login, captureErr := runtimeclient.CaptureLoginEnv(ctx)
	credentials := newCredentials(paths, version)
	defer credentials.Close()
	pool := runtimeclient.NewPool(runtimeclient.PoolOptions{Paths: paths, LoginEnv: login, Version: version, Credentials: credentials, Keychain: newKeychain(paths), Log: func(event string) { _ = runtimeclient.WriteLog(log, event, nil) }})
	return runtimeclient.Serve(ctx, runtimeclient.DaemonOptions{Paths: paths, Version: version, Lock: lock, LoginEnv: login, EnvFallback: captureErr != nil, Handler: pool, Log: log})
}
