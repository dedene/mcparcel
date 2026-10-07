package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

// healthDir is a private state directory under config.DefaultTempDir(), whose ancestors
// pass config.OpenPrivateDir (the per-user temp dir sits behind a symlink).
func healthDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(config.DefaultTempDir(), "mcp-health-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dir := filepath.Join(root, "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock           { return &fakeClock{time.Unix(1_759_700_000, 0)} }
func readBack(t *testing.T, dir string) map[string]ConnectionHealth {
	t.Helper()
	m, err := ReadHealth(dir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHealthBoundedTo50(t *testing.T) {
	dir, clock := healthDir(t), newFakeClock()
	h := NewHealth(dir, clock.now)
	for i := range 60 {
		clock.add(time.Minute)
		if err := h.Record("local:a", HealthEvent{Kind: HealthRefreshed, AccessTTL: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	events := readBack(t, dir)["local:a"].Events
	if len(events) != 50 || events[0].AccessTTL != 10 || events[49].AccessTTL != 59 {
		t.Fatal(len(events), events[0].AccessTTL)
	}
}

func TestHealthFilePrivate(t *testing.T) {
	dir := healthDir(t)
	h := NewHealth(dir, nil)
	if err := h.Record("local:a", HealthEvent{Kind: HealthAuthorized}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(filepath.Join(dir, healthFile))
	if err != nil || st.Mode().Perm() != 0o600 || !st.Mode().IsRegular() {
		t.Fatal(st.Mode(), err)
	}
	// A symlinked file is never read, and writing replaces the link without
	// touching its target.
	target := filepath.Join(filepath.Dir(dir), "target.json")
	other := `{"v":1,"connections":{"local:x":{"events":[{"at":1,"kind":"logout"}]}}}`
	if err := os.WriteFile(target, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, healthFile))
	if err := os.Symlink(target, filepath.Join(dir, healthFile)); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadHealth(dir); err == nil || len(m) != 0 {
		t.Fatal("symlink followed", m, err)
	}
	if err := NewHealth(dir, nil).Record("local:b", HealthEvent{Kind: HealthAuthorized}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != other {
		t.Fatal("symlink target written")
	}
	if m := readBack(t, dir); len(m) != 1 || len(m["local:b"].Events) != 1 {
		t.Fatal(m)
	}
}

func TestHealthByteCapEvictsOldest(t *testing.T) {
	old := healthMaxBytes
	healthMaxBytes = 2048
	t.Cleanup(func() { healthMaxBytes = old })
	dir, clock := healthDir(t), newFakeClock()
	h := NewHealth(dir, clock.now)
	for _, account := range []string{"local:old", "local:mid", "local:new"} {
		for range 8 {
			clock.add(time.Minute)
			if err := h.Record(account, HealthEvent{Kind: HealthRefreshed, Trigger: TriggerKeepAlive, AccessTTL: 3600, RefreshTTL: 1209600}); err != nil {
				t.Fatal(err)
			}
		}
	}
	m := readBack(t, dir)
	if _, ok := m["local:old"]; ok || len(m["local:new"].Events) != 8 {
		t.Fatal("oldest connection kept", len(m))
	}
	if st, _ := os.Stat(filepath.Join(dir, healthFile)); st.Size() > 2048 {
		t.Fatal(st.Size())
	}
}

func TestHealthCorruptStartsEmpty(t *testing.T) {
	for name, content := range map[string]string{
		"json":    "{not json",
		"version": `{"v":2,"connections":{"local:a":{"events":[]}}}`,
		"large":   `{"v":1,"connections":{}}` + strings.Repeat(" ", 1<<20),
	} {
		t.Run(name, func(t *testing.T) {
			dir := healthDir(t)
			if err := os.WriteFile(filepath.Join(dir, healthFile), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if m, err := ReadHealth(dir); err == nil || len(m) != 0 {
				t.Fatal(m, err)
			}
			h := NewHealth(dir, nil)
			if err := h.Record("local:a", HealthEvent{Kind: HealthAuthorized}); err != nil {
				t.Fatal(err)
			}
			if m := readBack(t, dir); len(m["local:a"].Events) != 1 {
				t.Fatal(m)
			}
		})
	}
	if m, err := ReadHealth(filepath.Join(healthDir(t), "missing")); err != nil || len(m) != 0 {
		t.Fatal("missing dir", m, err)
	}
}

func TestHealthIdleComputed(t *testing.T) {
	dir, clock := healthDir(t), newFakeClock()
	h := NewHealth(dir, clock.now)
	_ = h.Record("local:a", HealthEvent{Kind: HealthRefreshFailed, Code: "temporarily_unavailable"})
	_ = h.Record("local:a", HealthEvent{Kind: HealthAuthorized})
	clock.add(36 * time.Hour)
	_ = h.Record("local:a", HealthEvent{Kind: HealthRefreshed})
	clock.add(2 * time.Hour)
	_ = h.Record("local:a", HealthEvent{Kind: HealthRefreshFailed, Code: "Bad Code!"})
	events := readBack(t, dir)["local:a"].Events
	if events[0].Idle != 0 || events[1].Idle != 0 || events[2].Idle != 36*3600 || events[3].Idle != 2*3600 || events[3].Code != "error" {
		t.Fatalf("%+v", events)
	}
	if got := h.LastSuccess("local:a"); !got.Equal(clock.t.Add(-2 * time.Hour)) {
		t.Fatal(got)
	}
	if !h.LastSuccess("local:none").IsZero() {
		t.Fatal("success without events")
	}
	// The clock moved back: idle never goes negative.
	clock.add(-48 * time.Hour)
	_ = h.Record("local:a", HealthEvent{Kind: HealthRefreshed})
	if e := readBack(t, dir)["local:a"].Events[4]; e.Idle != 0 {
		t.Fatal(e.Idle)
	}
}

func TestHealthStaleTempIgnored(t *testing.T) {
	dir := healthDir(t)
	stale := filepath.Join(dir, ".oauth-health-00000000000000000000000000000000.tmp")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHealth(dir, nil)
	if err := h.Record("local:a", HealthEvent{Kind: HealthAuthorized}); err != nil {
		t.Fatal(err)
	}
	if m := readBack(t, dir); len(m["local:a"].Events) != 1 {
		t.Fatal(m)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("temp files left", len(entries))
	}
}

func TestHealthPendingClearedOnSuccess(t *testing.T) {
	dir, clock := healthDir(t), newFakeClock()
	h := NewHealth(dir, clock.now)
	_ = h.Record("local:a", HealthEvent{Kind: HealthAuthorized})
	_ = h.BeginRefresh("local:a")
	if readBack(t, dir)["local:a"].Pending != clock.t.Unix() {
		t.Fatal("pending not written before the request")
	}
	_ = h.Record("local:a", HealthEvent{Kind: HealthRefreshed, Rotated: true})
	if readBack(t, dir)["local:a"].Pending != 0 {
		t.Fatal("pending kept after success")
	}
	// An HTTP error answer or a request that never reached the provider
	// issued no token, so it clears the marker.
	for _, e := range []HealthEvent{{Code: "temporarily_unavailable", HTTPStatus: 503}, {Code: codeUnreachable}} {
		_ = h.BeginRefresh("local:a")
		e.Kind = HealthRefreshFailed
		_ = h.Record("local:a", e)
		if readBack(t, dir)["local:a"].Pending != 0 {
			t.Fatalf("pending kept after %+v", e)
		}
	}
	// A lost response (timeout, reset) may have rotated the token: it keeps
	// the marker, and the next refresh that gets invalid_grant is recorded as
	// interrupted.
	_ = h.BeginRefresh("local:a")
	_ = h.Record("local:a", HealthEvent{Kind: HealthRefreshFailed, Code: "network_error"})
	if readBack(t, dir)["local:a"].Pending == 0 {
		t.Fatal("pending cleared by a transient failure")
	}
	h = NewHealth(dir, clock.now) // a restarted daemon
	_ = h.BeginRefresh("local:a")
	_ = h.Record("local:a", HealthEvent{Kind: HealthRefreshFailed, Code: "invalid_grant", Terminal: true})
	c := readBack(t, dir)["local:a"]
	if last := c.Events[len(c.Events)-1]; !last.Interrupted || c.Pending != 0 {
		t.Fatalf("%+v pending=%d", last, c.Pending)
	}
	// A plain revocation, with no earlier marker, is not interrupted.
	_ = h.BeginRefresh("local:a")
	_ = h.Record("local:a", HealthEvent{Kind: HealthRefreshFailed, Code: "invalid_grant", Terminal: true})
	c = readBack(t, dir)["local:a"]
	if c.Events[len(c.Events)-1].Interrupted {
		t.Fatal("revocation marked interrupted")
	}
	// A provider that does not rotate cannot lose a refresh token.
	_ = h.Record("local:a", HealthEvent{Kind: HealthAuthorized})
	_ = h.Record("local:a", HealthEvent{Kind: HealthRefreshed})
	_ = h.BeginRefresh("local:a")
	_ = h.Record("local:a", HealthEvent{Kind: HealthRefreshFailed, Code: "timeout"})
	h = NewHealth(dir, clock.now)
	_ = h.BeginRefresh("local:a")
	_ = h.Record("local:a", HealthEvent{Kind: HealthRefreshFailed, Code: "invalid_grant", Terminal: true})
	c = readBack(t, dir)["local:a"]
	if c.Events[len(c.Events)-1].Interrupted {
		t.Fatal("non-rotating provider marked interrupted")
	}
	// Logout clears the remembered client.
	_ = h.RememberClient("local:a", "client-1", "http://127.0.0.1:1/callback")
	if hash, redirect := h.Client("local:a"); hash != ClientHash("client-1") || redirect == "" {
		t.Fatal(hash, redirect)
	}
	_ = h.Record("local:a", HealthEvent{Kind: HealthLogout})
	if hash, redirect := NewHealth(dir, nil).Client("local:a"); hash != "" || redirect != "" {
		t.Fatal("client kept after logout")
	}
}

func TestHealthNilAndNoTokenFields(t *testing.T) {
	var h *Health
	if h.Record("a", HealthEvent{}) != nil || h.BeginRefresh("a") != nil || h.RememberClient("a", "x", "y") != nil || !h.LastSuccess("a").IsZero() {
		t.Fatal("nil health did something")
	}
	if hash, _ := h.Client("a"); hash != "" {
		t.Fatal(hash)
	}
	b, _ := json.Marshal(ConnectionHealth{Client: ClientHash("secret-client-id")})
	if strings.Contains(string(b), "secret-client-id") || len(ClientHash("x")) != 12 {
		t.Fatal(string(b))
	}
	if _, err := config.OpenPrivateDir(healthDir(t), false); err != nil {
		t.Fatal("helper dir not private", err)
	}
}
