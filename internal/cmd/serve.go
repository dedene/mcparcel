package cmd

import (
	"context"
	"io"

	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

// RuntimeServeCmd runs the runtime in the foreground, for a supervisor that
// owns its lifetime: it takes the daemon lock itself, logs to the daemon log
// and stderr, never exits when idle, and drains and exits 0 on SIGTERM.
type RuntimeServeCmd struct{}

func (c *RuntimeServeCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	paths, err := commandPaths()
	if err != nil {
		return err
	}
	lock, ok, err := runtimeclient.AcquireDaemonLock(ctx, paths)
	if err != nil {
		return err
	}
	if !ok {
		return output.NewError("runtime_busy", nil)
	}
	defer lock.Close()
	log, err := runtimeclient.OpenLog(paths)
	if err != nil {
		return err
	}
	defer log.Close()
	// The daemon log comes first, so a stalled stderr reader cannot cost it a line.
	if err = runDaemon(ctx, paths, lock, io.MultiWriter(log, s.Err), true); err != nil {
		return err
	}
	if opts.JSON {
		return writeSuccess(s, opts, map[string]bool{"stopped": true})
	}
	return writeSuccess(s, opts, "Runtime stopped.\n")
}
