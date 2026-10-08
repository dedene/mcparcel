package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// headlessEnv writes a headless config.json whose state root is a fresh
// world-writable directory, as kubelet creates an emptyDir.
func headlessEnv(t *testing.T) (config.Paths, string) { return headlessEnvMode(t, 0o777) }

// headlessEnvMode is headlessEnv with the root's mode: 0777, or 2777 when the
// pod sets fsGroup (macOS drops the setgid bit when the group is not ours).
func headlessEnvMode(t *testing.T, mode os.FileMode) (config.Paths, string) {
	t.Helper()
	p := metadataEnv(t)
	root := filepath.Join(filepath.Dir(p.Home), "state-root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, mode); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "runtime": map[string]any{"mode": "headless", "stateRoot": root}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p.ConfigFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, root
}

func envelopeError(t *testing.T, stdout string) output.Error {
	t.Helper()
	var envelope struct {
		Error *output.Error `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || envelope.Error == nil {
		t.Fatal(stdout, err)
	}
	return *envelope.Error
}

func TestCommandPathsHeadlessStateRoot(t *testing.T) {
	p, root := headlessEnv(t)
	got, err := commandPaths()
	if err != nil {
		t.Fatal(err)
	}
	if got.StateRoot != root || got.RuntimeDir != root+"/run" || got.StateDir != root+"/state" || got.DataDir != root+"/data" || got.CacheDir != root+"/cache" {
		t.Fatalf("%+v", got)
	}
	if got.ConfigDir != p.ConfigDir || got.ConfigFile != p.ConfigFile || got.Home != p.Home {
		t.Fatalf("%+v", got)
	}
	if err = os.Remove(p.ConfigFile); err != nil {
		t.Fatal(err)
	}
	if got, err = commandPaths(); err != nil || got != p {
		t.Fatalf("desktop paths changed: %+v %v", got, err)
	}
}

func TestCommandPathsSupervised(t *testing.T) {
	p, root := headlessEnv(t)
	if got, err := commandPaths(); err != nil || got.Supervised {
		t.Fatalf("%+v %v", got, err)
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "runtime": map[string]any{"mode": "headless", "stateRoot": root, "supervised": true}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p.ConfigFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := commandPaths(); err != nil || !got.Supervised || got.StateRoot != root {
		t.Fatalf("%+v %v", got, err)
	}
}

// desktopSessionIs makes this process have a desktop session (a display or
// session bus) or not for the test.
func desktopSessionIs(t *testing.T, session bool) {
	t.Helper()
	saved := desktopSessionCheck
	t.Cleanup(func() { desktopSessionCheck = saved })
	desktopSessionCheck = func(config.Paths) bool { return session }
}

// Desktop mode runs on macOS and Linux. Without a desktop session and
// without config.json (Linux in a misconfigured container) runtime commands,
// runtime serve included, refuse with runtime_unsupported; with a config.json
// they run.
func TestRuntimeCommandModeByPlatform(t *testing.T) {
	p := metadataEnv(t)
	desktopSessionIs(t, true)
	if code, stdout, stderr := run(t, "runtime", "status", "--json"); code != 0 || stderr != "" {
		t.Fatal(code, stdout, stderr)
	}
	desktopSessionIs(t, false)
	want := output.LinuxNoSessionError()
	for _, argv := range [][]string{{"runtime", "status", "--json"}, {"runtime", "serve", "--json"}, {"call", "paper.x", "--json"}, {"auth", "lock", "--json"}} {
		code, stdout, stderr := run(t, argv...)
		if e := envelopeError(t, stdout); code != 2 || e.Code != "runtime_unsupported" || e.Message != want.Message || e.NextAction != want.NextAction || stderr != "" {
			t.Fatal(argv, code, stdout, stderr)
		}
	}
	noRuntime(t, p)
	// Offline commands work in desktop mode on every platform.
	if code, stdout, stderr := run(t, "list", "--json"); code != 0 || stderr != "" {
		t.Fatal(code, stdout, stderr)
	}
	if err := os.WriteFile(p.ConfigFile, []byte(`{"schemaVersion":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := run(t, "runtime", "status", "--json"); code != 0 || stderr != "" {
		t.Fatal("desktop runtime status with config.json", code, stdout, stderr)
	}
	headlessEnv(t)
	if code, stdout, stderr := run(t, "runtime", "status", "--json"); code != 0 || stderr != "" {
		t.Fatal("headless runtime status", code, stdout, stderr)
	}
}

func TestHeadlessErrorMapping(t *testing.T) {
	if e := safeFailure(config.ErrHeadlessOnly); *e != *output.HeadlessOnlyError() {
		t.Fatal(e)
	}
	e := safeFailure(errors.Join(errors.New("wrapped"), config.ErrConfigReadOnly))
	if e.Code != "config_read_only" || output.ExitCode(e) != 2 {
		t.Fatal(e)
	}
}

// TestHeadlessSetgidStateRoot: an fsGroup emptyDir (2777) works as a state
// root through the command layer, the fixture's StateDir opens included.
func TestHeadlessSetgidStateRoot(t *testing.T) {
	_, root := headlessEnvMode(t, 0o777|os.ModeSetgid)
	got, err := commandPaths()
	if err != nil || got.StateRoot != root {
		t.Fatal(got, err)
	}
	for range 2 {
		for _, dir := range []string{got.StateDir, got.RuntimeDir} {
			f, err := config.OpenPrivateDirUnder(got.StateRoot, dir, true)
			if err != nil {
				t.Fatal(dir, err)
			}
			_ = f.Close()
		}
	}
	if code, stdout, stderr := run(t, "runtime", "status", "--json"); code != 0 || stderr != "" {
		t.Fatal(code, stdout, stderr)
	}
}
