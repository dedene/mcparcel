package elicit

import (
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
)

const maxLine = 4096

// choices numbers the approval options; the terminal and the dialog share it.
// No lasting approval: a server does not store the persistence it receives.
var choices = []struct {
	label  string
	answer Answer
}{
	{"Decline", Answer{Action: "decline"}},
	{"Allow once", Answer{Action: "accept"}},
	{"Allow for this session", Answer{Action: "accept", Persist: "session"}},
}

var (
	declined = Answer{Action: "decline"}
	canceled = Answer{Action: "cancel"}
)

// offered reports whether choice i applies to p: a persistence only when p
// lists it.
func offered(p Prompt, i int) bool {
	persist := choices[i].answer.Persist
	return persist == "" || slices.Contains(p.Persist, persist)
}

// details are p's optional lines after the message.
func details(p Prompt) []string {
	var out []string
	if p.Subtitle != "" {
		out = append(out, "Note: "+p.Subtitle)
	}
	if p.RiskLevel != "" {
		out = append(out, "Risk: "+p.RiskLevel)
	}
	if p.Details != "" {
		out = append(out, "Details: "+p.Details)
	}
	return out
}

type term struct {
	ctx context.Context
	in  io.Reader
	out io.Writer
}

// Ask shows p on out and reads the answer from in, one line at a time. Enter
// declines; EOF, a read error or the end of ctx cancels, and the caller makes
// in return when ctx ends. It never accepts without explicit input.
func Ask(ctx context.Context, in io.Reader, out io.Writer, connection string, p Prompt) Answer {
	if p.Valid() != nil {
		return canceled
	}
	t := &term{ctx, in, out}
	fmt.Fprintf(out, "%s asks: %s\n", Clean(connection, 100), p.Message)
	for _, line := range details(p) {
		fmt.Fprintf(out, "  %s\n", line)
	}
	if len(p.Fields) > 0 {
		return t.form(p)
	}
	var opts []string
	for i, c := range choices {
		if offered(p, i) {
			opts = append(opts, fmt.Sprintf("%d) %s", i+1, c.label))
		}
	}
	opts[0] += " (default)"
	fmt.Fprintf(out, "  %s\n", strings.Join(opts, "  "))
	for range 3 {
		line, ok := t.line("Choice [1]: ")
		if !ok {
			return canceled
		}
		if line == "" {
			return declined
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(choices) && offered(p, n-1) {
			return choices[n-1].answer
		}
		fmt.Fprintln(out, "Invalid choice.")
	}
	return declined
}

func (t *term) form(p Prompt) Answer {
	fmt.Fprintln(t.out, "  1) Decline (default)  2) Answer")
	for bad := 0; ; {
		line, ok := t.line("Choice [1]: ")
		if !ok {
			return canceled
		}
		if line == "" || line == "1" {
			return declined
		}
		if line == "2" {
			break
		}
		fmt.Fprintln(t.out, "Invalid choice.")
		if bad++; bad == 3 {
			return declined
		}
	}
	content := map[string]any{}
	for _, f := range p.Fields {
		v, stop := t.field(f)
		if stop != nil {
			return *stop
		}
		if v != nil {
			content[f.Name] = v
		}
	}
	line, ok := t.line("Send? [y/N]: ")
	if !ok {
		return canceled
	}
	if yes, ok := parseBool(line); ok && yes {
		return Answer{Action: "accept", Content: content}
	}
	return declined
}

// field reads one value: nil when an optional field is left empty, or a
// non-nil stop answer that ends the form.
func (t *term) field(f Field) (any, *Answer) {
	label := f.Name
	if f.Title != "" {
		label = f.Title
	}
	if f.Required {
		label += " (required)"
	}
	fmt.Fprintln(t.out, label)
	if f.Description != "" {
		fmt.Fprintf(t.out, "  %s\n", f.Description)
	}
	prompt := "Value: "
	switch {
	case f.Enum != nil:
		opts := make([]string, len(f.Enum))
		for i, v := range f.Enum {
			opts[i] = fmt.Sprintf("%d) %s", i+1, v)
		}
		fmt.Fprintf(t.out, "  %s\n", strings.Join(opts, "  "))
		prompt = "Choice: "
	case f.Type == "boolean":
		prompt = "Value [y/n]: "
	}
	for range 3 {
		line, ok := t.line(prompt)
		if !ok {
			return nil, &canceled
		}
		if line == "" && !f.Required {
			return nil, nil
		}
		if v, ok := parse(f, line); ok {
			return v, nil
		}
		if line == "" {
			fmt.Fprintln(t.out, "A value is required.")
		} else {
			fmt.Fprintln(t.out, "Invalid value.")
		}
	}
	return nil, &declined
}

func parse(f Field, line string) (any, bool) {
	switch {
	case f.Enum != nil:
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(f.Enum) {
			return f.Enum[n-1], true
		}
	case f.Type == "string":
		if v := Clean(line, maxValue); v != "" {
			return v, true
		}
	case f.Type == "number":
		if v, err := strconv.ParseFloat(line, 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			return v, true
		}
	case f.Type == "integer":
		if v, err := strconv.ParseInt(line, 10, 64); err == nil && v >= -1<<53 && v <= 1<<53 {
			return float64(v), true
		}
	case f.Type == "boolean":
		if v, ok := parseBool(line); ok {
			return v, true
		}
	}
	return nil, false
}

func parseBool(line string) (bool, bool) {
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, true
	case "n", "no":
		return false, true
	}
	return false, false
}

// line prints prompt and reads up to a newline, one byte at a time so input
// typed ahead stays unread. false on EOF, an error, an overlong line or the
// end of ctx.
func (t *term) line(prompt string) (string, bool) {
	fmt.Fprint(t.out, prompt)
	var buf []byte
	b := make([]byte, 1)
	for {
		if t.ctx.Err() != nil {
			return "", false
		}
		n, err := t.in.Read(b)
		if t.ctx.Err() != nil {
			return "", false
		}
		if n == 1 {
			if b[0] == '\n' {
				return strings.TrimSpace(string(buf)), true
			}
			if len(buf) == maxLine {
				return "", false
			}
			buf = append(buf, b[0])
		}
		if err != nil {
			return "", false
		}
	}
}
