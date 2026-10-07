package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

const (
	frontClientID = "front-client-canary"
	frontSecret   = "FRONT-SECRET-CANARY"
	wrongSecret   = "WRONG-SECRET-CANARY"
	frontTokens   = "FRONT-TOKEN-CANARY-"
)

// frontRig is a headless rig: config.json selects headless mode with a
// world-writable state root, and connection front is the fixture MCP server
// behind a client_credentials authorization server. FRONT_CLIENT_ID and
// FRONT_CLIENT_SECRET are in the caller's environment. front allows counter
// and echo but denies echo. Every output is checked for the client ID, the
// secrets and the issued tokens.
type frontRig struct {
	*rig
	as      *testutil.AuthServer
	mcp     atomic.Int32 // requests that reached the MCP endpoint
	outputs []string
}

func newFrontRig(t *testing.T, runtime map[string]any) *frontRig {
	t.Helper()
	r := &frontRig{rig: newRig(t)}
	r.as = testutil.NewAuthServer(t, testutil.AuthServerOptions{ClientCredentials: true, ClientID: frontClientID, ClientSecret: frontSecret, CCExpiresIn: 900, TokenPrefix: frontTokens})
	server := testutil.NewFixtureServer()
	protected := r.as.Protect(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil), "/mcp")
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mcp.Add(1)
		protected.ServeHTTP(w, req)
	}))
	r.closers = append(r.closers, h.Close)
	allow := []string{"counter", "echo"}
	r.personal.Connections["front"] = config.Connection{
		Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(h.URL + "/mcp"), AllowInsecureHTTP: "loopback"}},
		Auth: &config.OAuth{
			Type: "oauth", Grant: config.GrantClientCredentials, TokenURL: r.as.URL + "/token",
			ClientID:     &config.Value{Secret: &config.SecretRef{Secret: "env:FRONT_CLIENT_ID"}},
			ClientSecret: &config.Value{Secret: &config.SecretRef{Secret: "env:FRONT_CLIENT_SECRET"}},
		},
		ToolPolicy: &config.ToolPolicy{Allow: &allow, Deny: []string{"echo"}},
	}
	r.save()
	root := filepath.Join(r.root, "sr")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if runtime == nil {
		runtime = map[string]any{}
	}
	runtime["mode"], runtime["stateRoot"] = "headless", root
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "runtime": runtime})
	if err != nil {
		t.Fatal(err)
	}
	r.write(r.paths.ConfigFile, string(raw), 0o600)
	if r.paths, err = config.ApplyStateRoot(r.paths, root); err != nil {
		t.Fatal(err)
	}
	r.env = append(r.env, "FRONT_CLIENT_ID="+frontClientID, "FRONT_CLIENT_SECRET="+frontSecret)
	// Registered after newRig, so it runs before the rig removes its root.
	t.Cleanup(r.checkLeaks)
	return r
}

func (r *frontRig) run(args ...string) result {
	v := r.rig.run(args...)
	r.outputs = append(r.outputs, v.stdout, v.stderr)
	return v
}

// setEnv replaces name in the caller's environment; "" removes it.
func (r *frontRig) setEnv(name, value string) {
	out := slices.DeleteFunc(slices.Clone(r.env), func(v string) bool { return strings.HasPrefix(v, name+"=") })
	if value != "" {
		out = append(out, name+"="+value)
	}
	r.env = out
}

func (r *frontRig) checkLeaks() {
	texts := slices.Clone(r.outputs)
	_ = filepath.WalkDir(r.paths.StateRoot, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, e := os.ReadFile(path); e == nil {
				texts = append(texts, string(b))
			}
		}
		return nil
	})
	for _, text := range texts {
		for _, canary := range []string{frontClientID, frontSecret, wrongSecret, frontTokens} {
			if strings.Contains(text, canary) {
				r.t.Errorf("%s leaked: %q", canary, text)
			}
		}
	}
}

// noRuntimeContact fails when a runtime directory entry, a daemon process,
// an MCP request or a token request exists.
func (r *frontRig) noRuntimeContact() {
	r.t.Helper()
	if entries, err := os.ReadDir(r.paths.RuntimeDir); err == nil && len(entries) > 0 || err != nil && !errors.Is(err, os.ErrNotExist) {
		r.t.Fatal("runtime directory used", entries, err)
	}
	if pids := r.daemonPIDs(); len(pids) != 0 {
		r.t.Fatal("daemon started", pids)
	}
	if r.mcp.Load() != 0 || r.as.Requests("/token") != 0 {
		r.t.Fatal("network contact", r.mcp.Load(), r.as.Requests("/token"))
	}
}

func TestDeniedToolNoRuntimeContact(t *testing.T) {
	r := newFrontRig(t, nil)
	v := r.check(r.run("call", "front.echo", "message=hi", "--json"), 4, "tool_denied")
	if strings.Contains(v.stdout, "hi") {
		t.Fatal(v.stdout)
	}
	// A tool outside the allow list is denied the same way.
	r.check(r.run("call", "front.typed", "--json"), 4, "tool_denied")
	r.noRuntimeContact()
}

func TestDisabledConnectionNoRuntimeContact(t *testing.T) {
	r := newFrontRig(t, nil)
	r.write(r.paths.SelectionsFile, `{"schemaVersion":1,"revision":1,"connections":{"local:front":{"enabled":false},"local:fixture":{"enabled":true}}}`, 0o600)
	r.check(r.run("call", "front.counter", "--json"), 4, "connection_disabled")
	r.noRuntimeContact()
}

