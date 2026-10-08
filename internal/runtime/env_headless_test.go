package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func TestHeadlessEnvKeepsOnlyNamedVariables(t *testing.T) {
	environ := []string{"PATH=/usr/bin", "HOME=/home/agent", "SHELL=/bin/zsh", "LOGNAME=agent", "FRONT_CLIENT_ID=id-canary", "AWS_SECRET_ACCESS_KEY=aws-canary", "GH_TOKEN=gh-canary", "FRONT_CLIENT_ID=second", "BROKEN", "=x", "LANG=C.UTF-8"}
	got := HeadlessEnv(environ, []string{"FRONT_CLIENT_ID", "FRONT_CLIENT_SECRET"})
	want := map[string]string{"PATH": "/usr/bin", "HOME": "/home/agent", "LANG": "C.UTF-8", "FRONT_CLIENT_ID": "id-canary"}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatal(k, got)
		}
	}
	// PATH falls back to the default when the daemon has none.
	if got = HeadlessEnv([]string{"HOME=/h"}, nil); got["PATH"] != defaultPath || got["HOME"] != "/h" || len(got) != 2 {
		t.Fatal(got)
	}
}

func TestForwardedNames(t *testing.T) {
	secret := func(name string) config.Value { return config.Value{Secret: &config.SecretRef{Secret: "env:" + name}} }
	op := config.Value{Secret: &config.SecretRef{Secret: "op://v/i/f"}}
	front := config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://mcp.example/mcp"), Headers: map[string]config.Value{"X-Key": secret("FRONT_KEY"), "X-Op": op}}}, Auth: &config.OAuth{Type: "oauth", ClientID: ptrValue(secret("FRONT_CLIENT_ID")), ClientSecret: ptrValue(secret("FRONT_CLIENT_SECRET"))}}
	tool := config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("/bin/tool"), InheritEnv: []string{"TOOL_HOME", "FRONT_KEY"}, Env: map[string]config.Value{"A": secret("TOOL_TOKEN"), "B": secret("LD_PRELOAD"), "C": secret("XDG_CONFIG_HOME"), "D": secret("MCPARCEL_RUNTIME_DIR")}}}}
	off := config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("/bin/off"), InheritEnv: []string{"OFF_HOME"}, Env: map[string]config.Value{"A": secret("OFF_TOKEN")}}}}
	snap := config.Snapshot{Effective: &config.EffectiveConfig{Connections: map[string]config.EffectiveConnection{
		"local:front": {Connection: &front, Enabled: true, Available: true},
		"local:tool":  {Connection: &tool, Enabled: true, Available: true},
		"local:off":   {Connection: &off, Enabled: false, Available: true},
		"local:gone":  {Enabled: true},
	}}}
	got := ForwardedNames(snap)
	want := []string{"FRONT_CLIENT_ID", "FRONT_CLIENT_SECRET", "FRONT_KEY", "HOME", "LANG", "LC_ALL", "PATH", "TMPDIR", "TOOL_HOME", "TOOL_TOKEN", "USER"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got = ForwardedNames(config.Snapshot{}); !slices.Equal(got, []string{"HOME", "LANG", "LC_ALL", "PATH", "TMPDIR", "USER"}) {
		t.Fatal(got)
	}
}

func ptrValue(v config.Value) *config.Value { return &v }

