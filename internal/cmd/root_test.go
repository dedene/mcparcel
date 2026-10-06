package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/testutil"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Run(context.Background(), args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersionPrintsToStdout(t *testing.T) {
	code, stdout, stderr := run(t, "version")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr: %q)", code, ExitOK, stderr)
	}
	if stdout != "mcparcel dev\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "mcparcel dev\n")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestVersionFlagMatchesVersionCommand(t *testing.T) {
	_, want, _ := run(t, "version")
	for _, args := range [][]string{{"--version"}, {"--version", "call"}, {"tools", "--version"}} {
		code, stdout, stderr := run(t, args...)
		if code != ExitOK || stdout != want || stderr != "" {
			t.Fatalf("%v: exit %d stdout %q stderr %q, want %q", args, code, stdout, stderr, want)
		}
	}
	defer func(v, c, d string) { version, commit, date = v, c, d }(version, commit, date)
	version, commit, date = "1.2.3", "abc1234", "2026-10-06"
	_, want, _ = run(t, "version")
	if _, got, _ := run(t, "--version"); got != want || got != "mcparcel 1.2.3 (abc1234, 2026-10-06)\n" {
		t.Fatalf("--version %q, version %q", got, want)
	}
	if _, help, _ := run(t, "--help"); !strings.Contains(help, "--version") {
		t.Fatalf("help does not list --version:\n%s", help)
	}
}

func TestHelpIsEnglishAndExitsZero(t *testing.T) {
	code, stdout, _ := run(t, "--help")
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d", code, ExitOK)
	}
	for _, want := range []string{"Usage: mcparcel", "version", "Print the mcparcel version."} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "spike") {
		t.Errorf("help output must not list the hidden spike command:\n%s", stdout)
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	code, stdout, stderr := run(t, "definitely-not-a-command")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "mcparcel --help") {
		t.Fatalf("stderr = %q, want a pointer to --help", stderr)
	}
}

func TestNoArgsIsUsageError(t *testing.T) {
	code, _, stderr := run(t)
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d (stderr: %q)", code, ExitUsage, stderr)
	}
}

func TestNewCommandUsageIsSafeJSON(t *testing.T) {
	for _, argv := range [][]string{
		{"call", "--json"},
		{"tools", "--json"},
		{"runtime", "bogus-CANARY", "--json"},
		{"auth", "--json"},
		{"auth", "login", "--json"},
		{"auth", "logout", "--json"},
		{"auth", "bogus-CANARY", "--json"},
		{"call", "fixture.echo", "--timeout", "CANARY", "--json"},
		{"call", "fixture.echo", "--args", "", "--json"},
		{"call", "fixture.echo", "--args-file=", "--json"},
		{"call", "fixture.echo", "--args={}", "--args={}", "--json"},
		{"call", "fixture.echo", "--args-file=-", "--args-file=-", "--json"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			code, stdout, stderr := run(t, argv...)
			if code != 2 || !strings.Contains(stdout, `"code":"invalid_arguments"`) || strings.Count(stdout, "\n") != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if strings.Contains(stdout+stderr, "CANARY") {
				t.Fatalf("unsafe diagnostic: %q %q", stdout, stderr)
			}
		})
	}
}

func TestPayloadIsNotJSONIntent(t *testing.T) {
	for _, argv := range [][]string{
		{"call", "fixture.echo", "--args", "--json"},
		{"call", "fixture.echo", "--args-file", "--json"},
		{"call", "fixture.echo", "--timeout", "--json"},
		{"call", "fixture.echo", "--args=--json"},
		{"call", "fixture.echo", "--json=false", "--args", "bad"},
		{"call", "fixture.echo", "--", "--json"},
	} {
		code, stdout, stderr := run(t, argv...)
		if code != 2 || stdout != "" || stderr == "" {
			t.Errorf("%q: code=%d stdout=%q stderr=%q", argv, code, stdout, stderr)
		}
	}
}

func TestProductHelp(t *testing.T) {
	code, stdout, stderr := run(t, "--help")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{"tools", "call", "runtime", "auth", "config", "import", "--json", "--no-input"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help missing %s: %s", want, stdout)
		}
	}
	if strings.Contains(stdout, "daemon") || strings.Contains(stdout, "spike") {
		t.Errorf("hidden commands in help: %s", stdout)
	}
}

func TestSplitCallTarget(t *testing.T) {
	for _, target := range []string{"fixture.echo.dotted", "local:fixture.echo.dotted"} {
		connection, tool, err := splitCallTarget(target)
		if err != nil || connection != strings.TrimSuffix(target, ".echo.dotted") || tool != "echo.dotted" {
			t.Errorf("%s: %s %s %v", target, connection, tool, err)
		}
	}
	for _, target := range []string{"", "fixture", ".echo", "fixture.", "github:owner/repo.echo", "owner/repo.echo", "local:.echo"} {
		if _, _, err := splitCallTarget(target); err == nil {
			t.Errorf("accepted %q", target)
		}
	}
}

