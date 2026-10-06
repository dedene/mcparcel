package cli_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/testutil"
)

func runFixture() int {
	fmt.Fprintln(os.Stderr, "FIXTURE-CHILD-STDERR")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := os.Getenv("MCPARCEL_FIXTURE_ROOT")
	safe := func(key string) string {
		p := os.Getenv(key)
		rel, e := filepath.Rel(root, p)
		if root == "" || e != nil || rel == ".." || strings.HasPrefix(rel, "../") || !filepath.IsAbs(p) {
			return ""
		}
		return p
	}
	started, release, writes := safe("MCPARCEL_FIXTURE_STARTED_FILE"), safe("MCPARCEL_FIXTURE_RELEASE_FILE"), safe("MCPARCEL_FIXTURE_WRITE_FILE")
	names := make(chan string)
	released := make(chan struct{})
	go func() {
		for {
			select {
			case name := <-names:
				if started != "" {
					f, e := os.OpenFile(started, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
					if e == nil {
						_, _ = f.WriteString(name + "\n")
						_ = f.Close()
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if release != "" {
					if _, e := os.Stat(release); e == nil {
						close(released)
						return
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	opts := testutil.FixtureOptions{Started: names, Release: released, OnWrite: func() {
		if writes != "" {
			f, e := os.OpenFile(writes, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if e == nil {
				_, _ = f.WriteString("write\n")
				_ = f.Close()
			}
		}
	}, Crash: func() { os.Exit(9) }, Legacy: os.Getenv("MCPARCEL_FIXTURE_LEGACY") == "1"}
	server := testutil.NewFixtureServerWithOptions(opts)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				if call, ok := req.(*mcp.CallToolRequest); ok && call.Params.Name != "wait" && call.Params.Name != "write_drop" {
					select {
					case names <- call.Params.Name:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
			}
			return next(ctx, method, req)
		}
	})
	if e := server.Run(ctx, &mcp.IOTransport{Reader: os.Stdin, Writer: rawHTMLWriter{os.Stdout}}); e != nil {
		return 1
	}
	return 0
}

// rawHTMLWriter writes the fixture's messages with <, > and & unescaped, as
// servers outside Go do; the SDK writes each message in one Write.
type rawHTMLWriter struct{ w io.Writer }

func (r rawHTMLWriter) Close() error { return nil }

func (r rawHTMLWriter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		if p[i] != '\\' || i+1 >= len(p) {
			out = append(out, p[i])
			continue
		}
		if seq := string(p[i:min(i+6, len(p))]); seq == `\u003c` || seq == `\u003e` || seq == `\u0026` {
			out = append(out, map[string]byte{`\u003c`: '<', `\u003e`: '>', `\u0026`: '&'}[seq])
			i += 5
			continue
		}
		out = append(out, p[i], p[i+1])
		i++
	}
	if _, err := r.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
