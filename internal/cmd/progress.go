package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// progressDelay is how long a command waits before it says what it waits for.
const progressDelay = time.Second

// progressAfter is time.AfterFunc; tests replace it to end the delay by hand.
var progressAfter = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// progressTerminal is stderrTerminal; tests replace it.
var progressTerminal = stderrTerminal

// progressColumns is the terminal's width, 0 when unknown; tests replace it.
var progressColumns = writerColumns

// progress is the one stderr line a slow command prints, such as "Fetching
// acme/mcp-catalog…": it appears once the operation has taken progressDelay
// and stop erases it, so what follows prints as if it had never been there.
// It is cut to the terminal's width so it stays on one row.
// It is a static line, never a spinner, so MCPARCEL_REDUCE_MOTION has nothing
// to turn off. Labels name connection IDs, tool names and owner/repo only.
type progress struct {
	mu      sync.Mutex
	w       io.Writer
	label   string
	width   int  // columns the line on screen takes; 0 when none is
	due     bool // the delay has passed
	stopped bool
	cancel  func() bool
}

// startProgress starts the line for label. It prints nothing, ever, with
// --json or --no-input or when stderr is not a terminal.
func startProgress(s *Streams, opts *CommandOptions, label string) *progress {
	p := &progress{w: s.Err, label: label}
	if opts.JSON || opts.NoInput || !progressTerminal(s.Err) {
		p.stopped = true
		return p
	}
	p.cancel = progressAfter(progressDelay, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.due = true
		p.draw()
	})
	return p
}

// set replaces the label: on screen at once when the line is up, else when
// the delay ends.
func (p *progress) set(label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if label == p.label {
		return
	}
	p.label = label
	p.draw()
}

// stop erases the line; later calls do nothing. Once it returns the line
// never appears.
func (p *progress) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	p.erase()
	p.stopped = true
}

func (p *progress) draw() {
	if p.stopped || !p.due || p.label == "" {
		return
	}
	p.erase()
	line := p.label
	// One column short of the width, so the line never wraps (erase clears
	// only the row the cursor is on) and never leaves the cursor waiting
	// to wrap. The width is read on each draw, so a resize counts.
	if cols := progressColumns(p.w); cols > 1 {
		line = fitColumns(line, cols-1)
	}
	fmt.Fprint(p.w, line)
	p.width = utf8.RuneCountInString(line)
}

// fitColumns cuts s to n columns, ending it with an ellipsis when cut.
// Labels are ASCII apart from the ellipsis, so a rune is a column.
func fitColumns(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	e := []rune(ellipsis())
	if n <= len(e) {
		return string(r[:n])
	}
	return string(r[:n-len(e)]) + string(e)
}

// erase overwrites the line with spaces, which needs no escape sequence.
func (p *progress) erase() {
	if p.width > 0 {
		fmt.Fprint(p.w, "\r"+strings.Repeat(" ", p.width)+"\r")
		p.width = 0
	}
}

// ellipsis ends a progress label: "…" on a UTF-8 terminal, else "...".
func ellipsis() string {
	if unicodeTerminal(os.Getenv) {
		return "…"
	}
	return "..."
}
