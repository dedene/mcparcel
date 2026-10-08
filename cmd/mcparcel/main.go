package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/dedene/mcparcel/internal/cmd"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

func main() {
	// Every mcparcel process is non-dumpable (D20), not only the daemon: a
	// headless CLI carries the tokenEnv token it passes to the daemon it
	// starts, and a same-uid stdio server must not read it from
	// /proc/<cli>/environ while a call waits. The flag resets on execve.
	runtimeclient.HardenProcess()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cmd.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
