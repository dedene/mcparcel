package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runtimeCase(t *testing.T, raw string, schemaValid, goValid bool, path string) {
	t.Helper()
	checkValidationCase(t, validationCase{Document: "config", Raw: raw, SyntaxValid: true, SchemaValid: schemaValid, GoValid: goValid, Path: path})
}

func TestHeadlessRequiresStateRoot(t *testing.T) {
	runtimeCase(t, `{"schemaVersion":1,"runtime":{"mode":"headless"}}`, false, false, "runtime.stateRoot")
	runtimeCase(t, `{"schemaVersion":1,"runtime":{"mode":"headless","stateRoot":""}}`, false, false, "runtime.stateRoot")
	runtimeCase(t, `{"schemaVersion":1,"runtime":{"mode":"headless","stateRoot":"/var/lib/mcparcel"}}`, true, true, "")
	l, err := DecodeLocal([]byte(`{"schemaVersion":1,"runtime":{"mode":"headless","stateRoot":"/var/lib/mcparcel"}}`))
	if err != nil || !l.Headless() || l.Runtime.StateRoot != "/var/lib/mcparcel" {
		t.Fatal(l, err)
	}
	for _, raw := range []string{`{"schemaVersion":1}`, `{"schemaVersion":1,"runtime":{"keepAlive":true}}`, `{"schemaVersion":1,"runtime":{"mode":"desktop"}}`} {
		l, err := DecodeLocal([]byte(raw))
		if err != nil || l.Headless() {
			t.Fatal(raw, l, err)
		}
	}
}

func TestStateRootRejectedInDesktop(t *testing.T) {
	runtimeCase(t, `{"schemaVersion":1,"runtime":{"stateRoot":"/var/lib/mcparcel"}}`, false, false, "runtime.stateRoot")
	runtimeCase(t, `{"schemaVersion":1,"runtime":{"mode":"desktop","stateRoot":"/var/lib/mcparcel"}}`, false, false, "runtime.stateRoot")
	runtimeCase(t, `{"schemaVersion":1,"runtime":{"mode":"desktop","keepAlive":true}}`, true, true, "")
}

func TestStateRootMustBeAbsoluteAndClean(t *testing.T) {
	for _, root := range []string{"var/lib/mcparcel", "./state", "/"} {
		runtimeCase(t, `{"schemaVersion":1,"runtime":{"mode":"headless","stateRoot":"`+root+`"}}`, false, false, "runtime.stateRoot")
	}
	// The schema checks the leading slash; Go also requires a clean path.
	for _, root := range []string{"/var/lib/../mcparcel", "/var/lib/mcparcel/", "/var//lib", "/var/lib/./mcparcel"} {
		runtimeCase(t, `{"schemaVersion":1,"runtime":{"mode":"headless","stateRoot":"`+root+`"}}`, true, false, "runtime.stateRoot")
	}
	if _, err := DecodeLocal([]byte("{\"schemaVersion\":1,\"runtime\":{\"mode\":\"headless\",\"stateRoot\":\"/var/\\u0000lib\"}}")); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
}

func TestUnknownModeRejected(t *testing.T) {
	for _, mode := range []string{"server", "Headless", "DESKTOP", ""} {
		runtimeCase(t, `{"schemaVersion":1,"runtime":{"mode":"`+mode+`","stateRoot":"/var/lib/mcparcel"}}`, false, false, "runtime")
	}
	runtimeCase(t, `{"schemaVersion":1,"runtime":{"mode":"server"}}`, false, false, "runtime.mode")
}

func TestApplyStateRootLayout(t *testing.T) {
	p := storePaths(t)
	root := filepath.Join(filepath.Dir(p.Home), "state-root")
	got, err := ApplyStateRoot(p, root)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalConfigPath(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	canonical = filepath.Join(canonical, "state-root")
	want := p
	want.StateRoot = canonical
	want.StateDir, want.DataDir, want.CacheDir, want.RuntimeDir = canonical+"/state", canonical+"/data", canonical+"/cache", canonical+"/run"
	want.SocketFile, want.LockFile, want.LogFile = canonical+"/run/daemon.sock", canonical+"/run/daemon.lock", canonical+"/state/daemon.log"
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if entries, err := os.ReadDir(filepath.Dir(root)); err != nil || len(entries) != 0 {
		t.Fatal("ApplyStateRoot created files", entries, err)
	}
	long := "/" + strings.Repeat("x", 100-len("/run/daemon.sock"))
	if _, err := ApplyStateRoot(p, long); !errors.Is(err, ErrUnsafePath) {
		t.Fatal(len(long), err)
	}
	if _, err := ApplyStateRoot(p, long[:len(long)-1]); err != nil {
		t.Fatal("socket path of exactly 100 bytes must be accepted:", err)
	}
	for _, bad := range []string{"", "relative", "/", "/a/../b", "/a/"} {
		if _, err := ApplyStateRoot(p, bad); !errors.Is(err, ErrUnsafePath) {
			t.Fatal(bad, err)
		}
	}
}

func TestReadRuntimeAbsentIsDesktop(t *testing.T) {
	p := storePaths(t)
	rt, err := ReadRuntime(context.Background(), p)
	if err != nil || rt != (RuntimeDefaults{}) {
		t.Fatal(rt, err)
	}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if rt, err = ReadRuntime(context.Background(), p); err != nil || rt != (RuntimeDefaults{}) {
		t.Fatal(rt, err)
	}
	testWrite(t, p.ConfigFile, []byte(`{"schemaVersion":1,"runtime":{"mode":"headless","stateRoot":"/var/lib/mcparcel"}}`), 0o600)
	rt, err = ReadRuntime(context.Background(), p)
	if err != nil || rt.Mode != ModeHeadless || rt.StateRoot != "/var/lib/mcparcel" {
		t.Fatal(rt, err)
	}
	entries, err := os.ReadDir(p.ConfigDir)
	if err != nil || len(entries) != 1 {
		t.Fatal("ReadRuntime must not create a lock or any other file", entries, err)
	}
	testWrite(t, p.ConfigFile, []byte(`{"schemaVersion":1,"runtime":{"mode":"server"}}`), 0o600)
	if _, err = ReadRuntime(context.Background(), p); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = ReadRuntime(ctx, p); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLinuxDesktopRuntimeCommandUnsupported(t *testing.T) {
	headless := RuntimeDefaults{Mode: ModeHeadless, StateRoot: "/var/lib/mcparcel"}
	for _, c := range []struct {
		goos string
		rt   RuntimeDefaults
		want error
	}{
		{"linux", RuntimeDefaults{}, ErrHeadlessOnly},
		{"linux", RuntimeDefaults{Mode: ModeDesktop}, ErrHeadlessOnly},
		{"linux", headless, nil},
		{"darwin", RuntimeDefaults{}, nil},
		{"darwin", headless, nil},
	} {
		if err := CheckMode(c.rt, DesktopSupported(c.goos)); !errors.Is(err, c.want) || (c.want == nil && err != nil) {
			t.Fatal(c.goos, c.rt, err)
		}
	}
}
