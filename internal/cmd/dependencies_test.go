//go:build !mcparceltest

package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/elicit"
)

// The production binary answers a prompt only from a real terminal: fixture
// stand-in files in StateDir change nothing.
func TestNewTerminalNeedsTerminal(t *testing.T) {
	state := t.TempDir()
	for _, name := range []string{"fixture-terminal", "fixture-dialog-answer"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte("3\nAllow for this session\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	paths := config.Paths{StateDir: state}
	for _, files := range [][2]*os.File{{r, w}, {nil, w}, {r, nil}, {nil, nil}} {
		if newTerminal(paths, files[0], files[1]) != nil {
			t.Fatal("terminal without a tty")
		}
	}
}

func TestPollReader(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd, ok := fileFD(r)
	if !ok {
		t.Fatal("no fd")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err = w.WriteString("3\n"); err != nil {
		t.Fatal(err)
	}
	p := elicit.Prompt{Message: "Allow?", Persist: []string{"session"}}
	if a := elicit.Ask(ctx, &pollReader{ctx: ctx, fd: fd}, io.Discard, "fixture", p); a.Action != "accept" || a.Persist != "session" {
		t.Fatal(a)
	}
	done := make(chan error)
	go func() {
		_, err := (&pollReader{ctx: ctx, fd: fd}).Read(make([]byte, 1))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read ignored the end of ctx")
	}
	_ = w.Close()
	if _, err = (&pollReader{ctx: context.Background(), fd: fd}).Read(make([]byte, 1)); err != io.EOF {
		t.Fatal(err)
	}
}

// A line typed before a prompt opens cannot answer it: only input after the
// prompt's reader is created counts.
func TestPromptReaderDropsTypeAhead(t *testing.T) {
	master, slave := openPTY(t)
	if _, err := master.WriteString("3\n"); err != nil {
		t.Fatal(err)
	}
	fd, ok := fileFD(slave)
	if !ok {
		t.Fatal("no fd")
	}
	ctx := context.Background()
	r := promptReader(ctx, fd)
	if _, err := master.WriteString("2\n"); err != nil {
		t.Fatal(err)
	}
	p := elicit.Prompt{Message: "Allow?", Persist: []string{"session"}}
	if a := elicit.Ask(ctx, r, io.Discard, "fixture", p); a.Action != "accept" || a.Persist != "" {
		t.Fatal(a)
	}
	if _, err := master.WriteString("3\n"); err != nil {
		t.Fatal(err)
	}
	if a := elicit.Ask(ctx, promptReader(ctx, -1), io.Discard, "fixture", p); a.Action != "cancel" {
		t.Fatal("unflushable input answered", a)
	}
}
