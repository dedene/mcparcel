package config

import (
	"runtime"
	"testing"
)

func TestTempDirFor(t *testing.T) {
	for _, tc := range []struct{ goos, tmpdir, want string }{
		{"darwin", "/var/folders/xy/T/", "/private/tmp"},
		{"darwin", "", "/private/tmp"},
		{"linux", "/var//tmp/", "/var/tmp"},
		{"linux", "", "/tmp"},
		{"linux", "tmp", "/tmp"},
		{"linux", "./relative/../tmp", "/tmp"},
	} {
		if got := tempDirFor(tc.goos, tc.tmpdir); got != tc.want {
			t.Errorf("tempDirFor(%q, %q) = %q, want %q", tc.goos, tc.tmpdir, got, tc.want)
		}
	}
}

func TestDefaultTempDir(t *testing.T) {
	for _, tmpdir := range []string{"/var//tmp/", "", "relative"} {
		t.Setenv("TMPDIR", tmpdir)
		if got, want := DefaultTempDir(), tempDirFor(runtime.GOOS, tmpdir); got != want {
			t.Fatalf("TMPDIR=%q: DefaultTempDir() = %q, want %q", tmpdir, got, want)
		}
	}
}
