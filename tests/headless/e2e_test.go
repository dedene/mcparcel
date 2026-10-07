//go:build linux && headless_e2e

// Package headless_test is the end-to-end proof of headless mode in a Linux
// container shaped like the target pod (tests/headless/Dockerfile, run with
// make test-headless-e2e): a read-only root fs, /etc/mcparcel laid out like
// a ConfigMap, the state root on a world-writable tmpfs, uid 10001 without a
// passwd entry, and a reaping PID 1 (--init). The fake Front runs in this
// process on fixed loopback ports; mcparcel is the real static binary.
package headless_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	mcparcel  = "/opt/mcparcel/mcparcel"
	configDir = "/etc/mcparcel"
	stateRoot = "/var/lib/mcparcel"
	home      = "/tmp/home"
)

var (
	front        *fakeFront
	configBefore string

	outputsMu sync.Mutex
	outputs   []string // every captured stdout and stderr, for the leak check
)

func TestMain(m *testing.M) {
	if _, err := os.Lstat(filepath.Join(configDir, "..data")); err != nil {
		fmt.Fprintln(os.Stderr, "tests/headless runs only in its image: make test-headless-e2e")
		os.Exit(2)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var err error
	if configBefore, err = snapshot(configDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if front, err = startFakeFront(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	code := m.Run()
	_, _ = exec.Command(mcparcel, "runtime", "stop", "--force", "--json").Output()
	front.close()
	os.Exit(code)
}

// callerEnv is the environment of a direct caller: the sidecar's, with the
// Front credentials. Names in drop are left out.
func callerEnv(drop ...string) []string {
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "XDG_CONFIG_HOME=/etc", "FRONT_CLIENT_ID=" + clientID, "FRONT_CLIENT_SECRET=" + clientSecret}
	return slices.DeleteFunc(env, func(v string) bool {
		return slices.ContainsFunc(drop, func(name string) bool { return strings.HasPrefix(v, name+"=") })
	})
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

type result struct {
	code           int
	stdout, stderr string
	env            envelope
	elapsed        time.Duration
}

func record(texts ...string) {
	outputsMu.Lock()
	defer outputsMu.Unlock()
	outputs = append(outputs, texts...)
}

// runE runs bin with args and env and parses stdout as one JSON envelope.
func runE(bin string, env []string, args ...string) (result, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = env, home, &stdout, &stderr
	started := time.Now()
	err := cmd.Run()
	r := result{stdout: stdout.String(), stderr: stderr.String(), elapsed: time.Since(started)}
	record(r.stdout, r.stderr)
	if exit, ok := err.(*exec.ExitError); ok {
		r.code = exit.ExitCode()
	} else if err != nil {
		return r, fmt.Errorf("%v: %w", args, err)
	}
	if lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n"); len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &r.env) != nil {
		return r, fmt.Errorf("%v: exit %d, stdout is not one JSON envelope: %q (stderr %q)", args, r.code, r.stdout, r.stderr)
	}
	return r, nil
}

func run(t *testing.T, bin string, env []string, args ...string) result {
	t.Helper()
	r, err := runE(bin, env, args...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func cli(t *testing.T, env []string, args ...string) result {
	t.Helper()
	return run(t, mcparcel, env, args...)
}

// check fails unless r exited with code and, when errCode is set, carries
// that error code (else ok:true).
func check(t *testing.T, r result, code int, errCode string) result {
	t.Helper()
	got := ""
	if r.env.Error != nil {
		got = r.env.Error.Code
	}
	if r.code != code || got != errCode || r.env.OK != (errCode == "") {
		t.Fatalf("exit %d error %q, want %d %q\nstdout %s\nstderr %s", r.code, got, code, errCode, r.stdout, r.stderr)
	}
	return r
}

// stopRuntime stops the daemon, so the next call starts a fresh one with no
// token.
func stopRuntime(t *testing.T) {
	t.Helper()
	check(t, cli(t, callerEnv(), "runtime", "stop", "--force", "--json"), 0, "")
}

func readConversation(t *testing.T) result {
	t.Helper()
	return check(t, cli(t, callerEnv(), "call", "front.read_conversation", "id=1", "--json"), 0, "")
}

func delta(t *testing.T, before counts, grants, unauthorized int, tools map[string]int) {
	t.Helper()
	after := front.counts()
	if after.Grants-before.Grants != grants || after.Unauthorized-before.Unauthorized != unauthorized {
		t.Fatalf("grants +%d, 401s +%d; want +%d, +%d", after.Grants-before.Grants, after.Unauthorized-before.Unauthorized, grants, unauthorized)
	}
	for tool, n := range tools {
		if got := after.Tools[tool] - before.Tools[tool]; got != n {
			t.Fatalf("%s invoked +%d, want +%d", tool, got, n)
		}
	}
}

func TestE2EConcurrentCallsShareOneToken(t *testing.T) {
	stopRuntime(t)
	before := front.counts()
	var wg sync.WaitGroup
	results, errs := make([]result, 2), make([]error, 2)
	for i := range results {
		wg.Go(func() {
			results[i], errs[i] = runE(mcparcel, callerEnv(), "call", "front.read_conversation", "id=1", "--json")
		})
	}
	wg.Wait()
	for i, r := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		check(t, r, 0, "")
	}
	delta(t, before, 1, 0, map[string]int{"read_conversation": 2})
}

func TestE2EProactiveRefresh(t *testing.T) {
	front.setExpiresIn(6)
	t.Cleanup(func() { front.setExpiresIn(900); stopRuntime(t) })
	stopRuntime(t)
	before := front.counts()
	readConversation(t)
	time.Sleep(4 * time.Second)
	readConversation(t)
	delta(t, before, 2, 0, map[string]int{"read_conversation": 2})
}

func TestE2E401RemintAndResend(t *testing.T) {
	readConversation(t)
	before := front.counts()
	front.revoke()
	readConversation(t)
	delta(t, before, 1, 1, map[string]int{"read_conversation": 1})
}

func runtimeEntries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateRoot, "run"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// requireStopped fails unless no daemon runs: runtime status reports
// running:false and the runtime dir holds no daemon.sock. It returns the
// runtime dir entries, taken after the status call.
func requireStopped(t *testing.T) []string {
	t.Helper()
	r := check(t, cli(t, callerEnv(), "runtime", "status", "--json"), 0, "")
	var status struct {
		Running *bool `json:"running"`
	}
	if err := json.Unmarshal(r.env.Data, &status); err != nil || status.Running == nil || *status.Running {
		t.Fatalf("want a stopped runtime (%v): %s", err, r.stdout)
	}
	entries := runtimeEntries(t)
	if slices.Contains(entries, "daemon.sock") {
		t.Fatalf("daemon.sock left in the runtime dir: %v", entries)
	}
	return entries
}

// requireNoRuntimeContact runs a call the policy denies with the daemon
// stopped and fails unless the call exits 4 tool_denied while leaving the
// runtime dir untouched and no daemon running (D9: the CLI checks the policy
// offline). The caller stops the runtime first.
func requireNoRuntimeContact(t *testing.T, call func() result) result {
	t.Helper()
	entries := requireStopped(t)
	r := check(t, call(), 4, "tool_denied")
	if got := runtimeEntries(t); !slices.Equal(got, entries) {
		t.Fatalf("denied call touched the runtime dir: %v -> %v", entries, got)
	}
	requireStopped(t)
	return r
}

func TestE2EBlockedToolRefused(t *testing.T) {
	stopRuntime(t) // a running daemon would hide a CLI that asks it
	before := front.counts()
	r := requireNoRuntimeContact(t, func() result {
		return cli(t, callerEnv(), "call", "front.send_message", "to=customer", "body=hi", "--json")
	})
	if after := front.counts(); after.MCP != before.MCP || after.Grants != before.Grants || after.Tools["send_message"] != 0 {
		t.Fatalf("denied call reached Front: %+v -> %+v", before, after)
	}
	if strings.Contains(r.stdout, "customer") {
		t.Fatal(r.stdout)
	}
}

func toolNames(t *testing.T, r result) []string {
	t.Helper()
	var data struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(r.env.Data, &data); err != nil {
		t.Fatal(err, r.stdout)
	}
	var names []string
	for _, item := range data.Items {
		names = append(names, item.Name)
	}
	return names
}

func TestE2EToolsListsAllowedOnly(t *testing.T) {
	names := toolNames(t, check(t, cli(t, callerEnv(), "tools", "front", "--json"), 0, ""))
	slices.Sort(names)
	if want := []string{"ask_approval", "create_draft", "read_conversation"}; !slices.Equal(names, want) {
		t.Fatalf("tools %v, want %v", names, want)
	}
}

func TestE2EApprovalDeclinedNoHang(t *testing.T) {
	readConversation(t) // a running daemon, so the timing covers only the call
	r := check(t, cli(t, callerEnv(), "call", "front.ask_approval", "--json"), 0, "")
	if r.elapsed > 3*time.Second {
		t.Fatalf("call took %v", r.elapsed)
	}
	var data struct {
		Warnings []struct {
			Code string `json:"code"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal(r.env.Data, &data); err != nil || len(data.Warnings) != 1 || data.Warnings[0].Code != "elicitation_declined" || !strings.Contains(r.stdout, "action=decline") {
		t.Fatalf("want one elicitation_declined warning and a declined elicitation (%v): %s", err, r.stdout)
	}
}

func TestE2EMissingSecret(t *testing.T) {
	stopRuntime(t)
	t.Cleanup(func() { stopRuntime(t) })
	before := front.counts()
	r := check(t, cli(t, callerEnv("FRONT_CLIENT_SECRET"), "call", "front.read_conversation", "id=1", "--json"), 2, "config_required")
	vars, _ := json.Marshal(r.env.Error.Details["variables"])
	if string(vars) != `["FRONT_CLIENT_SECRET"]` {
		t.Fatalf("details.variables %s: %s", vars, r.stdout)
	}
	delta(t, before, 0, 0, map[string]int{"read_conversation": 0})
}

// snapshot describes a tree: every entry's path, type, mode, owner, symlink
// target and content hash.
func snapshot(root string) (string, error) {
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		fmt.Fprintf(&b, "%s %v %d:%d", path, info.Mode(), st.Uid, st.Gid)
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, " -> %s", target)
		case info.Mode().IsRegular():
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, " %x", sha256.Sum256(raw))
		}
		b.WriteByte('\n')
		return nil
	})
	return b.String(), err
}

// TestE2EReadOnlyConfigUntouched runs after every other test: the ConfigMap
// layout is byte-identical, with no lock file, snapshot or migration added.
func TestE2EReadOnlyConfigUntouched(t *testing.T) {
	after, err := snapshot(configDir)
	if err != nil || after != configBefore {
		t.Fatalf("%s changed (%v):\nbefore\n%s\nafter\n%s", configDir, err, configBefore, after)
	}
	t.Logf("%s:\n%s", configDir, after)
}

// TestE2ENoSecretAnywhere runs last: neither the client secret nor any token
// the fake issued is in a captured output, the daemon log or any file below
// the state root.
func TestE2ENoSecretAnywhere(t *testing.T) {
	secrets := append([]string{clientSecret}, front.issuedTokens()...)
	if len(secrets) < 3 {
		t.Fatalf("only %d tokens issued", len(secrets)-1)
	}
	outputsMu.Lock()
	texts := slices.Clone(outputs)
	outputsMu.Unlock()
	files := map[string]bool{}
	err := filepath.WalkDir(stateRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = true
		texts = append(texts, string(raw))
		return nil
	})
	if err != nil || !files[filepath.Join(stateRoot, "state", "daemon.log")] {
		t.Fatalf("daemon.log not scanned (%v): %v", err, files)
	}
	for _, text := range texts {
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("secret or token leaked: %q", text)
			}
		}
	}
	t.Logf("scanned %d outputs and %d files for %d values", len(outputs), len(files), len(secrets))
}
