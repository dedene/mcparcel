package cmd

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// doctorEnv is metadataEnv with doctor's app directories inside the test root.
func doctorEnv(t *testing.T) config.Paths {
	t.Helper()
	p := metadataEnv(t)
	dirs := doctorAppDirs
	t.Cleanup(func() { doctorAppDirs = dirs })
	doctorAppDirs = func(paths config.Paths) []string { return []string{paths.StateDir + "/apps"} }
	return p
}

func TestDoctorLiveNeedsConnection(t *testing.T) {
	p := doctorEnv(t)
	// An unreadable config.json would fail runtime.mode (exit 8): the usage
	// error comes first, before any file is read.
	if err := os.WriteFile(p.ConfigFile, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, "doctor", "--live", "--json")
	if code != ExitUsage || stderr != "" || envelopeError(t, stdout).Code != "invalid_arguments" {
		t.Fatal(code, stdout, stderr)
	}
	code, _, stderr = run(t, "doctor", "--live")
	if code != ExitUsage || !strings.Contains(stderr, "Invalid command arguments.") {
		t.Fatal(code, stderr)
	}
}

func TestDoctorUnknownConnection(t *testing.T) {
	p := doctorEnv(t)
	personal := `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"stdio","command":"/bin/sh"}},"paper2":{"transport":{"type":"stdio","command":"/bin/sh"}}}}`
	if err := os.WriteFile(p.PersonalFile, []byte(personal), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := run(t, "doctor", "nope", "--json")
	if code != 4 || envelopeError(t, stdout).Code != "connection_unavailable" {
		t.Fatal(code, stdout)
	}
	code, stdout, _ = run(t, "doctor", "local:paper", "--json")
	var env struct {
		Data output.DoctorData `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil || code != 0 && code != 8 {
		t.Fatal(code, stdout, err)
	}
	for _, c := range env.Data.Checks {
		if c.Subject == "local:paper2" {
			t.Fatal("row for another connection", c)
		}
	}
}

func TestDoctorJSONUsageEnvelope(t *testing.T) {
	doctorEnv(t)
	code, stdout, stderr := run(t, "doctor", "a", "b", "--json")
	if code != ExitUsage || stderr != "" || envelopeError(t, stdout).Code != "invalid_arguments" {
		t.Fatal(code, stdout, stderr)
	}
	code, stdout, stderr = run(t, "doctor", "--bogus", "--json")
	if code != ExitUsage || stderr != "" || envelopeError(t, stdout).Code != "invalid_arguments" {
		t.Fatal(code, stdout, stderr)
	}
}

// Offline doctor builds no credential source and no dialog, even for a
// configuration full of 1Password references and OAuth connections.
func TestDoctorConstructsNoCredentialSource(t *testing.T) {
	p := doctorEnv(t)
	calls := spyFactories(t)
	dialog := newDialogFactory
	t.Cleanup(func() { newDialogFactory = dialog })
	newDialogFactory = func(config.Paths) func(context.Context, []string) (string, error) {
		t.Fatal("doctor built the approval dialog")
		return nil
	}
	personal := `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{
		"op":{"credentialProfile":"team","transport":{"type":"stdio","command":"/bin/sh","env":{"K":{"secret":"op://v/i/f"},"E":{"secret":"env:SOME_KEY"}}}},
		"web":{"transport":{"type":"http","url":"https://a.example.invalid/mcp"},"auth":{"type":"oauth"}}}}`
	local := `{"schemaVersion":1,"credentialProfiles":{"p":{"mode":"desktop-service-account","account":"a","bootstrapRef":"op://v/i/token"}}}`
	selections := `{"schemaVersion":1,"revision":1,"connections":{"local:op":{"enabled":true,"credentialProfile":"p"},"local:web":{"enabled":true}}}`
	for path, body := range map[string]string{p.PersonalFile: personal, p.ConfigFile: local, p.SelectionsFile: selections} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, stdout, stderr := run(t, "doctor", "--json")
	if code != 8 || stderr != "" {
		t.Fatal(code, stdout, stderr)
	}
	var env struct {
		Data  output.DoctorData `json:"data"`
		Error output.Error      `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil || env.Error.Code != "doctor_failed" || len(env.Data.Checks) == 0 {
		t.Fatal(stdout, err)
	}
	if *calls != [3]int{} {
		t.Fatal("credential source built", *calls)
	}
	if _, err := os.Stat(p.RuntimeDir + "/daemon.sock"); !os.IsNotExist(err) {
		t.Fatal("doctor started a runtime", err)
	}
}

// A canceled doctor exits 130 (canceled), never 8 with rows its own
// cancellation failed.
func TestDoctorCanceledExits130(t *testing.T) {
	doctorEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, args := range [][]string{{"doctor", "--json"}, {"doctor", "--json", "--live", "x"}} {
		var out, errOut strings.Builder
		code := Run(ctx, args, strings.NewReader(""), &out, &errOut)
		if code != ExitInterrupted || strings.Contains(out.String(), "doctor_failed") {
			t.Fatal(args, code, out.String(), errOut.String())
		}
	}
}
