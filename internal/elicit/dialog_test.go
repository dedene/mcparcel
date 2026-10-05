package elicit_test

import (
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/dedene/mcparcel/internal/elicit"
)

func TestDialogArgs(t *testing.T) {
	for _, tc := range []struct {
		persist []string
		buttons []string
	}{
		{nil, []string{"Decline", "Allow once"}},
		{[]string{"session"}, []string{"Decline", "Allow once", "Allow for this session"}},
	} {
		p := approval()
		p.Persist = tc.persist
		argv := elicit.DialogArgs("codex\x1b[1m-cu\u202e", p)
		if len(argv) < 4 || !reflect.DeepEqual(argv[3:], tc.buttons) {
			t.Fatalf("persist %v: argv %q", tc.persist, argv)
		}
		if argv[0] != "MCParcel: codex-cu" || argv[2] != "300" {
			t.Fatalf("argv %q", argv)
		}
		want := "Allow Computer Use to use \"Calculator\"?\nNote: Computer use\nRisk: medium\nDetails: {\"app\":\"Calculator\"}"
		if argv[1] != want {
			t.Fatalf("text = %q", argv[1])
		}
		for _, arg := range argv {
			for _, r := range arg {
				if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
					t.Fatalf("control character %U in %q", r, arg)
				}
			}
		}
	}
	if argv := elicit.DialogArgs("c", elicit.Prompt{Message: "m"}); argv[1] != "m" {
		t.Fatalf("plain text = %q", argv[1])
	}
	if argv := elicit.DialogArgs("", elicit.Prompt{Message: "m"}); !strings.HasPrefix(argv[0], "MCParcel") {
		t.Fatalf("title = %q", argv[0])
	}
	if argv := elicit.DialogArgs("c", form()); argv != nil {
		t.Fatalf("form argv = %q", argv)
	}
	for _, persist := range [][]string{{"always"}, {"session", "always"}} {
		if argv := elicit.DialogArgs("c", elicit.Prompt{Message: "m", Persist: persist}); argv != nil {
			t.Fatalf("persist %v argv = %q", persist, argv)
		}
	}
	if argv := elicit.DialogArgs("c", elicit.Prompt{Message: "a\x07"}); argv != nil {
		t.Fatalf("invalid prompt argv = %q", argv)
	}
}

func TestDialogAnswer(t *testing.T) {
	session := approval()
	none := approval()
	none.Persist = nil
	for _, tc := range []struct {
		p      elicit.Prompt
		button string
		want   elicit.Answer
	}{
		{session, "Decline", elicit.Answer{Action: "decline"}},
		{session, "Allow once", elicit.Answer{Action: "accept"}},
		{session, "Allow for this session", elicit.Answer{Action: "accept", Persist: "session"}},
		{session, "Always allow", elicit.Answer{Action: "cancel"}},
		{none, "Allow once", elicit.Answer{Action: "accept"}},
		{none, "Allow for this session", elicit.Answer{Action: "cancel"}},
		{session, "", elicit.Answer{Action: "cancel"}},
		{session, "allow once", elicit.Answer{Action: "cancel"}},
		{session, "Allow once\n", elicit.Answer{Action: "cancel"}},
		{session, "OK", elicit.Answer{Action: "cancel"}},
		{form(), "Allow once", elicit.Answer{Action: "cancel"}},
	} {
		if got := elicit.DialogAnswer(tc.p, tc.button); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("persist %v, %q: got %+v, want %+v", tc.p.Persist, tc.button, got, tc.want)
		}
	}
}
