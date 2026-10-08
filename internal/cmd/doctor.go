package cmd

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/doctor"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

type DoctorCmd struct {
	MCP  string `arg:"" optional:"" json:"-" help:"Check only this connection (plus the global checks)."`
	Live bool   `json:"-" help:"Also connect to the named connection and list its tools."`
}

// doctorAppDirs is where doctor looks for 1Password.app; tests replace it so
// they never look at the real /Applications.
var doctorAppDirs = onePasswordAppDirs

// doctorConfigWait bounds doctor's wait for a writer's configuration lock.
const doctorConfigWait = 5 * time.Second

// Run is offline unless --live: it never starts or waits for the runtime,
// runs no shell, reads no credential store and writes nothing. It may ask an
// already running runtime for its status, as runtime status does.
func (c *DoctorCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	if c.Live && c.MCP == "" {
		return output.CommandUsageError()
	}
	in, err := doctorInput(ctx, c.MCP)
	if err != nil {
		return err
	}
	// A canceled doctor exits 130, not with rows its cancellation failed.
	if err = ctx.Err(); err != nil {
		return err
	}
	checks := doctor.Offline(in)
	if c.Live {
		checks = append(checks, doctor.Live(ctx, in, func(ctx context.Context) (output.ToolList, error) {
			client, err := newRuntimeClient(opts)
			if err != nil {
				return output.ToolList{}, safeFailure(err)
			}
			client.NoStart = !doctor.MayStart(in)
			list, err := client.Tools(ctx, in.Target, false)
			if errors.Is(err, runtimeclient.ErrNotRunning) {
				return output.ToolList{}, err
			}
			if err != nil {
				return output.ToolList{}, safeFailure(err)
			}
			return list, nil
		}))
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	data := output.DoctorData{Version: version, Mode: doctor.Mode(in), Live: c.Live, Checks: checks, Summary: doctor.Summarize(checks)}
	if data.Summary.Fail > 0 {
		for _, check := range checks {
			if check.Status == doctor.Fail {
				return &commandFailure{Data: data, Failure: output.DoctorFailed(data.Summary.Fail, check)}
			}
		}
	}
	return writeSuccess(s, opts, data)
}

// doctorInput gathers what the offline checks look at. Only an unusable home
// or an unknown or ambiguous <mcp> fails the command itself; everything else
// becomes a row.
func doctorInput(ctx context.Context, target string) (doctor.Input, error) {
	in := doctor.Input{
		Version:          version,
		DesktopSupported: desktopSupported(),
		LookupEnv:        func(name string) bool { _, ok := os.LookupEnv(name); return ok },
		PATH:             os.Getenv("PATH"),
		Stat:             os.Stat,
		TokenFile:        config.CheckTokenFile,
	}
	paths, err := config.ResolvePaths(os.Getenv, os.Getenv("HOME"), config.DefaultTempDir(), os.Getuid())
	if err != nil {
		return in, err
	}
	// As resolveCommandPaths, but an unreadable config.json keeps the XDG
	// paths for the configuration rows and leaves the runtime rows unknown.
	rt, err := config.ReadRuntime(ctx, paths)
	if err == nil {
		var applied config.Paths
		if applied, err = applyRuntime(paths, rt); err == nil {
			paths = applied
		}
	}
	in.Paths, in.Runtime, in.RuntimeErr = paths, rt, err
	in.Retain = retainEnabled(paths)
	in.AppDirs = doctorAppDirs(paths)
	in.DesktopSession, in.DesktopOnePassword = desktopSessionCheck(paths), desktopOnePassword(paths)
	if !paths.Headless() && !in.DesktopOnePassword {
		// Desktop mode without the 1Password app is Linux, whose keyring is
		// a Secret Service on the session bus.
		in.KeyringReachable = func() bool { return keyringReachableCheck(paths) }
	}

	filesCtx, cancel := context.WithTimeout(ctx, doctorConfigWait)
	in.Files, in.FilesErr = config.CheckFiles(filesCtx, paths)
	cancel()
	if ctx.Err() != nil {
		return in, ctx.Err()
	}
	if in.Files.State != nil {
		snapshot, err := config.NewSnapshot(*in.Files.State)
		if err != nil {
			in.Files.State, in.Files.StateErr = nil, err
		} else {
			in.Snapshot = &snapshot
		}
	}
	if target != "" && in.Snapshot != nil {
		effective := in.Snapshot.Effective
		ids := make([]string, 0, len(effective.Connections))
		for id := range effective.Connections {
			ids = append(ids, id)
		}
		if in.Target, err = config.ResolveID(target, effective.Aliases, ids); err != nil {
			return in, err
		}
	}
	if in.RuntimeErr == nil && (paths.Headless() || in.DesktopSupported) {
		in.Supervised = paths.Supervised
		if supervised, err := runtimeclient.Supervised(paths); err == nil {
			in.Supervised = supervised
		}
		client := runtimeclient.Client{Paths: paths, Version: version}
		in.Probe, in.ProbeErr = client.Probe(ctx)
	}
	return in, nil
}
