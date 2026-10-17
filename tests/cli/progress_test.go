package cli_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestSlowFetchProgressBlackBox: a catalog fetch that takes longer than a
// second says so in one stderr line on a terminal (the fixture's marker
// stands in for one) and erases it when done; it prints nothing with --json,
// without a terminal or when the fetch is fast.
func TestSlowFetchProgressBlackBox(t *testing.T) {
	r, a, _, old := catalogFixture(t)
	old.Close()
	var slow atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "/git/blobs/") {
			time.Sleep(time.Duration(slow.Load()))
		}
		a.ServeHTTP(w, req)
	}))
	t.Cleanup(server.Close)
	r.env = replaceEnv(r.env, "MCPARCEL_TEST_GITHUB_API", server.URL)
	r.env = replaceEnv(r.env, "LC_ALL", "C")
	line := "Fetching fixture-owner/fixture.repo..."
	erased := line + "\r" + strings.Repeat(" ", len(line)) + "\r"

	// Fast, on a terminal: nothing.
	if err := os.MkdirAll(r.paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(r.paths.StateDir, "fixture-stderr-terminal")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	v := catalogExecute(t, r, "add", "fixture-owner/fixture.repo")
	if v.code != 0 || v.stderr != "" {
		t.Fatalf("fast add: %d %q", v.code, v.stderr)
	}

	// The catalog file's request waits, so the fetch takes over a second.
	slow.Store(int64(1300 * time.Millisecond))
	if v = catalogExecute(t, r, "add", "fixture-owner/fixture.repo"); v.code != 0 || v.stderr != erased {
		t.Fatalf("slow add: %d %q %q", v.code, v.stdout, v.stderr)
	}
	if v = catalogExecute(t, r, "sync", "--json"); v.code != 0 || v.stderr != "" {
		t.Fatalf("slow sync --json: %d %q", v.code, v.stderr)
	}
	if v = catalogExecute(t, r, "sync"); v.code != 0 || v.stderr != erased {
		t.Fatalf("slow sync: %d %q", v.code, v.stderr)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if v = catalogExecute(t, r, "sync"); v.code != 0 || v.stderr != "" {
		t.Fatalf("slow sync without a terminal: %d %q", v.code, v.stderr)
	}
}
