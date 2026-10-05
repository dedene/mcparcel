package runtime

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/testutil"
)

func runLockFixture() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := os.WriteFile(os.Getenv("MCP_TEST_CHILD_PID"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return 2
	}
	if err := testutil.NewFixtureServer().Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		return 2
	}
	// EOF closes the protocol session, but only explicit cleanup ends this process.
	<-ctx.Done()
	return 0
}
