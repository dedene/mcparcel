package cmd

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
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
	paths, err := runtimePaths()
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
	return runDaemon(ctx, paths, lock, log, false)
}

// daemonEnvSource returns how the runtime obtains the environment its
// servers start from. Desktop mode captures the login shell; headless mode
// is to use the daemon's own environment instead (D11).
func daemonEnvSource(config.Paths) func(context.Context) (map[string]string, error) {
	return runtimeclient.CaptureLoginEnv
}

// runDaemon serves the runtime on a held daemon lock until ctx ends, a stop
// request arrives or, unless supervised, it has been idle. A supervised
// runtime also refuses restart requests. The auto-started daemon and runtime
// serve share it, so both apply the same environment, credential, config,
// peer and version checks.
func runDaemon(ctx context.Context, paths config.Paths, lock *os.File, log io.Writer, supervised bool) error {
	login, captureErr := daemonEnvSource(paths)(ctx)
	credentials := newCredentials(paths, version)
	defer credentials.Close()
	pool := runtimeclient.NewPool(runtimeclient.PoolOptions{Paths: paths, LoginEnv: login, Version: version, Credentials: credentials, Keychain: newKeychain(paths), Keyring: newKeyring(paths), Health: auth.NewHealth(paths.StateDir, time.Now), Log: func(event string) { _ = runtimeclient.WriteLog(log, event, nil) }, SignInFailure: func(stage, code string) { _ = runtimeclient.WriteSignInFailure(log, stage, code) }})
	return runtimeclient.Serve(ctx, runtimeclient.DaemonOptions{Paths: paths, Version: version, Lock: lock, LoginEnv: login, EnvFallback: captureErr != nil, Handler: pool, Log: log, IdleTimeout: daemonIdleTimeout(paths), NoIdleExit: supervised, Supervised: supervised})
}
