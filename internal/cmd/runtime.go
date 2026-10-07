package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

type RuntimeCmd struct {
	Stop    RuntimeStopCmd    `cmd:"" help:"Stop the runtime and owned server sessions."`
	Status  RuntimeStatusCmd  `cmd:"" help:"Inspect the runtime without starting it."`
	Restart RuntimeRestartCmd `cmd:"" help:"Restart the runtime and reset owned server state."`
	Serve   RuntimeServeCmd   `cmd:"" help:"Run the runtime in the foreground under a supervisor."`
}
type (
	RuntimeStatusCmd struct{}
	RuntimeStopCmd   struct {
		Force bool `help:"Cancel active calls before stopping."`
	}
	RuntimeRestartCmd struct {
		Force bool `help:"Cancel active calls before restarting."`
	}
)

// commandPaths resolves the XDG paths, then reads config.json's runtime block;
// in headless mode its state root replaces the state, data, cache and runtime
// directories. The daemon resolves its paths the same way from the same
// config, so CLI and daemon agree.
func commandPaths() (config.Paths, error) {
	paths, _, err := resolveCommandPaths()
	return paths, err
}

func resolveCommandPaths() (config.Paths, config.RuntimeDefaults, error) {
	paths, err := config.ResolvePaths(os.Getenv, os.Getenv("HOME"), config.DefaultTempDir(), os.Getuid())
	if err != nil {
		return config.Paths{}, config.RuntimeDefaults{}, err
	}
	rt, err := config.ReadRuntime(context.Background(), paths)
	if err != nil {
		return config.Paths{}, config.RuntimeDefaults{}, err
	}
	if rt.Mode == config.ModeHeadless {
		if paths, err = config.ApplyStateRoot(paths, rt.StateRoot); err != nil {
			return config.Paths{}, config.RuntimeDefaults{}, err
		}
		paths.Supervised = rt.Supervised
	}
	return paths, rt, nil
}

// runtimePaths is commandPaths for a command that uses the runtime (tools,
// call, auth, runtime, daemon): where desktop mode is unsupported (Linux), it
// refuses desktop mode with runtime_unsupported.
func runtimePaths() (config.Paths, error) {
	paths, rt, err := resolveCommandPaths()
	if err != nil {
		return config.Paths{}, err
	}
	if err = config.CheckMode(rt, desktopSupported()); err != nil {
		return config.Paths{}, err
	}
	return paths, nil
}

func newRuntimeClient(opts *CommandOptions) (*runtimeclient.Client, error) {
	paths, err := runtimePaths()
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return nil, output.NewError("runtime_start_failed", nil)
	}
	info, err := os.Stat(exe)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, output.NewError("runtime_start_failed", nil)
	}
	// Headless implies --no-input: no prompt, browser or dialog (D10).
	return &runtimeclient.Client{Paths: paths, Version: version, Executable: exe, NoInput: opts.NoInput || paths.Headless()}, nil
}

func statusText(status runtimeclient.Status, headless bool) string {
	var b strings.Builder
	state := "stopped"
	if status.Running {
		state = "running"
	}
	fmt.Fprintf(&b, "Runtime: %s\n", state)
	if status.Running {
		environment := "login shell"
		if headless {
			environment = "daemon environment"
		} else if status.EnvFallback {
			environment = "caller fallback"
		}
		stay := "off"
		if status.StayAlive {
			stay = "on"
		}
		fmt.Fprintf(&b, "PID: %d\nVersion: %s\nActive calls: %d\nPATH: %s\nEnvironment: %s\nStay-alive: %s\n", status.PID, status.BinaryVersion, status.ActiveCalls, status.CapturedPath, environment, stay)
	}
	fmt.Fprintf(&b, "Socket: %s\nLog: %s\n", status.Socket, status.Log)
	return b.String()
}

func (c *RuntimeStatusCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	data, err := client.Status(ctx)
	if err != nil {
		return err
	}
	if opts.JSON {
		return writeSuccess(s, opts, data)
	}
	return writeSuccess(s, opts, statusText(data, client.Paths.Headless()))
}

func (c *RuntimeRestartCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	data, err := client.Restart(ctx, c.Force)
	if err != nil {
		return err
	}
	if opts.JSON {
		return writeSuccess(s, opts, data)
	}
	return writeSuccess(s, opts, "Runtime restarted. Server state was reset.\n"+statusText(data.Status, client.Paths.Headless()))
}

func (c *RuntimeStopCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	data, err := client.Stop(ctx, c.Force)
	if err != nil {
		return err
	}
	if opts.JSON {
		return writeSuccess(s, opts, data)
	}
	return writeSuccess(s, opts, "Runtime stopped.\n")
}
