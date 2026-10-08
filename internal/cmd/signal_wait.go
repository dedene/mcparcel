package cmd

import (
	"context"
	"fmt"
)

// SignalWaitCmd lets the packaging tests check that the npm launcher passes
// signals to the native process.
type SignalWaitCmd struct{}

// Run prints "ready" and blocks until the context is cancelled.
func (c *SignalWaitCmd) Run(ctx context.Context, s *Streams) error {
	fmt.Fprintln(s.Out, "ready")
	<-ctx.Done()
	return ctx.Err()
}
