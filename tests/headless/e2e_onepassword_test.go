//go:build linux && headless_e2e

package headless_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// garbageToken is the service-account token the image bakes into
// /etc/mcparcel-secrets and /etc/mcparcel-world (testdata/op-token). The
// container has no network, so 1Password never sees it.
// TestE2ENoSecretAnywhere scans for it.
const garbageToken = "e2e-garbage-op-token-canary"

// requireTokenFile fails unless path holds garbageToken with the given owner,
// group and permissions, so the cases below test the layout they claim.
func requireTokenFile(t *testing.T, path string, uid, gid uint32, perm os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Uid != uid || st.Gid != gid || info.Mode().Perm() != perm {
		t.Fatalf("%s is %d:%d %v, want %d:%d %v", path, st.Uid, st.Gid, info.Mode().Perm(), uid, gid, perm)
	}
	if perm&0o044 == 0 {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(raw)) != garbageToken {
		t.Fatalf("%s does not hold the garbage token (%v)", path, err)
	}
}

// TestE2EServiceAccountProfiles covers the four 1Password profiles of
// testdata/config.json. This file sorts before e2e_test.go, so its tests run
// before TestE2ENoSecretAnywhere scans every output.
func TestE2EServiceAccountProfiles(t *testing.T) {
	stopRuntime(t)
	t.Cleanup(func() { stopRuntime(t) })
	call := func(id string) result { return cli(t, callerEnv(), "call", id+".read", "--json") }

	t.Run("token env missing", func(t *testing.T) {
		r := check(t, call("sa-env"), 2, "config_required")
		vars, _ := json.Marshal(r.env.Error.Details["variables"])
		if string(vars) != `["OP_E2E_TOKEN"]` || !strings.Contains(r.stdout, "Set OP_E2E_TOKEN in the environment that starts mcparcel") {
			t.Fatalf("details.variables %s: %s", vars, r.stdout)
		}
	})

	t.Run("garbage token in a Secret volume", func(t *testing.T) {
		link, err := filepath.EvalSymlinks("/etc/mcparcel-secrets/op-token")
		if err != nil || !strings.Contains(link, "/..2026_") {
			t.Fatalf("op-token is not behind ..data: %s %v", link, err)
		}
		requireTokenFile(t, link, 0, 10001, 0o440)
		// The static binary constructs the SDK client (WASM, no CGO), which
		// rejects the token: auth_failed, no panic, no internal_error.
		r := check(t, call("sa-file"), 3, "auth_failed")
		if r.elapsed > 30*time.Second || !strings.Contains(r.stdout, "token from file /etc/mcparcel-secrets/op-token") || strings.Contains(r.stderr, "panic") {
			t.Fatalf("took %v: %s %s", r.elapsed, r.stdout, r.stderr)
		}
		// The unchanged rejected token is answered without a new attempt.
		again := check(t, call("sa-file"), 3, "auth_failed")
		if again.elapsed > 10*time.Second {
			t.Fatalf("retry took %v", again.elapsed)
		}
		status := check(t, cli(t, callerEnv(), "runtime", "status", "--json"), 0, "")
		var s struct {
			Running bool `json:"running"`
		}
		if err := json.Unmarshal(status.env.Data, &s); err != nil || !s.Running {
			t.Fatalf("the runtime did not survive (%v): %s", err, status.stdout)
		}
		log, err := os.ReadFile(filepath.Join(stateRoot, "state", "daemon.log"))
		if err != nil || strings.Contains(string(log), "panic") || strings.Contains(string(log), "internal_error") {
			t.Fatalf("daemon log (%v): %s", err, log)
		}
	})

	t.Run("world-readable token file", func(t *testing.T) {
		requireTokenFile(t, "/etc/mcparcel-world/op-token", 0, 0, 0o444)
		r := check(t, call("sa-world"), 2, "unsafe_local_path")
		if !strings.Contains(r.stdout, "never readable by others") {
			t.Fatal(r.stdout)
		}
	})

	t.Run("desktop-app profile", func(t *testing.T) {
		r := check(t, call("desktop-sa"), 2, "config_required")
		if !strings.Contains(r.stdout, "This connection's 1Password profile uses the desktop app, which headless mode does not use.") {
			t.Fatal(r.stdout)
		}
	})
}
