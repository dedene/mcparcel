package doctor

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

func TestRuntimeBinaryRows(t *testing.T) {
	base := inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
	retained, err := runtimeclient.RetainedPath(base.Paths, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	statOnly := func(present ...string) func(string) (fs.FileInfo, error) {
		return func(path string) (fs.FileInfo, error) {
			for _, p := range present {
				if p == path {
					return nil, nil
				}
			}
			return nil, os.ErrNotExist
		}
	}
	running := func(exe string) runtimeclient.Probe {
		return runtimeclient.Probe{State: runtimeclient.ProbeRunning, DaemonVersion: "1.2.3", PID: 7, Status: &runtimeclient.Status{Running: true, Executable: exe}}
	}
	cases := []struct {
		name, status, message, next string
		edit                        func(*Input)
	}{
		{"retained", OK, "Runs from its retained copy " + retained + ".", "", func(in *Input) {
			in.Probe, in.Stat = running(retained), statOnly(retained)
		}},
		{"executable gone", Warn, "The runtime's executable file is gone", "Run mcparcel runtime restart when no calls are active.", func(in *Input) {
			in.Probe, in.Stat = running(retained), statOnly()
		}},
		{"outside retained", Warn, "The runtime runs from /npx/cache/mcparcel, not a retained copy", "Check free space", func(in *Input) {
			in.Probe, in.Stat = running("/npx/cache/mcparcel"), statOnly("/npx/cache/mcparcel")
		}},
		{"headless", Skip, "Headless mode runs the installed binary in place.", "", func(in *Input) {
			*in = headlessInput(*in)
			in.Probe = running("/usr/local/bin/mcparcel")
		}},
		{"supervised", Skip, "A supervisor runs the runtime from its own binary.", "", func(in *Input) {
			in.Supervised, in.Probe = true, running("/opt/mcparcel")
		}},
		{"mismatch", Skip, "See runtime.version.", "", func(in *Input) {
			in.Probe = runtimeclient.Probe{State: runtimeclient.ProbeVersionMismatch, DaemonVersion: "1.2.2", PID: 7}
		}},
		{"probe error", Skip, "See runtime.version.", "", func(in *Input) { in.ProbeErr = config.ErrUnsafePath }},
		{"stopped with copy", OK, "Retained copy present: " + retained + ".", "", func(in *Input) { in.Stat = statOnly(retained) }},
		{"stopped without copy", OK, "The next start copies this binary to " + retained + ".", "", func(in *Input) { in.Stat = statOnly() }},
		{"stale socket", OK, "The next start copies", "", func(in *Input) {
			in.Probe, in.Stat = runtimeclient.Probe{State: runtimeclient.ProbeStaleSocket}, statOnly()
		}},
		{"retain off", Skip, "This build runs the runtime from the CLI binary in place.", "", func(in *Input) { in.Retain = false }},
		{"mode unknown", Skip, modeUnknown, "", func(in *Input) { in.RuntimeErr = errors.New("bad") }},
		{"linux desktop", Skip, "Desktop mode does not run here; see runtime.mode.", "", func(in *Input) { in.DesktopSupported = false }},
		{"unnameable version", Skip, "This version (1 2) cannot name a retained copy", "", func(in *Input) { in.Version = "1 2" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.edit(&in)
			c := find(t, Offline(in), "runtime.binary", "")
			if c.Status != tc.status || !strings.HasPrefix(c.Message, tc.message) || !strings.HasPrefix(c.NextAction, tc.next) || c.Code != "" {
				t.Fatalf("%+v", c)
			}
		})
	}
}

func TestCatalogVersionRows(t *testing.T) {
	in := inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
	state := *in.Files.State
	state.Local.Sources = []config.Source{{ID: "github-1"}, {ID: "github-2"}, {ID: "github-3"}}
	state.Catalogs = map[string]config.Catalog{"github-1": {}, "github-2": {MinVersion: "0.1.0"}, "github-3": {MinVersion: "0.2.0"}}
	in.Files.State = &state
	in.Version = "0.1.0-rc.1+3f2a9c1d0e4b"
	checks := Offline(in)
	want(t, find(t, checks, "version.catalog", "github-1"), OK, "")
	newer := find(t, checks, "version.catalog", "github-2")
	want(t, newer, Fail, "catalog_requires_upgrade")
	if newer.Message != "This catalog needs MCParcel 0.1.0 or newer; this is 0.1.0-rc.1+3f2a9c1d0e4b." || newer.NextAction != "Upgrade MCParcel, then run mcparcel runtime restart." {
		t.Fatalf("%+v", newer)
	}
	want(t, find(t, checks, "version.catalog", "github-3"), Fail, "catalog_requires_upgrade")

	in.Version = "0.2.0"
	checks = Offline(in)
	if c := find(t, checks, "version.catalog", "github-3"); c.Status != OK || c.Message != "Needs 0.2.0 or newer; this is 0.2.0." {
		t.Fatalf("%+v", c)
	}
	in.Version = "5319954-dirty"
	checks = Offline(in)
	if c := find(t, checks, "version.catalog", "github-2"); c.Status != Skip || c.Message != "Development build; version not compared." {
		t.Fatalf("%+v", c)
	}
	if c := find(t, checks, "version.catalog", "github-1"); c.Status != OK || c.Message != "No minimum version." {
		t.Fatalf("%+v", c)
	}
	in.Files.State = nil
	absent(t, Offline(in), "version.catalog")
}