// TestHeadlessCallNeverPrompts: stdin and stderr on a terminal, no
// --no-input, a typed answer and an approval dialog ready: headless mode
// still declines the server's request at once and asks nobody.
func TestHeadlessCallNeverPrompts(t *testing.T) {
	r := newFrontRig(t, map[string]any{"approvalDialog": true})
	r.check(r.run("call", "fixture.counter", "--json"), 0, "")
	r.write(r.paths.StateDir+"/fixture-terminal", "2\n", 0o600)
	r.write(r.paths.StateDir+"/fixture-dialog-answer", "Allow once\n", 0o600)
	master, slavePath, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(master) })
	slave, err := os.OpenFile(slavePath, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	cli := exec.Command(r.bin+"/cli-a", "call", "fixture.elicit", dirty, "schema=none", "persist=session")
	cli.Env, cli.Dir = append([]string(nil), r.env...), r.paths.Home
	cli.Stdin, cli.Stdout, cli.Stderr = slave, &stdout, slave
	cli.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	started := time.Now()
	if err = cli.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	var stderr bytes.Buffer
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 4096)
		for {
			n, err := unix.Read(master, buf)
			if n <= 0 || err != nil {
				return
			}
			stderr.Write(buf[:n])
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cli.Wait() }()
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		_ = cli.Process.Kill()
		t.Fatal("headless call waited for an answer")
	}
	elapsed := time.Since(started)
	<-readerDone
	r.outputs = append(r.outputs, stdout.String(), stderr.String())
	if err != nil || stdout.String() != "action=decline\n" || elapsed > 2*time.Second {
		t.Fatalf("%v %q %q %v", err, stdout.String(), stderr.String(), elapsed)
	}
	if !strings.Contains(stderr.String(), "no prompt was possible") || strings.Contains(stderr.String(), "asks:") || strings.Contains(stderr.String(), "Choice") {
		t.Fatalf("stderr %q", stderr.String())
	}
	if _, err = os.Stat(r.paths.StateDir + "/fixture-dialog-args"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("approval dialog shown in headless mode", err)
	}
	// The request carried no prompt: the daemon declined without forwarding.
	log, err := os.ReadFile(r.paths.LogFile)
	if err != nil || !strings.Contains(string(log), `{"event":"elicitation_declined"}`) || strings.Contains(string(log), "elicitation_forwarded") {
		t.Fatal(string(log), err)
	}
}

var revision = regexp.MustCompile(`"sourceRevisions":\{"personal":"[0-9a-f]+"\}`)

// golden compares stdout with tests/cli/testdata/headless/<name>.json;
// MCPARCEL_UPDATE_GOLDEN=1 rewrites it.
func (r *frontRig) golden(name string, v result) {
	r.t.Helper()
	got := revision.ReplaceAllString(v.stdout, `"sourceRevisions":{"personal":"<revision>"}`)
	path := filepath.Join(repoRoot(), "tests", "cli", "testdata", "headless", name+".json")
	if os.Getenv("MCPARCEL_UPDATE_GOLDEN") == "1" {
		r.write(path, got, 0o644)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != got {
		r.t.Fatalf("%s: got %s\nwant %s (%v)", name, got, want, err)
	}
	for _, canary := range []string{frontClientID, frontSecret, wrongSecret, frontTokens} {
		if strings.Contains(string(want), canary) {
			r.t.Fatalf("%s holds %s", path, canary)
		}
	}
}

// TestHeadlessJSONContract pins the agent-facing envelopes of headless mode.
func TestHeadlessJSONContract(t *testing.T) {
	r := newFrontRig(t, nil)
	// The daemon starts with the caller's forwarded environment (D11).
	r.setEnv("FRONT_CLIENT_SECRET", "")
	v := r.check(r.run("call", "front.counter", "--json"), 2, "config_required")
	r.golden("config_required", v)
	r.setEnv("FRONT_CLIENT_SECRET", frontSecret)
	r.check(r.run("runtime", "restart", "--json"), 0, "")
	r.golden("tools", r.check(r.run("tools", "front", "--json"), 0, ""))
	r.golden("call", r.check(r.run("call", "front.counter", "--json"), 0, ""))
	r.golden("tool_denied", r.check(r.run("call", "front.echo", "message=hi", "--json"), 4, "tool_denied"))
	r.setEnv("FRONT_CLIENT_SECRET", wrongSecret)
	r.check(r.run("runtime", "restart", "--json"), 0, "")
	v = r.check(r.run("call", "front.counter", "--json"), 3, "auth_failed")
	if !strings.Contains(v.stdout, "invalid_client") {
		t.Fatal(v.stdout)
	}
	r.golden("auth_failed", v)
	r.golden("config_read_only", r.check(r.run("disable", "front", "--json"), 2, "config_read_only"))
	if c := r.as.GrantCounts(); c.ClientCredentials != 1 {
		t.Fatalf("%+v", c)
	}
	log, err := os.ReadFile(r.paths.LogFile)
	if err != nil || !strings.Contains(string(log), `{"event":"oauth_token_minted","trigger":"first","ttl":900}`) || !strings.Contains(string(log), `{"event":"oauth_token_mint_failed","code":"invalid_client","status":401}`) {
		t.Fatal(string(log), err)
	}
}
