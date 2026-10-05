package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func metadataEnv(t *testing.T) config.Paths {
	t.Helper()
	p, env := testutil.IsolatedPaths(t)
	for _, v := range env {
		k, s, _ := strings.Cut(v, "=")
		t.Setenv(k, s)
	}
	return p
}

func TestConfigConflictEnvelope(t *testing.T) {
	p := metadataEnv(t)
	store := config.NewStore(p)
	_, e := store.Update(context.Background(), 0, func(s *config.State) error {
		s.Local.CredentialProfiles["work"] = config.Profile{Mode: "desktop", Account: "Fixture"}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = store.Update(context.Background(), 0, func(*config.State) error { t.Fatal("stale callback ran"); return nil })
	var out, errOut bytes.Buffer
	code := writeFailure(&out, &errOut, true, e)
	if code != 7 || !strings.Contains(out.String(), `"data":null`) || !strings.Contains(out.String(), `"code":"config_conflict"`) || strings.Count(out.String(), "\n") != 1 || errOut.Len() != 0 {
		t.Fatal(code, out.String(), errOut.String())
	}
}

func TestMetadataWriterFailure(t *testing.T) {
	p := metadataEnv(t)
	file := filepath.Join(p.Home, "catalog.json")
	if e := os.WriteFile(file, []byte(`{"schemaVersion":1,"connections":{}}`), 0o600); e != nil {
		t.Fatal(e)
	}
	w := &failingProductWriter{}
	var errOut bytes.Buffer
	code := Run(context.Background(), []string{"config", "validate", "--file", file, "--json"}, strings.NewReader(""), w, &errOut)
	if code != 1 || w.calls != 1 || errOut.Len() != 0 {
		t.Fatal(code, w.calls, errOut.String())
	}
}

func TestConfigSafeFailures(t *testing.T) {
	for _, row := range []struct {
		err  error
		code string
	}{{config.ErrDisabled, "connection_disabled"}, {config.ErrReviewRequired, "review_required"}, {config.ErrToolDenied, "tool_denied"}, {config.ErrRuntimeUnsupported, "runtime_unsupported"}, {config.ErrConfig, "invalid_config"}, {config.ErrAliasCollision, "alias_collision"}, {config.ErrConfigWrite, "config_write_failed"}, {errors.Join(config.ErrConfigWrite, config.ErrUnsafePath), "unsafe_local_path"}, {config.ErrDurability, "config_write_failed"}, {context.DeadlineExceeded, "timeout"}, {errors.New("IO-CANARY"), "internal_error"}} {
		if got := safeFailure(row.err); got.Code != row.code || strings.Contains(got.Error(), "CANARY") {
			t.Fatal(got, row)
		}
	}
}

func TestConfigCommandFileSafety(t *testing.T) {
	p := metadataEnv(t)
	for _, tc := range []struct {
		name string
		body []byte
		mode os.FileMode
		code string
	}{{"readonly", []byte(`{"schemaVersion":1,"connections":{}}`), 0o444, ""}, {"large", bytes.Repeat([]byte("x"), 2*1024*1024+1), 0o600, "invalid_config"}, {"unsafe", []byte(`{}`), 0o666, "unsafe_local_path"}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(p.Home, tc.name)
			if e := os.WriteFile(path, tc.body, tc.mode); e != nil {
				t.Fatal(e)
			}
			if e := os.Chmod(path, tc.mode); e != nil {
				t.Fatal(e)
			}
			code, out, errOut := run(t, "config", "validate", "--file", path, "--json")
			want := 2
			if tc.code == "" {
				want = 0
			}
			if code != want || errOut != "" || tc.code != "" && !strings.Contains(out, `"code":"`+tc.code+`"`) {
				t.Fatal(code, out, errOut)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := readCommandFile(ctx, filepath.Join(p.Home, "missing")); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	for _, path := range []string{p.Home, filepath.Join(p.Home, "missing")} {
		_, e := readCommandFile(context.Background(), path)
		want := config.ErrConfig
		if path == p.Home {
			want = config.ErrUnsafePath
		}
		if !errors.Is(e, want) {
			t.Fatal(e)
		}
	}
}
