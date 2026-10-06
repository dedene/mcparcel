package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/testutil"
)

// TestOutputDirFlagValidation: a repeated, empty or unusable --output-dir
// fails with exit 2 before the runtime is contacted.
func TestOutputDirFlagValidation(t *testing.T) {
	p, env := testutil.IsolatedPaths(t)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"--output-dir", dir, "--output-dir", dir}, "Invalid call arguments."},
		{[]string{"--output-dir=" + dir, "--output-dir=" + dir}, "Invalid call arguments."},
		{[]string{"--output-dir="}, "Invalid call arguments."},
		{[]string{"--output-dir", ""}, "Invalid call arguments."},
		{[]string{"--output-dir", filepath.Join(dir, "missing")}, "--output-dir must name an existing directory."},
		{[]string{"--output-dir", file}, "--output-dir must name an existing directory."},
	} {
		code, stdout, stderr := run(t, append([]string{"call", "fixture.echo", "--json", "text=x"}, tc.args...)...)
		var envelope struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if code != ExitUsage || json.Unmarshal([]byte(stdout), &envelope) != nil || envelope.Error.Code != "invalid_arguments" || envelope.Error.Message != tc.message || stderr != "" {
			t.Fatalf("%v: exit %d stdout %q stderr %q", tc.args, code, stdout, stderr)
		}
	}
	if entries, _ := os.ReadDir(p.RuntimeDir); len(entries) != 0 {
		t.Fatal("runtime contacted", entries)
	}
}
