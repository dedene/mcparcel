package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

const (
	dirty       = "message=Allow \x1b[31mComputer\x1b[0m Use \x1b]8;;https://evil.example\x07link\x1b]8;;\x07 \u202eto use Calculator?\x1b"
	clean       = "Allow Computer Use link to use Calculator?"
	allOptions  = "  1) Decline (default)  2) Allow once  3) Allow for this session  4) Always allow\n"
	youDeclined = "You declined the server's request: " + clean
	canceled    = "The server's request was canceled without an answer: " + clean
	unsupported = "The server asked for input MCParcel cannot show, so MCParcel declined it: " + clean
)

// promptRig stands in for the user: StateDir/fixture-terminal holds the typed
// lines and StateDir/fixture-dialog-answer the dialog's button.
func promptRig(t *testing.T) *rig {
	r := newRig(t)
	c := r.personal.Connections["fixture"]
	c.Transport.Stdio.Env["MCPARCEL_FIXTURE_LEGACY"] = literal("1")
	r.personal.Connections["fixture"] = c
	r.save()
	return r
}

func (r *rig) typed(name, body string) {
	r.t.Helper()
	if body == "-" {
		if e := os.Remove(r.paths.StateDir + "/" + name); e != nil && !errors.Is(e, os.ErrNotExist) {
			r.t.Fatal(e)
		}
		return
	}
	r.write(r.paths.StateDir+"/"+name, body, 0o600)
}

func (r *rig) approvalDialog(on bool) {
	r.t.Helper()
	b, e := os.ReadFile(r.paths.ConfigFile)
	var local config.Local
	if e == nil {
		e = json.Unmarshal(b, &local)
	}
	if e != nil {
		r.t.Fatal(e)
	}
	local.Runtime = &config.RuntimeDefaults{ApprovalDialog: on}
	if b, e = json.Marshal(local); e != nil {
		r.t.Fatal(e)
	}
	r.write(r.paths.ConfigFile, string(b), 0o600)
}

func (r *rig) human(args ...string) result {
	r.t.Helper()
	v := r.run(append([]string{"call", "fixture.elicit", dirty}, args...)...)
	if v.code != 0 {
		r.t.Fatalf("%+v", v)
	}
	return v
}

// warning returns the call's single warning message, or "".
func (r *rig) warning(v result) (string, string) {
	r.t.Helper()
	var d struct {
		Result   struct{ Content []struct{ Text string } }
		Warnings []struct{ Code, Message string }
	}
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil || len(d.Result.Content) != 1 || len(d.Warnings) > 1 {
		r.t.Fatal(v.stdout, e)
	}
	if len(d.Warnings) == 0 {
		return d.Result.Content[0].Text, ""
	}
	if d.Warnings[0].Code != "elicitation_declined" {
		r.t.Fatal(v.stdout)
	}
	return d.Result.Content[0].Text, d.Warnings[0].Message
}

func TestTerminalPromptBlackBox(t *testing.T) {
	r := promptRig(t)
	r.typed("fixture-terminal", "4\n")
	v := r.human("schema=none", "persist=session,always", "risk=high", "display=click (10,20)")
	if v.stdout != "action=accept persist=always\n" {
		t.Fatalf("%+v", v)
	}
	want := "fixture asks: " + clean + "\n  Risk: high\n  Details: click (10,20)\n" + allOptions + "Choice [1]: "
	if v.stderr != want {
		t.Fatalf("stderr %q", v.stderr)
	}
	log, e := os.ReadFile(r.paths.LogFile)
	if e != nil || !strings.Contains(string(log), `{"event":"elicitation_forwarded"}`) || !strings.Contains(string(log), `{"event":"elicitation_accepted"}`) || strings.Contains(string(log), "Calculator") || strings.Contains(string(log), "click") {
		t.Fatal(string(log), e)
	}

	r.typed("fixture-terminal", "\n")
	v = r.human("schema=none", "persist=session,always")
	if v.stdout != "action=decline\n" || !strings.Contains(v.stderr, allOptions) || !strings.Contains(v.stderr, youDeclined+"\n") {
		t.Fatalf("%+v", v)
	}
	r.typed("fixture-terminal", "")
	v = r.human("schema=none", "persist=session,always")
	if v.stdout != "action=cancel\n" || !strings.Contains(v.stderr, canceled+"\n") {
		t.Fatalf("%+v", v)
	}
	r.typed("fixture-terminal", "2\n")
	v = r.human("schema=none", "persist=session")
	if v.stdout != "action=accept\n" || !strings.Contains(v.stderr, "  1) Decline (default)  2) Allow once  3) Allow for this session\n") || strings.Contains(v.stderr, "Always allow") || strings.Contains(v.stderr, "declined") {
		t.Fatalf("%+v", v)
	}
	r.typed("fixture-terminal", "2\ny\ny\n")
	v = r.human()
	if v.stdout != "action=accept content={\"allow\":true}\n" || !strings.Contains(v.stderr, "  1) Decline (default)  2) Answer\n") {
		t.Fatalf("%+v", v)
	}
}