// A failed stdout write must never cause a second JSON envelope attempt.
func TestProductWriterFailure(t *testing.T) {
	_, env := testutil.IsolatedPaths(t)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	writer := &failingProductWriter{}
	var errOut bytes.Buffer
	code := Run(context.Background(), []string{"runtime", "status", "--json"}, strings.NewReader(""), writer, &errOut)
	if code != ExitInternal || writer.calls != 1 {
		t.Fatalf("code=%d writes=%d stderr=%q", code, writer.calls, errOut.String())
	}
	if strings.Contains(errOut.String(), "WRITER-CANARY") {
		t.Fatal("writer error leaked")
	}
}

type failingProductWriter struct{ calls int }

func (w *failingProductWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, errors.New("WRITER-CANARY")
}

func TestHiddenDaemonRequiresInheritedLock(t *testing.T) {
	for _, argv := range [][]string{{"daemon"}, {"daemon", "--lock-fd=4", "--json"}} {
		code, stdout, stderr := run(t, argv...)
		if code != 2 {
			t.Fatalf("%q: code=%d stdout=%q stderr=%q", argv, code, stdout, stderr)
		}
		if len(argv) == 1 && (stdout != "" || stderr == "") {
			t.Fatalf("human failure: %q %q", stdout, stderr)
		}
		if len(argv) > 1 && (!strings.Contains(stdout, `"code":"unsafe_local_path"`) || stderr != "") {
			t.Fatalf("JSON failure: %q %q", stdout, stderr)
		}
	}
}

func TestCanceledArgumentPayload(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	input := &observedPayloadReader{Reader: reader, started: started}
	done := make(chan int, 1)
	var out, diagnostics bytes.Buffer
	go func() {
		done <- Run(ctx, []string{"call", "fixture.counter", "--args-file", "-", "--json"}, input, &out, &diagnostics)
	}()
	<-started
	cancel()
	select {
	case code := <-done:
		if code != 130 || !strings.Contains(out.String(), `"code":"canceled"`) {
			t.Fatal(code, out.String())
		}
	case <-time.After(time.Second):
		writer.Close()
		<-done
		t.Fatal("payload read ignored cancellation")
	}
}

type observedPayloadReader struct {
	Reader  *io.PipeReader
	started chan struct{}
	once    sync.Once
}

func (r *observedPayloadReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.Reader.Read(p)
}
func (r *observedPayloadReader) Close() error { return r.Reader.Close() }

func TestFIFOArgumentPayloadCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload")
	if e := syscall.Mkfifo(path, 0o600); e != nil {
		t.Fatal(e)
	}
	file, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	reader := &observedReadCloser{ReadCloser: &fifoPayload{File: file, ctx: ctx}, started: started}
	done := make(chan error, 1)
	go func() { _, e := parsePayload(ctx, nil, "", reader); done <- e }()
	<-started
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("read succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO ignored cancellation")
	}
}

type observedReadCloser struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (r *observedReadCloser) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.ReadCloser.Read(p)
}

func TestQualifiedCallTarget(t *testing.T) {
	for _, row := range []struct{ target, connection, tool string }{{"github:example/tools.v2#paper.echo.dotted", "github:example/tools.v2#paper", "echo.dotted"}, {"local:paper.echo", "local:paper", "echo"}} {
		c, tool, e := splitCallTarget(row.target)
		if e != nil || c != row.connection || tool != row.tool {
			t.Fatal(c, tool, e)
		}
	}
}

func TestMetadataParserSafety(t *testing.T) {
	metadataEnv(t)
	for _, argv := range [][]string{{"catalog", "--domain", ""}, {"catalog", "--domain", "other", "--domain", "other"}, {"inspect"}, {"list", "extra"}} {
		code, out, stderr := run(t, append(argv, "--json")...)
		if code != 2 || stderr != "" || !strings.Contains(out, `"code":"invalid_arguments"`) {
			t.Fatal(code, out, stderr)
		}
	}
	intent := scanIntent([]string{"catalog", "--domain", "--json"})
	if !intent.product || intent.json {
		t.Fatal(intent)
	}
}

func TestSelectionParserSafety(t *testing.T) {
	metadataEnv(t)
	for _, argv := range [][]string{{"enable"}, {"disable"}, {"tools", "enable", "paper"}, {"tools", "disable", "paper", "read", "--cached"}, {"tools", "paper", "extra"}} {
		code, out, stderr := run(t, append(argv, "--json")...)
		if code != 2 || stderr != "" || !strings.Contains(out, `"code":"invalid_arguments"`) {
			t.Fatal(code, out, stderr)
		}
	}
}

func TestLocalParserSafety(t *testing.T) {
	metadataEnv(t)
	for _, argv := range [][]string{{"local", "add", "--file", ""}, {"local", "add", "--file", "a", "--file", "b"}, {"local", "update", "paper"}, {"local", "remove"}} {
		code, out, stderr := run(t, append(argv, "--json")...)
		if code != 2 || stderr != "" || !strings.Contains(out, `"code":"invalid_arguments"`) {
			t.Fatal(code, out, stderr)
		}
	}
	intent := scanIntent([]string{"local", "add", "--file", "--json"})
	if !intent.product || intent.json {
		t.Fatal(intent)
	}
}
