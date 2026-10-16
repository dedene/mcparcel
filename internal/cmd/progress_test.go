package cmd

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/elicit"
)

// progressClock replaces the delay and the terminal check: fire ends the
// delay by hand, as if a second had passed.
type progressClock struct {
	fire      func()
	delay     time.Duration
	cancelled bool
}

func fakeProgress(t *testing.T, terminal bool) *progressClock {
	t.Helper()
	clock := &progressClock{}
	after, isTerminal := progressAfter, progressTerminal
	progressAfter = func(d time.Duration, f func()) func() bool {
		clock.delay, clock.fire = d, f
		return func() bool { clock.cancelled = true; return true }
	}
	progressTerminal = func(io.Writer) bool { return terminal }
	t.Cleanup(func() { progressAfter, progressTerminal = after, isTerminal })
	return clock
}

func TestProgressAppearsAfterDelayAndErases(t *testing.T) {
	clock := fakeProgress(t, true)
	var errOut bytes.Buffer
	p := startProgress(&Streams{Err: &errOut}, &CommandOptions{}, "Fetching acme/mcp-catalog...")
	if clock.delay != time.Second || errOut.Len() != 0 {
		t.Fatalf("delay %v, printed %q before it", clock.delay, errOut.String())
	}
	clock.fire()
	if got := errOut.String(); got != "Fetching acme/mcp-catalog..." {
		t.Fatalf("line %q", got)
	}
	p.stop()
	// The line is overwritten with spaces, no escape sequence, and the
	// cursor is back at column 0.
	want := "Fetching acme/mcp-catalog...\r" + strings.Repeat(" ", 28) + "\r"
	if got := errOut.String(); got != want || !clock.cancelled {
		t.Fatalf("after stop %q, cancelled %v", got, clock.cancelled)
	}
	p.stop()
	p.set("Fetching other/repo...")
	if errOut.String() != want {
		t.Fatalf("a stopped line printed again: %q", errOut.String())
	}
}

func TestProgressSilentWhenFast(t *testing.T) {
	clock := fakeProgress(t, true)
	var errOut bytes.Buffer
	p := startProgress(&Streams{Err: &errOut}, &CommandOptions{}, "Calling context7.resolve-library-id...")
	p.stop()
	// A delay that ends after stop, as time.AfterFunc's can, prints nothing.
	clock.fire()
	if errOut.Len() != 0 || !clock.cancelled {
		t.Fatalf("printed %q, cancelled %v", errOut.String(), clock.cancelled)
	}
}

func TestProgressSilentModes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		opts     CommandOptions
	}{
		{"not a terminal", false, CommandOptions{}},
		{"json", true, CommandOptions{JSON: true}},
		{"no input", true, CommandOptions{NoInput: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := fakeProgress(t, tc.terminal)
			var errOut bytes.Buffer
			p := startProgress(&Streams{Err: &errOut}, &tc.opts, "Loading context7 tools...")
			p.set("Loading other tools...")
			p.stop()
			if clock.fire != nil || errOut.Len() != 0 {
				t.Fatalf("scheduled %v, printed %q", clock.fire != nil, errOut.String())
			}
		})
	}
}