func TestPromptUnavailableBlackBox(t *testing.T) {
	r := promptRig(t)
	r.typed("fixture-terminal", "4\n")
	text, warning := r.warning(r.call("fixture.elicit", dirty, "schema=none", "persist=always"))
	if text != "action=decline" || !strings.HasPrefix(warning, "The server asked for approval and MCParcel declined it because no prompt was possible: ") {
		t.Fatal(text, warning)
	}
	if v := r.human("schema=none", "persist=always"); v.stdout != "action=accept persist=always\n" {
		t.Fatalf("fixture-terminal consumed by --json: %+v", v)
	}
	r.approvalDialog(true)
	r.typed("fixture-dialog-answer", "Always allow\n")
	v := r.human("schema=none", "persist=always", "--no-input")
	if v.stdout != "action=decline\n" || !strings.Contains(v.stderr, "no prompt was possible") || strings.Contains(v.stderr, "asks:") {
		t.Fatalf("%+v", v)
	}
	if _, e := os.Stat(r.paths.StateDir + "/fixture-dialog-args"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("dialog shown under --no-input", e)
	}
}

func TestDialogPromptBlackBox(t *testing.T) {
	r := promptRig(t)
	r.approvalDialog(true)
	args := func() []string {
		var argv []string
		b, e := os.ReadFile(r.paths.StateDir + "/fixture-dialog-args")
		if e == nil {
			e = json.Unmarshal(b, &argv)
		}
		if e != nil {
			t.Fatal(e)
		}
		r.typed("fixture-dialog-args", "-")
		return argv
	}
	r.typed("fixture-dialog-answer", "Always allow\n")
	text, warning := r.warning(r.call("fixture.elicit", dirty, "schema=none", "persist=session,always", "risk=high"))
	if text != "action=accept persist=always" || warning != "" {
		t.Fatal(text, warning)
	}
	if argv := args(); !slices.Equal(argv, []string{"MCParcel: fixture", clean + "\nRisk: high", "300", "Decline", "Allow once", "Always allow"}) {
		t.Fatalf("%q", argv)
	}
	r.typed("fixture-dialog-answer", "Allow for this session\n")
	text, _ = r.warning(r.call("fixture.elicit", dirty, "schema=none", "persist=session"))
	if argv := args(); text != "action=accept persist=session" || !slices.Equal(argv[3:], []string{"Decline", "Allow once", "Allow for this session"}) {
		t.Fatalf("%s %q", text, argv)
	}
	r.typed("fixture-dialog-answer", "-")
	text, warning = r.warning(r.call("fixture.elicit", dirty, "schema=none", "persist=always"))
	if args(); text != "action=cancel" || warning != canceled {
		t.Fatal(text, warning)
	}
	// The dialog shows no forms, even when the form also offers persistence.
	r.typed("fixture-dialog-answer", "Always allow\n")
	for _, extra := range [][]string{nil, {"persist=session,always"}} {
		text, warning = r.warning(r.call("fixture.elicit", append([]string{dirty}, extra...)...))
		if text != "action=decline" || warning != unsupported {
			t.Fatal(text, warning)
		}
		if _, e := os.Stat(r.paths.StateDir + "/fixture-dialog-args"); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("dialog shown for a form", e)
		}
	}
}
