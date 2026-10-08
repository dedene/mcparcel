//go:build darwin

package spike

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/testutil"
)

const fixtureEnv = "MCPARCEL_FIXTURE_STDIO"

// TestMain re-executes the test binary as a stdio MCP server when asked to.
func TestMain(m *testing.M) {
	if os.Getenv(fixtureEnv) == "1" {
		if err := testutil.NewFixtureServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
		return
	}
	os.Exit(m.Run())
}

func newClient() *mcp.Client {
	return mcp.NewClient(&mcp.Implementation{Name: "mcparcel-spike", Version: "0.0.0"}, nil)
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func structured[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool returned an error result: %+v", result.Content)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("structured content %s: %v", raw, err)
	}
	return out
}

func TestStdioSessionKeepsServerState(t *testing.T) {
	ctx := testContext(t)
	command := exec.Command(os.Args[0])
	command.Env = append(os.Environ(), fixtureEnv+"=1")
	session, err := newClient().Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	for want := int64(1); want <= 2; want++ {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "counter"})
		if err != nil {
			t.Fatal(err)
		}
		if got := structured[testutil.CounterOutput](t, result).Count; got != want {
			t.Fatalf("counter = %d, want %d: the stdio session lost server state", got, want)
		}
	}
}

func TestStreamableHTTPCall(t *testing.T) {
	ctx := testContext(t)
	server := testutil.NewFixtureServer()
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()

	session, err := newClient().Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "héllo"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := structured[testutil.EchoOutput](t, result).Text; got != "héllo" {
		t.Fatalf("echo = %q, want %q", got, "héllo")
	}
}

func TestLegacySSECall(t *testing.T) {
	ctx := testContext(t)
	server := testutil.NewFixtureServer()
	httpServer := httptest.NewServer(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()

	session, err := newClient().Connect(ctx, &mcp.SSEClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "legacy"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := structured[testutil.EchoOutput](t, result).Text; got != "legacy" {
		t.Fatalf("echo = %q, want %q", got, "legacy")
	}
}

// TestStreamableAgainstSSEOnlyServerFails records how the SDK reports a
// transport mismatch. The logged error text is evidence:
// stage 5 uses it to decide when a legacy SSE fallback is allowed.
func TestStreamableAgainstSSEOnlyServerFails(t *testing.T) {
	ctx := testContext(t)
	server := testutil.NewFixtureServer()
	httpServer := httptest.NewServer(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()

	session, err := newClient().Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL, MaxRetries: -1}, nil)
	if err == nil {
		session.Close()
		t.Fatal("streamable client connected to an SSE-only server; the SDK falls back on its own")
	}
	t.Logf("mismatch error (%T): %v", err, err)
}
