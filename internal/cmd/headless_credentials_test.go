package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

const headlessPersonal = `{"schemaVersion":1,"connections":{"front":{"transport":{"type":"http","url":"https://mcp.example/mcp","headers":{"Authorization":{"secret":"env:FRONT_CLIENT_SECRET","prefix":"Bearer "}}}}}}`

// headlessFront is headlessEnv with a personal.json whose one connection
// reads FRONT_CLIENT_SECRET.
func headlessFront(t *testing.T) config.Paths {
	t.Helper()
	headlessEnv(t)
	paths, err := runtimePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(paths.PersonalFile, []byte(headlessPersonal), 0o600); err != nil {
		t.Fatal(err)
	}
	return paths
}

// spyShell sets SHELL to a script that leaves a marker file when run.
func spyShell(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "shell")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > '"+marker+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", script)
	return marker
}

func TestHeadlessNeverCapturesLoginShell(t *testing.T) {
	paths := headlessFront(t)
	marker := spyShell(t)
	for k, v := range map[string]string{"FRONT_CLIENT_SECRET": "secret-canary", "AWS_SECRET_ACCESS_KEY": "aws-canary", "GH_TOKEN": "gh-canary"} {
		t.Setenv(k, v)
	}
	env, err := daemonEnvSource(paths)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); err == nil {
		t.Fatal("headless daemon ran the login shell")
	}
	if env["FRONT_CLIENT_SECRET"] != "secret-canary" || env["HOME"] == "" || env["PATH"] == "" {
		t.Fatal(env)
	}
	for _, k := range []string{"AWS_SECRET_ACCESS_KEY", "GH_TOKEN", "SHELL"} {
		if _, ok := env[k]; ok {
			t.Fatalf("%s reached the headless runtime environment", k)
		}
	}
	// The spy does catch a login-shell capture: desktop mode runs it.
	desktop := paths
	desktop.StateRoot = ""
	_, _ = daemonEnvSource(desktop)(context.Background())
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("desktop mode did not run the spy shell", err)
	}
}

// spyFactories replaces the Keychain, keyring and 1Password factories with
// spies and reports how often each ran.
func spyFactories(t *testing.T) *[3]int {
	t.Helper()
	calls := &[3]int{}
	keychain, keyring, credentials := keychainFactory, keyringFactory, credentialsFactory
	t.Cleanup(func() { keychainFactory, keyringFactory, credentialsFactory = keychain, keyring, credentials })
	keychainFactory = func(config.Paths) func(context.Context, string) (string, error) {
		calls[0]++
		return func(context.Context, string) (string, error) { return "", auth.ErrProvider }
	}
	keyringFactory = func(config.Paths) auth.Keyring { calls[1]++; return nil }
	credentialsFactory = func(config.Paths, string, map[string]string) auth.Resolver {
		calls[2]++
		return auth.NewResolver(auth.ResolverOptions{})
	}
	return calls
}

// Headless mode builds the 1Password resolver, for service-account profiles,
// but neither the Keychain nor the keyring.
func TestHeadlessNoKeyringConstructed(t *testing.T) {
	paths := headlessFront(t)
	calls := spyFactories(t)
	opts := daemonPoolOptions(paths, map[string]string{}, io.Discard)
	if *calls != [3]int{0, 0, 1} || !opts.Headless || opts.Keyring != nil || opts.Keychain != nil || opts.Credentials == nil {
		t.Fatalf("calls %v, options %+v", *calls, opts)
	}
	code, stdout, stderr := run(t, "auth", "status", "--json")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"items":[]`) || !strings.Contains(stdout, `"profiles":[]`) {
		t.Fatal(code, stdout, stderr)
	}
	code, stdout, _ = run(t, "auth", "status")
	if code != 0 || !strings.HasPrefix(stdout, "No sign-ins in headless mode.\n") {
		t.Fatal(code, stdout)
	}
	if *calls != [3]int{0, 0, 1} {
		t.Fatal("factory used by auth status", *calls)
	}
	desktop := paths
	desktop.StateRoot = ""
	if opts = daemonPoolOptions(desktop, map[string]string{}, io.Discard); *calls != [3]int{1, 1, 2} || opts.Headless || opts.Credentials == nil {
		t.Fatal("desktop factories", *calls)
	}
	// Desktop mode without the 1Password app (Linux) refuses its profiles.
	if opts.NoDesktopApp == desktopOnePassword(desktop) {
		t.Fatal("NoDesktopApp", opts.NoDesktopApp)
	}
}

// The resolver reads service-account tokens from the daemon's environment,
// in both modes.
func TestDaemonCredentialsGetLoginEnv(t *testing.T) {
	paths := headlessFront(t)
	saved := credentialsFactory
	t.Cleanup(func() { credentialsFactory = saved })
	var got []map[string]string
	credentialsFactory = func(_ config.Paths, _ string, env map[string]string) auth.Resolver {
		got = append(got, env)
		return auth.NewResolver(auth.ResolverOptions{})
	}
	login := map[string]string{"OP_SERVICE_ACCOUNT_TOKEN": "token-canary"}
	daemonPoolOptions(paths, login, io.Discard)
	desktop := paths
	desktop.StateRoot = ""
	daemonPoolOptions(desktop, login, io.Discard)
	if len(got) != 2 || got[0]["OP_SERVICE_ACCOUNT_TOKEN"] != "token-canary" || got[1]["OP_SERVICE_ACCOUNT_TOKEN"] != "token-canary" {
		t.Fatal(got)
	}
}

// Headless auth status lists a bound service-account profile, with where its
// token comes from and never the token; profiles stays an array.
func TestHeadlessAuthStatusProfiles(t *testing.T) {
	paths, _ := headlessEnv(t)
	personal := `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{
		"op":{"credentialProfile":"team","transport":{"type":"stdio","command":"/bin/sh","env":{"K":{"secret":"op://v/i/f"}}}}}}`
	selections := `{"schemaVersion":1,"revision":1,"connections":{"local:op":{"enabled":true,"credentialProfile":"ops"}}}`
	raw, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	local := strings.Replace(string(raw), `{"runtime"`, `{"credentialProfiles":{"ops":{"mode":"service-account","tokenEnv":"OP_SERVICE_ACCOUNT_TOKEN"}},"runtime"`, 1)
	if local == string(raw) {
		t.Fatal("config.json shape changed:", string(raw))
	}
	for path, body := range map[string]string{paths.PersonalFile: personal, paths.ConfigFile: local, paths.SelectionsFile: selections} {
		if err = os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "token-canary")
	code, stdout, stderr := run(t, "auth", "status", "--json")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"items":[]`) ||
		!strings.Contains(stdout, `"profiles":[{"profile":"ops","mode":"service-account","session":"none","tokenEnv":"OP_SERVICE_ACCOUNT_TOKEN","connections":["local:op"]}]`) ||
		strings.Contains(stdout, "token-canary") {
		t.Fatal(code, stdout, stderr)
	}
	code, stdout, _ = run(t, "auth", "status")
	if code != 0 || stdout != "No sign-ins in headless mode.\nprofile ops  service-account  none  token from OP_SERVICE_ACCOUNT_TOKEN\n" {
		t.Fatal(code, stdout)
	}
}

func TestStatusTextHeadlessEnvironment(t *testing.T) {
	status := runtimeclient.Status{Running: true, CapturedPath: "/usr/bin"}
	if text := statusText(status, true); !strings.Contains(text, "Environment: daemon environment\n") {
		t.Fatal(text)
	}
	if text := statusText(status, false); !strings.Contains(text, "Environment: login shell\n") {
		t.Fatal(text)
	}
}
