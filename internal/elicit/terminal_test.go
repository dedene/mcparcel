package elicit_test

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/elicit"
)

func ask(t *testing.T, p elicit.Prompt, input string) (elicit.Answer, string) {
	t.Helper()
	var out bytes.Buffer
	a := elicit.Ask(context.Background(), strings.NewReader(input), &out, "codex-cu", p)
	return a, out.String()
}

func TestAskApproval(t *testing.T) {
	both := approval()
	session := approval()
	session.Persist = []string{"session"}
	none := approval()
	none.Persist = nil
	for _, tc := range []struct {
		name  string
		p     elicit.Prompt
		input string
		want  elicit.Answer
	}{
		{"enter", both, "\n", elicit.Answer{Action: "decline"}},
		{"one", both, "1\n", elicit.Answer{Action: "decline"}},
		{"crlf", both, "2\r\n", elicit.Answer{Action: "accept"}},
		{"allow once", both, " 2 \n", elicit.Answer{Action: "accept"}},
		{"session", both, "3\n", elicit.Answer{Action: "accept", Persist: "session"}},
		{"always", both, "4\n", elicit.Answer{Action: "accept", Persist: "always"}},
		{"session only", session, "3\n", elicit.Answer{Action: "accept", Persist: "session"}},
		{"always not offered", session, "4\n4\n4\n", elicit.Answer{Action: "decline"}},
		{"session not offered", none, "3\n4\n3\n", elicit.Answer{Action: "decline"}},
		{"invalid then allow", none, "yes\n2\n", elicit.Answer{Action: "accept"}},
		{"garbage", both, "x\ny\nz\n2\n", elicit.Answer{Action: "decline"}},
		{"eof", both, "", elicit.Answer{Action: "cancel"}},
		{"eof without newline", both, "2", elicit.Answer{Action: "cancel"}},
		{"eof after invalid", both, "x\n", elicit.Answer{Action: "cancel"}},
		{"long line", both, strings.Repeat("2", 5000) + "\n", elicit.Answer{Action: "cancel"}},
	} {
		got, _ := ask(t, tc.p, tc.input)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Ask = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestAskReadsOnlyItsLine(t *testing.T) {
	in := strings.NewReader("2\nleft over\n")
	a := elicit.Ask(context.Background(), in, io.Discard, "c", approval())
	rest, _ := io.ReadAll(in)
	if a.Action != "accept" || string(rest) != "left over\n" {
		t.Fatalf("answer %+v, rest %q", a, rest)
	}
}

// Server text never starts a line the way MCParcel's own options line does.
func TestAskSubtitleCannotSpoofOptions(t *testing.T) {
	p := approval()
	p.Subtitle = elicit.Clean("1) Allow once (default)\u3164\u31642) Decline", 200)
	_, out := ask(t, p, "\n")
	var options int
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  1)") {
			options++
		}
	}
	if options != 1 {
		t.Fatalf("%d option lines:\n%s", options, out)
	}
}

