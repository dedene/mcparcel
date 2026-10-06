package runtime

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// TestPoolHTTPFixture is a helper process: an HTTP MCP server the pool did
// not start, with /alive answering 204.
func TestPoolHTTPFixture(t *testing.T) {
	if os.Getenv("MCP_POOL_HTTP_FIXTURE") != "1" {
		return
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		os.Exit(2)
	}
	server := testutil.NewFixtureServer()
	mux := http.NewServeMux()
	mux.HandleFunc("/alive", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	addr := os.Getenv("MCP_POOL_ADDR")
	if e = os.WriteFile(addr+".tmp", []byte("http://"+l.Addr().String()), 0o600); e != nil || os.Rename(addr+".tmp", addr) != nil {
		os.Exit(2)
	}
	_ = http.Serve(l, mux)
	os.Exit(0)
}

// awaitClosed waits until n pooled sessions have closed.
func awaitClosed(t *testing.T, r *poolRig, n int32) {
	t.Helper()
	ctx := testCtx(t)
	for r.closed.Load() != n {
		select {
		case <-ctx.Done():
			t.Fatalf("closed %d want %d", r.closed.Load(), n)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestDisableBeforeDispatch(t *testing.T) {
	for name, tc := range map[string]struct {
		row  config.Selection
		want string
	}{
		"disable": {config.Selection{Enabled: false}, "connection_disabled"},
		"review":  {config.Selection{Enabled: true, ReviewRequired: true}, "review_required"},
	} {
		t.Run(name, func(t *testing.T) {
			editAtList(t, func(r *poolRig) {
				saveSelections(t, r, map[string]config.Selection{"local:a": tc.row, "local:b": {Enabled: true}})
			}, tc.want)
		})
	}
}

func TestBlockedConnectionClosesAfterDrain(t *testing.T) {
	for _, name := range []string{"disabled", "changed", "reverted"} {
		t.Run(name, func(t *testing.T) {
			started := make(chan string, 1)
			release := make(chan struct{})
			r := newRig(t)
			r.http("a", testutil.FixtureOptions{Started: started, Release: release}, false)
			r.http("b", testutil.FixtureOptions{}, false)
			r.http("other", testutil.FixtureOptions{}, false)
			other := r.personal.Connections["other"]
			delete(r.personal.Connections, "other")
			r.start()
			a := asyncCall(r, testCtx(t), "a", "wait")
			<-started
			if name == "changed" {
				r.personal.Connections["a"] = other
				r.save()
			} else {
				saveSelections(t, r, map[string]config.Selection{"local:a": {Enabled: false}, "local:b": {Enabled: true}})
			}
			count(t, r.call(testCtx(t), "b", "counter"))
			if r.closed.Load() != 0 {
				t.Fatal("closed while a call was in flight")
			}
			if name == "reverted" {
				saveSelections(t, r, map[string]config.Selection{"local:a": {Enabled: true}, "local:b": {Enabled: true}})
			}
			close(release)
			success(t, response(t, a))
			if name == "reverted" {
				// The sweep rechecks the current config after the drain and
				// keeps the session a re-enable made current again.
				r.h.(*pool).sweeps.Wait()
				count(t, r.call(testCtx(t), "a", "counter"))
				if r.closed.Load() != 0 || r.connects.Load() != 2 {
					t.Fatal("reverted connection retired", r.closed.Load(), r.connects.Load())
				}
				return
			}
			awaitClosed(t, r, 1)
			if n := count(t, r.call(testCtx(t), "b", "counter")); n != 2 || r.connects.Load() != 2 {
				t.Fatal("unaffected connection reconnected", n, r.connects.Load())
			}
			if name == "disabled" {
				responseCode(t, r.call(testCtx(t), "a", "counter"), "connection_disabled", false)
				if r.connects.Load() != 2 {
					t.Fatal("disabled connection reconnected")
				}
				return
			}
			if n := count(t, r.call(testCtx(t), "a", "counter")); n != 1 || r.connects.Load() != 3 {
				t.Fatal("changed connection did not reopen", n, r.connects.Load())
			}
		})
	}
}

func TestExternalAppNotKilled(t *testing.T) {
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	addr := filepath.Join(t.TempDir(), "addr")
	cmd := exec.Command(exe, "-test.run=^TestPoolHTTPFixture$")
	cmd.Env = append(os.Environ(), "MCP_POOL_HTTP_FIXTURE=1", "MCP_POOL_ADDR="+addr)
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var base string
	for ctx := testCtx(t); base == ""; {
		if b, err := os.ReadFile(addr); err == nil {
			base = strings.TrimSpace(string(b))
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	r := newRig(t)
	r.personal.Connections["a"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(base + "/mcp"), AllowInsecureHTTP: "loopback"}}}
	r.http("b", testutil.FixtureOptions{}, false)
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	saveSelections(t, r, map[string]config.Selection{"local:a": {Enabled: false}, "local:b": {Enabled: true}})
	count(t, r.call(testCtx(t), "b", "counter"))
	awaitClosed(t, r, 1)
	if e = r.h.Shutdown(testCtx(t), true); e != nil {
		t.Fatal(e)
	}
	if e = syscall.Kill(cmd.Process.Pid, 0); e != nil {
		t.Fatal("external server stopped", e)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/alive", nil)
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
}
