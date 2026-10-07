package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/ui"
)

// configFiles reads every file under the config and data directories, so a
// test can prove setup wrote nothing (not even the lock file).
func configFiles(t *testing.T, p config.Paths) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range []string{p.ConfigDir, p.DataDir} {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				b, _ := os.ReadFile(path)
				out[path] = string(b)
			}
			return nil
		})
	}
	return out
}

func TestSetupRejectsJSON(t *testing.T) {
	p, _, _ := metadataSeed(t)
	before := configFiles(t, p)
	code, out, errOut := run(t, "setup", "--json")
	if code != 2 || errOut != "" || !strings.Contains(out, `"code":"terminal_required"`) || strings.Count(out, "\n") != 1 {
		t.Fatal(code, out, errOut)
	}
	if !reflect.DeepEqual(before, configFiles(t, p)) {
		t.Fatal("setup --json wrote files")
	}
}

func TestSetupRejectsNoInput(t *testing.T) {
	p, _, _ := metadataSeed(t)
	before := configFiles(t, p)
	code, out, errOut := run(t, "setup", "--no-input")
	if code != 2 || out != "" || !strings.Contains(errOut, "Setup needs an interactive terminal") || !strings.Contains(errOut, "config input set") {
		t.Fatal(code, out, errOut)
	}
	if !reflect.DeepEqual(before, configFiles(t, p)) {
		t.Fatal("setup --no-input wrote files")
	}
}

func TestSetupRejectsWithoutTerminal(t *testing.T) {
	p, _, _ := metadataSeed(t)
	before := configFiles(t, p)
	code, out, errOut := run(t, "setup")
	if code != 2 || out != "" || !strings.Contains(errOut, "Setup needs an interactive terminal") {
		t.Fatal(code, out, errOut)
	}
	if !reflect.DeepEqual(before, configFiles(t, p)) {
		t.Fatal("setup without a terminal wrote files")
	}
	if _, err := os.Stat(p.LockFile); !os.IsNotExist(err) {
		t.Fatal("lock file created", err)
	}
}

func TestSetupRejectsBeforeReadingConfig(t *testing.T) {
	p := metadataEnv(t)
	if err := os.MkdirAll(filepath.Dir(p.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{"setup"}, {"setup", "--json"}, {"setup", "--no-input"}} {
		code, out, errOut := run(t, argv...)
		if code != 2 || !strings.Contains(out+errOut, "terminal_required") && !strings.Contains(errOut, "Setup needs an interactive terminal") {
			t.Fatal(argv, code, out, errOut)
		}
	}
}

func TestSetupHelp(t *testing.T) {
	code, out, errOut := run(t, "setup", "--help")
	if code != 0 || errOut != "" || !strings.Contains(out, "Choose connections and tools in an interactive terminal.") {
		t.Fatal(code, out, errOut)
	}
	code, out, _ = run(t, "--help")
	if code != 0 || !strings.Contains(out, "setup") {
		t.Fatal(code, out)
	}
	code, out, _ = run(t, "setup", "extra", "--json")
	if code != 2 || !strings.Contains(out, `"code":"invalid_arguments"`) {
		t.Fatal(code, out)
	}
}

func TestSetupSummary(t *testing.T) {
	for _, tc := range []struct {
		result  ui.Result
		err     error
		out     string
		wantErr error
	}{
		{ui.Result{}, nil, "No changes saved.\n", nil},
		{ui.Result{Revision: 7, Saves: 2}, nil, "Configuration saved at revision 7.\n", nil},
		{ui.Result{Revision: 7, Interrupted: true}, nil, "", context.Canceled},
		{ui.Result{Revision: 8, Saves: 1, Interrupted: true}, nil, "Configuration saved at revision 8.\n", context.Canceled},
		{ui.Result{Revision: 8, Saves: 1, Interrupted: true}, context.Canceled, "Configuration saved at revision 8.\n", context.Canceled},
		{ui.Result{}, config.ErrConfigWrite, "", config.ErrConfigWrite},
		{ui.Result{Revision: 7, Unconfirmed: true}, nil, unconfirmedSummary, nil},
		{ui.Result{Revision: 7, Unconfirmed: true, Interrupted: true}, nil, unconfirmedSummary, context.Canceled},
		{ui.Result{Revision: 8, Saves: 1, Unconfirmed: true}, nil, "Configuration saved at revision 8.\n" + unconfirmedSummary, nil},
	} {
		var out bytes.Buffer
		err := finishSetup(&Streams{Out: &out}, tc.result, tc.err)
		if out.String() != tc.out || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (err == nil) {
			t.Fatal(tc.result, out.String(), err)
		}
	}
}
