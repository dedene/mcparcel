package cli_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const declined = "The server asked for approval and MCParcel declined it: Allow Computer Use to use Calculator?"

func TestElicitationDeclinedBlackBox(t *testing.T) {
	r := newRig(t)
	c := r.personal.Connections["fixture"]
	c.Transport.Stdio.Env["MCPARCEL_FIXTURE_LEGACY"] = literal("1")
	r.personal.Connections["fixture"] = c
	r.save()
	message := "message=Allow Computer Use to use\nCalculator?\x1b"
	v := r.call("fixture.elicit", message)
	var d struct {
		Result   json.RawMessage
		Warnings []struct{ Code, Message, NextAction string }
	}
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil || len(d.Warnings) != 1 || !strings.Contains(string(d.Result), "action=decline") {
		t.Fatal(v.stdout, e)
	}
	if w := d.Warnings[0]; w.Code != "elicitation_declined" || w.Message != declined || w.NextAction != "Approve this in the server's own app, then retry." {
		t.Fatal(v.stdout)
	}
	v = r.check(r.run("call", "fixture.elicit", message, "then=rpc", "--json"), 3, "elicitation_declined")
	var failure struct {
		Error struct{ Message, NextAction string }
	}
	if e := json.Unmarshal([]byte(v.stdout), &failure); e != nil || failure.Error.Message != declined || failure.Error.NextAction == "" {
		t.Fatal(v.stdout, e)
	}
	h := r.run("call", "fixture.elicit", message)
	if h.code != 0 || h.stdout != "action=decline\n" || !strings.Contains(h.stderr, declined+"\n") || !strings.Contains(h.stderr, "Approve this in the server's own app, then retry.") {
		t.Fatalf("%+v", h)
	}
	log, e := os.ReadFile(r.paths.LogFile)
	if e != nil || strings.Count(string(log), `{"event":"elicitation_declined"}`) != 3 || strings.Contains(string(log), "Calculator") {
		t.Fatal(string(log), e)
	}
}

func TestCallMetaBlackBox(t *testing.T) {
	r := newRig(t)
	for _, args := range [][]string{{"--meta", "{"}, {"--meta", "[]"}, {"--meta", "null"}, {"--meta", ""}, {"--meta", `{"progressToken":1}`}, {"--meta", `{"io.modelcontextprotocol/x":1}`}, {"--meta", `{"k":"` + strings.Repeat("a", 64*1024) + `"}`}, {"--meta", `{"k":"` + strings.Repeat("<", 40*1024) + `"}`}, {"--meta", "{}", "--meta", "{}"}} {
		r.check(r.run(append([]string{"call", "fixture.meta", "--json"}, args...)...), 2, "invalid_arguments")
	}
	if len(r.daemonPIDs()) != 0 {
		t.Fatal("invalid --meta contacted the daemon")
	}
	v := r.call("fixture.meta", "--meta", `{"x-codex-turn-metadata":{"session_id":"s1","turn_id":"t1","call_id":"c1"}}`)
	meta, _ := structured(t, v)["meta"].(map[string]any)
	turn, _ := meta["x-codex-turn-metadata"].(map[string]any)
	if turn["session_id"] != "s1" || turn["turn_id"] != "t1" || turn["call_id"] != "c1" {
		t.Fatal(v.stdout)
	}
}
