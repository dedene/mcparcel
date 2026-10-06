package cli_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/testutil"
)

type exported struct {
	Result struct {
		Content []struct{ Type, MIMEType, Data, Text string } `json:"content"`
	} `json:"result"`
	Artifacts []struct {
		Index    int    `json:"index"`
		Type     string `json:"type"`
		MIMEType string `json:"mimeType"`
		Path     string `json:"path"`
		Bytes    int64  `json:"bytes"`
	} `json:"artifacts"`
	Warnings []struct{ Code string } `json:"warnings"`
}

func exportData(t *testing.T, v result) exported {
	t.Helper()
	var d exported
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
		t.Fatal(e, v.stdout)
	}
	return d
}

func outDir(t *testing.T, r *rig, mode os.FileMode) string {
	t.Helper()
	dir := filepath.Join(r.root, "out")
	if e := os.Mkdir(dir, 0o700); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(dir, mode); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

var artifactName = regexp.MustCompile(`^mcparcel-\d{8}T\d{6}Z-[0-9a-f]{6}-\d+\.(png|wav)$`)

func checkArtifactFiles(t *testing.T, dir string, d exported) {
	t.Helper()
	for _, a := range d.Artifacts {
		want := testutil.MediaPNG
		if a.Type == "audio" {
			want = testutil.MediaWAV
		}
		b, e := os.ReadFile(a.Path)
		info, se := os.Lstat(a.Path)
		if e != nil || se != nil || !bytes.Equal(b, want) || info.Mode() != 0o600 || a.Bytes != int64(len(want)) || filepath.Dir(a.Path) != dir || !artifactName.MatchString(filepath.Base(a.Path)) {
			t.Fatalf("%+v %v %v", a, e, info)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != len(d.Artifacts) {
		t.Fatal(entries)
	}
}

func TestOutputDirExportBlackBox(t *testing.T) {
	r := newRig(t)
	dir := outDir(t, r, 0o700)
	d := exportData(t, r.call("fixture.media", "--output-dir", dir, "count=2"))
	if len(d.Result.Content) != 4 || len(d.Artifacts) != 3 {
		t.Fatal(d)
	}
	for i, a := range d.Artifacts {
		block := d.Result.Content[i]
		if a.Index != i || a.Type != block.Type || a.MIMEType != block.MIMEType {
			t.Fatalf("%+v %+v", a, block)
		}
		raw, e := base64.StdEncoding.DecodeString(block.Data)
		if f, _ := os.ReadFile(a.Path); e != nil || !bytes.Equal(raw, f) {
			t.Fatal("--json must keep the full base64 next to the export")
		}
	}
	if d.Artifacts[2].Type != "audio" || filepath.Ext(d.Artifacts[2].Path) != ".wav" || d.Result.Content[3].Text != "media" {
		t.Fatal(d.Artifacts)
	}
	checkArtifactFiles(t, dir, d)
	// A relative directory resolves against the working directory.
	rel := filepath.Join(r.paths.Home, "rel")
	if e := os.Mkdir(rel, 0o700); e != nil {
		t.Fatal(e)
	}
	d = exportData(t, r.call("fixture.media", "--output-dir=rel", "count=0"))
	if len(d.Artifacts) != 1 || filepath.Dir(d.Artifacts[0].Path) != rel {
		t.Fatal(d.Artifacts)
	}
	// Nothing to save: no artifacts key at all.
	if v := r.call("fixture.echo", "--output-dir", dir, "text=x"); strings.Contains(v.stdout, "artifacts") {
		t.Fatal(v.stdout)
	}
}

func TestOutputDirHumanBlackBox(t *testing.T) {
	r := newRig(t)
	dir := outDir(t, r, 0o700)
	v := r.run("call", "fixture.media", "--output-dir", dir)
	if v.code != 0 || v.stderr != "" {
		t.Fatalf("%d %q %q", v.code, v.stdout, v.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(v.stdout, "\n"), "\n")
	if len(lines) != 3 || lines[2] != "media" {
		t.Fatalf("%q", v.stdout)
	}
	for i, kind := range []string{"image", "audio"} {
		path, ok := strings.CutPrefix(lines[i], "["+kind+" saved: ")
		path, ok2 := strings.CutSuffix(path, "]")
		if !ok || !ok2 || filepath.Dir(path) != dir {
			t.Fatalf("%q", lines[i])
		}
		if _, e := os.Stat(path); e != nil {
			t.Fatal(e)
		}
	}
	if v = r.run("call", "fixture.media"); v.code != 0 || v.stdout != "[image]\n[audio]\nmedia\n" {
		t.Fatalf("%q", v.stdout)
	}
}

func TestOutputDirInvalidBlackBox(t *testing.T) {
	r := newRig(t)
	file := filepath.Join(r.root, "file")
	r.write(file, "", 0o600)
	for _, args := range [][]string{{"--output-dir", filepath.Join(r.root, "missing")}, {"--output-dir", file}, {"--output-dir="}, {"--output-dir", r.root, "--output-dir", r.root}} {
		r.check(r.run(append([]string{"call", "fixture.media", "--json"}, args...)...), 2, "invalid_arguments")
	}
	if b, _ := os.ReadFile(r.root + "/started"); len(b) != 0 {
		t.Fatalf("dispatched: %q", b)
	}
	if pids := r.daemonPIDs(); len(pids) != 0 {
		t.Fatal("runtime contacted", pids)
	}
}

// TestNoImplicitFilesBlackBox: without --output-dir no call writes a payload
// file anywhere under the test's home, temp or state directories.
func TestNoImplicitFilesBlackBox(t *testing.T) {
	r := newRig(t)
	r.call("fixture.media", "count=3")
	if v := r.run("call", "fixture.media"); v.code != 0 {
		t.Fatal(v.stdout, v.stderr)
	}
	png := testutil.MediaPNG
	_ = filepath.WalkDir(r.root, func(path string, d fs.DirEntry, e error) error {
		if e != nil || d.IsDir() || d.Type()&fs.ModeSocket != 0 {
			return nil
		}
		if artifactName.MatchString(d.Name()) || strings.HasSuffix(d.Name(), ".png") || strings.HasSuffix(d.Name(), ".wav") {
			t.Errorf("implicit file %s", path)
		}
		if b, re := os.ReadFile(path); re == nil && bytes.Contains(b, png) {
			t.Errorf("payload written to %s", path)
		}
		return nil
	})
}

func TestOutputDirToolErrorExportsBlackBox(t *testing.T) {
	r := newRig(t)
	dir := outDir(t, r, 0o700)
	v := r.check(r.run("call", "fixture.media", "--json", "--output-dir", dir, "fail=true"), 5, "tool_error")
	d := exportData(t, v)
	if len(d.Artifacts) != 2 || len(d.Warnings) != 0 {
		t.Fatal(v.stdout)
	}
	checkArtifactFiles(t, dir, d)

	locked := filepath.Join(r.root, "locked")
	if e := os.Mkdir(locked, 0o500); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	// The call failed already: the export failure is a warning next to it.
	v = r.check(r.run("call", "fixture.media", "--json", "--output-dir", locked, "fail=true"), 5, "tool_error")
	if d = exportData(t, v); len(d.Artifacts) != 0 || len(d.Warnings) != 1 || d.Warnings[0].Code != "export_failed" || len(d.Result.Content) != 3 {
		t.Fatal(v.stdout)
	}
	// The call succeeded: export_failed, exit 1, with the full result kept.
	v = r.check(r.run("call", "fixture.media", "--json", "--output-dir", locked), 1, "export_failed")
	if d = exportData(t, v); len(d.Artifacts) != 0 || len(d.Result.Content) != 3 || d.Result.Content[0].Data == "" {
		t.Fatal(v.stdout)
	}
	if entries, _ := os.ReadDir(locked); len(entries) != 0 {
		t.Fatal(entries)
	}
	h := r.run("call", "fixture.media", "--output-dir", locked)
	if h.code != 1 || h.stdout != "[image]\n[audio]\nmedia\n" || !strings.Contains(h.stderr, "could not save its image or audio blocks") {
		t.Fatalf("%d %q %q", h.code, h.stdout, h.stderr)
	}
	h = r.run("call", "fixture.media", "--output-dir", locked, "fail=true")
	if h.code != 5 || strings.Count(h.stderr, "could not save its image or audio blocks") != 1 || !strings.Contains(h.stderr, "The MCP tool returned an error result.") {
		t.Fatalf("%d %q %q", h.code, h.stdout, h.stderr)
	}
}