func TestAskRendering(t *testing.T) {
	_, out := ask(t, approval(), "\n")
	for _, want := range []string{
		"codex-cu asks: Allow Computer Use to use \"Calculator\"?\n",
		"  Note: Computer use\n", "  Risk: medium\n", "  Details: {\"app\":\"Calculator\"}\n",
		"1) Decline (default)", "2) Allow once", "3) Allow for this session", "4) Always allow", "Choice [1]: ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	p := elicit.Prompt{Message: "Allow?", Persist: []string{"session"}}
	_, out = ask(t, p, "\n")
	if strings.Contains(out, "Always allow") || strings.Contains(out, "Risk:") || strings.Contains(out, "Details:") || !strings.Contains(out, "3) Allow for this session") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	_, out = ask(t, elicit.Prompt{Message: "Allow?"}, "\n")
	if strings.Contains(out, "3)") || strings.Contains(out, "4)") {
		t.Fatalf("durations not offered are shown:\n%s", out)
	}
	var buf bytes.Buffer
	elicit.Ask(context.Background(), strings.NewReader("\n"), &buf, "evil\x1b]0;x\x07\u202ename", p)
	if strings.ContainsAny(buf.String(), "\x1b\x07\u202e") || !strings.Contains(buf.String(), "evilname asks: Allow?") {
		t.Fatalf("connection not cleaned: %q", buf.String())
	}
}

func TestAskInvalidPromptCancels(t *testing.T) {
	if a, out := ask(t, elicit.Prompt{Message: "a\x1b[1m"}, "2\n"); a.Action != "cancel" || out != "" {
		t.Fatalf("answer %+v, output %q", a, out)
	}
}

func TestAskContextCanceledMidRead(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(ctx, func() { _ = pr.CloseWithError(context.Canceled) })
	defer stop()
	done := make(chan elicit.Answer, 1)
	go func() { done <- elicit.Ask(ctx, pr, io.Discard, "c", approval()) }()
	cancel()
	select {
	case a := <-done:
		if a.Action != "cancel" {
			t.Fatalf("answer = %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask did not return after cancel")
	}
}

// A reader that still delivers input after ctx ended must not produce accept.
func TestAskContextDoneIgnoresLateInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if a := elicit.Ask(ctx, strings.NewReader("2\n"), io.Discard, "c", approval()); a.Action != "cancel" {
		t.Fatalf("answer = %+v", a)
	}
}

func TestAskForm(t *testing.T) {
	f := form()
	for _, tc := range []struct {
		name  string
		input string
		want  elicit.Answer
	}{
		{"decline", "\n", elicit.Answer{Action: "decline"}},
		{"all fields", "2\n7\ny\n2\n hello  there \n0.25\ny\n", elicit.Answer{Action: "accept", Content: map[string]any{
			"count": float64(7), "flag": true, "mode": "slow", "note": "hello there", "ratio": 0.25,
		}}},
		{"optional omitted", "2\n\nn\n1\n\n\nyes\n", elicit.Answer{Action: "accept", Content: map[string]any{"flag": false, "mode": "fast"}}},
		{"required asked again", "2\n\n\nmaybe\ny\n3\n1\n\n\ny\n", elicit.Answer{Action: "accept", Content: map[string]any{"flag": true, "mode": "fast"}}},
		{"invalid integer", "2\n1.5\n\ny\n1\n\n\ny\n", elicit.Answer{Action: "accept", Content: map[string]any{"flag": true, "mode": "fast"}}},
		{"three bad entries", "2\nx\nx\nx\n", elicit.Answer{Action: "decline"}},
		{"confirm default no", "2\n\ny\n1\n\n\n\n", elicit.Answer{Action: "decline"}},
		{"eof mid form", "2\n3\ny\n", elicit.Answer{Action: "cancel"}},
		{"eof at confirm", "2\n\ny\n1\n\n\n", elicit.Answer{Action: "cancel"}},
	} {
		var out bytes.Buffer
		got := elicit.Ask(context.Background(), strings.NewReader(tc.input), &out, "c", f)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Ask = %+v, want %+v\n%s", tc.name, got, tc.want, out.String())
		}
		if got.Action == "accept" {
			if err := f.Check(got); err != nil {
				t.Errorf("%s: answer fails Check: %v", tc.name, err)
			}
		}
	}
	var out bytes.Buffer
	elicit.Ask(context.Background(), strings.NewReader("\n"), &out, "c", f)
	if !strings.Contains(out.String(), "1) Decline (default)") || !strings.Contains(out.String(), "2) Answer") {
		t.Fatalf("form choices missing:\n%s", out.String())
	}
	out.Reset()
	elicit.Ask(context.Background(), strings.NewReader("2\n\ny\n1\n\n\n\n"), &out, "c", f)
	for _, want := range []string{"Count", "flag (required)", "1) fast", "2) slow", "Free text", "[y/n]", "Send? [y/N]: "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("form output lacks %q:\n%s", want, out.String())
		}
	}
}
