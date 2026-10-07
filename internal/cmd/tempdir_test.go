package cmd

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultTempDir(t *testing.T) {
	t.Setenv("TMPDIR", "/var//tmp/")
	want := "/var/tmp"
	if runtime.GOOS == "darwin" {
		want = "/private/tmp"
	}
	if got := defaultTempDir(); got != want || !filepath.IsAbs(got) {
		t.Fatalf("defaultTempDir() = %q, want %q", got, want)
	}
}