func TestProgressSetReplacesLine(t *testing.T) {
	clock := fakeProgress(t, true)
	var errOut bytes.Buffer
	// sync starts without a label and names each source as it fetches it.
	p := startProgress(&Streams{Err: &errOut}, &CommandOptions{}, "")
	clock.fire()
	if errOut.Len() != 0 {
		t.Fatalf("an empty label printed %q", errOut.String())
	}
	p.set("Fetching a/long-name...")
	p.set("Fetching b/c...")
	p.stop()
	want := "Fetching a/long-name...\r" + strings.Repeat(" ", 23) + "\r" + "Fetching b/c...\r" + strings.Repeat(" ", 15) + "\r"
	if got := errOut.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestProgressLabels(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	if got := callLabel("context7", "resolve-library-id"); got != "Calling context7.resolve-library-id..." {
		t.Fatal(got)
	}
	// A tool name outside the grammar could be a pasted secret: not shown.
	if got := callLabel("context7", "sk-secret value"); got != "Calling context7..." {
		t.Fatal(got)
	}
	if got := toolsLabel("context7"); got != "Loading context7 tools..." {
		t.Fatal(got)
	}
	// A connection name outside the grammar could be a pasted secret.
	if got := toolsLabel("sk-secret value"); got != "Loading tools..." {
		t.Fatal(got)
	}
	// owner/repo only, never the catalog's path or ref.
	if got := fetchLabel(config.Source{Owner: "acme", Repo: "mcp-catalog", Path: "team/mcparcel.json", Ref: "v2"}); got != "Fetching acme/mcp-catalog..." {
		t.Fatal(got)
	}
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("TERM", "xterm-256color")
	if got := fetchLabel(config.Source{Owner: "acme", Repo: "mcp-catalog"}); got != "Fetching acme/mcp-catalog…" {
		t.Fatal(got)
	}
}

// A server prompt during a call erases the progress line before the
// question shows.
func TestProgressStopsBeforeElicit(t *testing.T) {
	clock := fakeProgress(t, true)
	var errOut bytes.Buffer
	p := startProgress(&Streams{Err: &errOut}, &CommandOptions{}, "Calling context7.resolve-library-id...")
	clock.fire()
	var seen string
	ask := stopBefore(p, func(context.Context, elicit.Prompt) elicit.Answer {
		seen = errOut.String()
		return elicit.Answer{Action: "accept"}
	})
	answer := ask(context.Background(), elicit.Prompt{})
	want := "Calling context7.resolve-library-id...\r" + strings.Repeat(" ", 38) + "\r"
	if seen != want || answer.Action != "accept" || !clock.cancelled {
		t.Fatalf("seen %q, answer %+v, cancelled %v", seen, answer, clock.cancelled)
	}
	if stopBefore(p, nil) != nil {
		t.Fatal("a client with no prompt got one")
	}
}

func TestSyncNamesEachSourceBeforeItsFetch(t *testing.T) {
	service, state := syncSeed(t)
	var events []string
	f := service.Fetcher
	service.Fetcher = syncFetcher(func(ctx context.Context, s config.Source) (catalog.Snapshot, error) {
		events = append(events, "fetch "+s.ID)
		return f.Fetch(ctx, s)
	})
	fetching := func(s config.Source) { events = append(events, "name "+s.ID) }
	if _, err := runSync(context.Background(), service, state, "", false, nil, fetching); err != nil {
		t.Fatal(err)
	}
	want := []string{"name github-1", "fetch github-1", "name github-2", "fetch github-2", "name github-3", "fetch github-3"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", events, want)
	}
}

// A label wider than the terminal is cut to one row, so erasing it leaves
// nothing behind.
func TestProgressCutsLabelToTerminalWidth(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("TERM", "xterm-256color")
	clock := fakeProgress(t, true)
	columns := progressColumns
	t.Cleanup(func() { progressColumns = columns })
	progressColumns = func(io.Writer) int { return 20 }
	var errOut bytes.Buffer
	label := "Calling github:acme/platform-catalog#context7.some_long_tool_name…"
	p := startProgress(&Streams{Err: &errOut}, &CommandOptions{}, label)
	clock.fire()
	line := errOut.String()
	if n := len([]rune(line)); n != 19 || !strings.HasSuffix(line, "…") || !strings.HasPrefix(line, "Calling github:acm") {
		t.Fatalf("line %q (%d runes)", line, n)
	}
	p.stop()
	if got, want := errOut.String(), line+"\r"+strings.Repeat(" ", 19)+"\r"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// An unknown width (a failed ioctl) leaves the label as it is.
	progressColumns = func(io.Writer) int { return 0 }
	errOut.Reset()
	p = startProgress(&Streams{Err: &errOut}, &CommandOptions{}, label)
	clock.fire()
	if errOut.String() != label {
		t.Fatalf("uncut line %q", errOut.String())
	}
	p.stop()
}

func TestFitColumns(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"a longer label", 10, "a longe..."},
		{"abcdef", 3, "abc"},
		{"abcdef", 2, "ab"},
	} {
		if got := fitColumns(tc.in, tc.n); got != tc.want {
			t.Errorf("fitColumns(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
