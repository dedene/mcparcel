package output

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func exportDir(t *testing.T) (*ExportDir, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := OpenExportDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	abs, _ := filepath.EvalSymlinks(dir)
	return d, abs
}

func wantExportCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("%v want %s", err, code)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestExportWritesImageAndAudio(t *testing.T) {
	d, dir := exportDir(t)
	result := json.RawMessage(`{"content":[{"type":"text","text":"hi"},{"type":"image","mimeType":"image/png","data":"` + b64("PNGDATA") + `"},{"type":"audio","mimeType":"audio/wav","data":"` + base64.RawStdEncoding.EncodeToString([]byte("WAVE!")) + `"}]}`)
	artifacts, err := d.Export(result, "p")
	if err != nil || len(artifacts) != 2 {
		t.Fatal(artifacts, err)
	}
	want := []Artifact{
		{Index: 1, Type: "image", MIMEType: "image/png", Path: filepath.Join(d.abs, "p-1.png"), Bytes: 7},
		{Index: 2, Type: "audio", MIMEType: "audio/wav", Path: filepath.Join(d.abs, "p-2.wav"), Bytes: 5},
	}
	for i, a := range artifacts {
		if a != want[i] || !filepath.IsAbs(a.Path) {
			t.Fatalf("%+v want %+v", a, want[i])
		}
		info, err := os.Stat(filepath.Join(dir, filepath.Base(a.Path)))
		if err != nil || info.Mode().Perm() != 0o600 || info.Size() != a.Bytes {
			t.Fatal(info, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "p-1.png")); string(b) != "PNGDATA" {
		t.Fatal(string(b))
	}
	b, _ := json.Marshal(artifacts[0])
	if string(b) != `{"index":1,"type":"image","mimeType":"image/png","path":`+string(mustJSON(t, want[0].Path))+`,"bytes":7}` {
		t.Fatal(string(b))
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExportExtensionFromMIME(t *testing.T) {
	for mimeType, ext := range map[string]string{
		"image/png": "png", "IMAGE/PNG; charset=x": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp", "image/svg+xml": "svg",
		"audio/wav": "wav", "audio/x-wav": "wav", "audio/mpeg": "mp3", "audio/ogg": "ogg", "audio/webm": "webm", "audio/flac": "flac", "audio/mp4": "m4a", "audio/aac": "m4a",
		"": "bin", "image/../../etc/passwd": "bin", "image/png/../../x": "bin", "text/html": "bin", "image/x-icon": "bin", "../png": "bin", "image/png\x00": "bin",
	} {
		if got := extFor(mimeType); got != ext {
			t.Errorf("%q: %q want %q", mimeType, got, ext)
		}
	}
	d, dir := exportDir(t)
	result := json.RawMessage(`{"content":[{"type":"image","mimeType":"../../../escape/x.png","data":"` + b64("x") + `"}]}`)
	artifacts, err := d.Export(result, "p")
	if err != nil || len(artifacts) != 1 || filepath.Base(artifacts[0].Path) != "p-0.bin" {
		t.Fatal(artifacts, err)
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "p-0.bin" {
		t.Fatal(names)
	}
}

func TestExportIgnoresOtherBlocks(t *testing.T) {
	d, dir := exportDir(t)
	result := json.RawMessage(`{"content":[{"type":"text","text":"x"},{"type":"resource","resource":{"uri":"a://b","blob":"` + b64("blob") + `"}},{"type":"future_kind","data":"` + b64("x") + `"},{"type":"tool_result","content":[{"type":"image","mimeType":"image/png","data":"` + b64("nested") + `"}]},"odd",7,null,{"type":7}],"structuredContent":{"type":"image","data":"` + b64("x") + `"}}`)
	artifacts, err := d.Export(result, "p")
	if err != nil || len(artifacts) != 0 || len(dirNames(t, dir)) != 0 {
		t.Fatal(artifacts, err, dirNames(t, dir))
	}
	for _, odd := range []string{`{}`, `{"content":"x"}`, `{"content":{"type":"image"}}`, `[]`} {
		if artifacts, err = d.Export(json.RawMessage(odd), "p"); err != nil || len(artifacts) != 0 {
			t.Fatal(odd, artifacts, err)
		}
	}
}

func TestExportTooManyBlocks(t *testing.T) {
	d, dir := exportDir(t)
	block := `{"type":"image","mimeType":"image/png","data":"` + b64("x") + `"}`
	blocks := strings.TrimSuffix(strings.Repeat(block+",", MaxArtifacts+1), ",")
	artifacts, err := d.Export(json.RawMessage(`{"content":[`+blocks+`]}`), "p")
	wantExportCode(t, err, "export_failed")
	if len(artifacts) != 0 || len(dirNames(t, dir)) != 0 {
		t.Fatal(artifacts, dirNames(t, dir))
	}
	blocks = strings.TrimSuffix(strings.Repeat(block+",", MaxArtifacts), ",")
	if artifacts, err = d.Export(json.RawMessage(`{"content":[`+blocks+`]}`), "q"); err != nil || len(artifacts) != MaxArtifacts {
		t.Fatal(len(artifacts), err)
	}
}

func TestExportNeverOverwrites(t *testing.T) {
	d, dir := exportDir(t)
	outside := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p-1.png"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "q-1.png")); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"content":[{"type":"image","mimeType":"image/png","data":"` + b64("first") + `"},{"type":"image","mimeType":"image/png","data":"` + b64("new") + `"}]}`)
	for _, prefix := range []string{"p", "q"} {
		artifacts, err := d.Export(result, prefix)
		wantExportCode(t, err, "export_failed")
		if len(artifacts) != 1 || artifacts[0].Index != 0 {
			t.Fatal(prefix, artifacts)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "p-1.png")); string(b) != "existing" {
		t.Fatal(string(b))
	}
	if b, _ := os.ReadFile(outside); string(b) != "outside" {
		t.Fatal(string(b))
	}
	if target, err := os.Readlink(filepath.Join(dir, "q-1.png")); err != nil || target != outside {
		t.Fatal(target, err)
	}
}

func TestExportInvalidBase64(t *testing.T) {
	d, dir := exportDir(t)
	for _, bad := range []string{`"!!not base64!!"`, `7`, `null`} {
		result := json.RawMessage(`{"content":[{"type":"image","mimeType":"image/png","data":"` + b64("ok") + `"},{"type":"audio","mimeType":"audio/wav","data":` + bad + `}]}`)
		artifacts, err := d.Export(result, "p")
		wantExportCode(t, err, "export_failed")
		if len(artifacts) != 0 || len(dirNames(t, dir)) != 0 {
			t.Fatal(bad, artifacts, dirNames(t, dir))
		}
	}
}

func TestOpenExportDirRejects(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "missing"), file} {
		d, err := OpenExportDir(dir)
		wantExportCode(t, err, "invalid_arguments")
		if d != nil || err.Error() != "--output-dir must name an existing directory." || ExitCode(err) != 2 {
			t.Fatal(dir, err)
		}
	}
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "out"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)
	d, err := OpenExportDir("out")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !filepath.IsAbs(d.abs) || filepath.Base(d.abs) != "out" {
		t.Fatal(d.abs)
	}
}

func TestExportPrefix(t *testing.T) {
	now := time.Date(2026, 10, 6, 1, 2, 3, 0, time.FixedZone("x", 2*3600))
	a, b := ExportPrefix(now), ExportPrefix(now)
	if !regexp.MustCompile(`^mcparcel-20261005T230203Z-[0-9a-f]{6}$`).MatchString(a) || a == b {
		t.Fatal(a, b)
	}
}

func TestExportFailedCode(t *testing.T) {
	e := NewError("export_failed", nil)
	if ExitCode(e) != 1 || e.Message != "The call finished, but MCParcel could not save its image or audio blocks." || e.NextAction != "The full result is in data.result; fix the output directory. Do not call the tool again just to export." {
		t.Fatal(e)
	}
}
