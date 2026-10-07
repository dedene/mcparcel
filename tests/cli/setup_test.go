package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

// setupRig seeds one personal connection and returns a listing of every
// file under the config and data directories.
func setupRig(t *testing.T) (*metadataRig, func() map[string]string) {
	t.Helper()
	r := newMetadataRig(t)
	_, err := config.NewStore(r.paths).Update(context.Background(), 0, func(s *config.State) error {
		s.Personal.Connections["paper"] = config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("nonexistent-fixture")}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	files := func() map[string]string {
		out := map[string]string{}
		for _, root := range []string{r.paths.ConfigDir, r.paths.DataDir, r.paths.StateDir} {
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
	return r, files
}

func TestSetupWithoutTTY(t *testing.T) {
	r, files := setupRig(t)
	before := files()
	for _, argv := range [][]string{{"setup"}, {"setup", "--no-input"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		c := exec.CommandContext(ctx, binaryA, argv...)
		c.Env = r.env
		c.Stdin = strings.NewReader("q\n")
		var out, errOut bytes.Buffer
		c.Stdout, c.Stderr = &out, &errOut
		err := c.Run()
		cancel()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "Setup needs an interactive terminal") || strings.Contains(errOut.String(), "\x1b") {
			t.Fatal(argv, err, out.String(), errOut.String())
		}
	}
	if !reflect.DeepEqual(before, files()) {
		t.Fatal("setup without a terminal wrote files")
	}
	r.offline()
}

func TestSetupJSONEnvelope(t *testing.T) {
	r, files := setupRig(t)
	before := files()
	v := r.run(2, "terminal_required", "setup", "--json")
	if !strings.Contains(v.stdout, "config input set") || string(v.envelope.Data) != "null" {
		t.Fatal(v.stdout)
	}
	if !reflect.DeepEqual(before, files()) {
		t.Fatal("setup --json wrote files")
	}
	r.offline()
}
