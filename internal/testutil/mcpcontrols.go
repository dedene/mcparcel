package testutil

import (
	"context"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type TypedInput struct {
	Limit   int64            `json:"limit"`
	Enabled bool             `json:"enabled"`
	Queries []map[string]any `json:"queries"`
}
type (
	EnvInput struct {
		Name string `json:"name"`
	}
	EnvOutput struct {
		Value string `json:"value"`
	}
	// EnvSetOutput says whether a variable is set, never its value, so a
	// live proof with real secrets prints none.
	EnvSetOutput struct {
		Set bool `json:"set"`
	}
	FixtureOptions struct {
		PageSize  int
		Started   chan<- string
		Release   <-chan struct{}
		OnWrite   func()
		Crash     func()
		LookupEnv func(string) string
		// Legacy rejects server/discover, so the client falls back to the
		// initialize handshake that server-to-client requests need.
		Legacy bool
	}
)

func (o FixtureOptions) started(ctx context.Context, name string) error {
	if o.Started == nil {
		return nil
	}
	select {
	case o.Started <- name:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o FixtureOptions) lookup(name string) string {
	if o.LookupEnv != nil {
		return o.LookupEnv(name)
	}
	return os.Getenv(name)
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
