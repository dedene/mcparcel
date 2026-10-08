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
	paths, err := daemonPaths()
	if err != nil {
		return err
	}
	if paths.Supervised {
		// Only runtime serve runs a supervised runtime.
		return output.NewError("runtime_supervised", nil)
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
// uses the daemon's own environment restricted to the forwarded names (D11)
// and never runs a shell.
func daemonEnvSource(paths config.Paths) func(context.Context) (map[string]string, error) {
	if paths.Headless() {
		return func(context.Context) (map[string]string, error) { return runtimeclient.HeadlessDaemonEnv(paths), nil }
	}
	return runtimeclient.CaptureLoginEnv
}

// runDaemon serves the runtime on a held daemon lock until ctx ends, a stop
// request arrives or, unless supervised, it has been idle. A supervised
// runtime also refuses restart requests. The auto-started daemon and runtime
// serve share it, so both apply the same environment, credential, config,
// peer and version checks.
func runDaemon(ctx context.Context, paths config.Paths, lock *os.File, log io.Writer, supervised bool) error {
	// Before any credential is read: no same-uid child may read this
	// process's environment or memory (D20).
	runtimeclient.HardenProcess()
	login, captureErr := daemonEnvSource(paths)(ctx)
	opts := daemonPoolOptions(paths, login, log)
	if opts.Credentials != nil {
		defer opts.Credentials.Close()
	}
	pool := runtimeclient.NewPool(opts)
	// The file this daemon runs from, for runtime status and doctor.
	exe, _ := os.Executable()
	return runtimeclient.Serve(ctx, runtimeclient.DaemonOptions{Paths: paths, Version: version, Executable: exe, Lock: lock, LoginEnv: login, EnvFallback: captureErr != nil, Handler: pool, Log: log, IdleTimeout: daemonIdleTimeout(paths), NoIdleExit: supervised, Supervised: supervised})
}

// The credential sources: 1Password, the Keychain env: fallback and the
// OAuth keyring. Tests replace them with spies.
var (
	credentialsFactory = newCredentials
	keychainFactory    = newKeychain
	keyringFactory     = newKeyring
)

// daemonPoolOptions configures the runtime's pool. Both modes get the
// 1Password resolver, which reads service-account tokens from login; headless
// mode constructs neither the Keychain nor the keyring (D10, D11), and its
// pool refuses every profile but a service-account one. Desktop mode without
// the 1Password app (Linux) refuses the profiles that use it.
func daemonPoolOptions(paths config.Paths, login map[string]string, log io.Writer) runtimeclient.PoolOptions {
	opts := runtimeclient.PoolOptions{
		Paths: paths, LoginEnv: login, Version: version, Health: auth.NewHealth(paths.StateDir, time.Now),
		Log:           func(event string) { _ = runtimeclient.WriteLog(log, event, nil) },
		SignInFailure: func(stage, code string) { _ = runtimeclient.WriteSignInFailure(log, stage, code) },
		TokenLog:      func(event string, fields map[string]any) { _ = runtimeclient.WriteTokenEvent(log, event, fields) },
	}
	opts.Credentials = credentialsFactory(paths, version, login)
	if paths.Headless() {
		opts.Headless = true
		return opts
	}
	opts.NoDesktopApp = !desktopOnePassword(paths)
	opts.Keychain, opts.Keyring = keychainFactory(paths), keyringFactory(paths)
	return opts
}
