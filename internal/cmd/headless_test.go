package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// headlessEnv writes a headless config.json whose state root is a fresh
// world-writable directory, as kubelet creates an emptyDir.
func headlessEnv(t *testing.T) (config.Paths, string) {
	t.Helper()
	p := metadataEnv(t)
	root := filepath.Join(filepath.Dir(p.Home), "state-root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
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

func TestRuntimeCommandModeByPlatform(t *testing.T) {
	metadataEnv(t)
	code, stdout, stderr := run(t, "runtime", "status", "--json")
	if goruntime.GOOS == "darwin" {
		if code != 0 || stderr != "" {
			t.Fatal(code, stdout, stderr)
		}
	} else {
		e := envelopeError(t, stdout)
		if code != 2 || e.Code != "runtime_unsupported" || e.Message != "On Linux, MCParcel runs in headless mode only." || stderr != "" {
			t.Fatal(code, stdout, stderr)
		}
	}
	// Offline commands work in desktop mode on every platform.
	if code, stdout, stderr = run(t, "list", "--json"); code != 0 || stderr != "" {
		t.Fatal(code, stdout, stderr)
	}
	headlessEnv(t)
	if code, stdout, stderr = run(t, "runtime", "status", "--json"); code != 0 || stderr != "" {
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