// writeHeadlessConfig writes config.json (headless, p's state root) and a
// personal.json with one HTTP connection whose credentials come from
// FRONT_CLIENT_ID and FRONT_CLIENT_SECRET.
func writeHeadlessConfig(t *testing.T, p config.Paths) {
	t.Helper()
	local, err := json.Marshal(map[string]any{"schemaVersion": 1, "runtime": map[string]any{"mode": "headless", "stateRoot": p.StateRoot}})
	if err != nil {
		t.Fatal(err)
	}
	personal := `{"schemaVersion":1,"connections":{"front":{"transport":{"type":"http","url":"https://mcp.example/mcp","headers":{"X-Client":{"secret":"env:FRONT_CLIENT_ID"},"Authorization":{"secret":"env:FRONT_CLIENT_SECRET","prefix":"Bearer "}}}}}}`
	for path, body := range map[string][]byte{p.ConfigFile: local, p.PersonalFile: []byte(personal)} {
		if err = os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The CLI forwards to a daemon it starts only the base names and what enabled
// connections reference; unrelated secrets in its environment stay behind.
func TestHeadlessForwardsOnlyReferencedNames(t *testing.T) {
	p := headlessPaths(t)
	writeHeadlessConfig(t, p)
	for k, v := range map[string]string{"FRONT_CLIENT_ID": "id-canary", "FRONT_CLIENT_SECRET": "secret-canary", "AWS_SECRET_ACCESS_KEY": "aws-canary", "GH_TOKEN": "gh-canary", "UNRELATED": "x", "SHELL": "/bin/zsh", "LANG": "C"} {
		t.Setenv(k, v)
	}
	got := envMap(DaemonEnvironment(p))
	if got["FRONT_CLIENT_ID"] != "id-canary" || got["FRONT_CLIENT_SECRET"] != "secret-canary" || got["LANG"] != "C" || got["HOME"] != p.Home || got["PATH"] == "" {
		t.Fatal(got)
	}
	for _, k := range []string{"AWS_SECRET_ACCESS_KEY", "GH_TOKEN", "UNRELATED", "SHELL", "LOGNAME"} {
		if _, ok := got[k]; ok {
			t.Fatalf("%s forwarded to the headless daemon", k)
		}
	}
	// An unreadable configuration still starts the daemon with the base names.
	if err := os.Remove(p.PersonalFile); err != nil {
		t.Fatal(err)
	}
	got = envMap(DaemonEnvironment(p))
	if _, ok := got["FRONT_CLIENT_ID"]; ok || got["HOME"] != p.Home {
		t.Fatal(got)
	}
}

func TestEnvRefValuesHeadlessMessage(t *testing.T) {
	secret := func(name string) config.Value { return config.Value{Secret: &config.SecretRef{Secret: "env:" + name}} }
	c := config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Env: map[string]config.Value{"A": secret("FRONT_CLIENT_SECRET"), "B": secret("FRONT_CLIENT_ID"), "C": secret("PRESENT")}}}}
	keychain := func(context.Context, string) (string, error) {
		t.Error("keychain consulted in headless mode")
		return "keychain-canary", nil
	}
	_, e := envRefValues(t.Context(), map[string]string{"PRESENT": "value-canary"}, keychain, true, c, nil)
	var out *output.Error
	if !errors.As(e, &out) || out.Code != "config_required" {
		t.Fatal(e)
	}
	if out.Message != "Environment variables FRONT_CLIENT_ID, FRONT_CLIENT_SECRET are not set." || out.NextAction != "Set FRONT_CLIENT_ID, FRONT_CLIENT_SECRET in the environment that starts mcparcel (headless mode reads no Keychain), then run mcparcel runtime restart." || out.Details == nil || !slices.Equal(out.Details.Variables, []string{"FRONT_CLIENT_ID", "FRONT_CLIENT_SECRET"}) {
		t.Fatalf("%+v %+v", out, out.Details)
	}
	one := config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Env: map[string]config.Value{"A": secret("FRONT_CLIENT_SECRET")}}}}
	_, e = envRefValues(t.Context(), map[string]string{"FRONT_CLIENT_SECRET": ""}, nil, true, one, nil)
	if !errors.As(e, &out) || out.Message != "Environment variable FRONT_CLIENT_SECRET is not set." || out.NextAction != "Set FRONT_CLIENT_SECRET in the environment that starts mcparcel (headless mode reads no Keychain), then run mcparcel runtime restart." || !slices.Equal(out.Details.Variables, []string{"FRONT_CLIENT_SECRET"}) {
		t.Fatalf("%+v", out)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"variables":["FRONT_CLIENT_SECRET"]`) || strings.Contains(string(b), "canary") {
		t.Fatal(string(b))
	}
	got, e := envRefValues(t.Context(), map[string]string{"FRONT_CLIENT_SECRET": "s", "FRONT_CLIENT_ID": "i", "PRESENT": "p"}, keychain, true, c, nil)
	if e != nil || got["env:FRONT_CLIENT_SECRET"] != "s" || got["env:FRONT_CLIENT_ID"] != "i" {
		t.Fatal(got, e)
	}
}

// A service-account profile's tokenEnv reaches the headless daemon, which
// reads it itself, but is never a forwarded (child-visible) name and never
// reaches a child.
func TestHeadlessDaemonKeepsTokenEnv(t *testing.T) {
	p := headlessPaths(t)
	writeHeadlessConfig(t, p)
	local, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "runtime": map[string]any{"mode": "headless", "stateRoot": p.StateRoot},
		"credentialProfiles": map[string]any{
			"ops":  map[string]any{"mode": "service-account", "tokenEnv": "OP_SERVICE_ACCOUNT_TOKEN"},
			"ops2": map[string]any{"mode": "service-account", "tokenEnv": "OP_SERVICE_ACCOUNT_TOKEN"},
			"file": map[string]any{"mode": "service-account", "tokenFile": "/run/secrets/op"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p.ConfigFile, local, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "token-canary")
	t.Setenv("OP_OTHER", "other-canary")
	snap, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	names := daemonEnvNames(p)
	if !slices.Contains(names, "OP_SERVICE_ACCOUNT_TOKEN") || !slices.IsSorted(names) || len(slices.Compact(slices.Clone(names))) != len(names) {
		t.Fatal(names)
	}
	if slices.Contains(ForwardedNames(snap), "OP_SERVICE_ACCOUNT_TOKEN") {
		t.Fatal("token variable is a forwarded name")
	}
	daemon := envMap(DaemonEnvironment(p))
	own := HeadlessDaemonEnv(p)
	for _, env := range []map[string]string{daemon, own} {
		if env["OP_SERVICE_ACCOUNT_TOKEN"] != "token-canary" {
			t.Fatal(env)
		}
		if _, ok := env["OP_OTHER"]; ok {
			t.Fatal("unreferenced OP_ variable kept")
		}
	}
	child, err := BuildChildEnv(own, config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("/bin/tool")}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range child {
		if k == "OP_SERVICE_ACCOUNT_TOKEN" || strings.Contains(v, "token-canary") {
			t.Fatal("token reached the child environment")
		}
	}
	_, err = BuildChildEnv(own, config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("/bin/tool"), InheritEnv: []string{"OP_SERVICE_ACCOUNT_TOKEN"}}}}, nil)
	var e *output.Error
	if !errors.As(err, &e) || e.Code != "invalid_config" {
		t.Fatal("inheritEnv of the token variable:", err)
	}
}
