package cli_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func resultText(t *testing.T, v result) string {
	t.Helper()
	var d struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil || len(d.Result.Content) != 1 {
		t.Fatalf("%v: %.200s", e, v.stdout)
	}
	return d.Result.Content[0].Text
}

func TestSchemaValidationBlackBox(t *testing.T) {
	r := newRig(t)
	for _, args := range [][]string{{`filter={"kind":"sk-SENTINEL"}`}, {`filter={"kind":"open","sk-SENTINEL":1}`}, {`limit:="sk-SENTINEL"`}} {
		v := r.check(r.run(append([]string{"call", "fixture.refs", "--json"}, args...)...), 2, "invalid_arguments")
		if strings.Contains(v.stdout, "SENTINEL") || !strings.Contains(v.stdout, "Arguments do not match the tool schema at /") {
			t.Fatal(v.stdout)
		}
	}
	if b, _ := os.ReadFile(r.root + "/started"); strings.Contains(string(b), "refs") {
		t.Fatalf("invalid call dispatched: %q", b)
	}
}

func TestLocalRefAndNullableCoercionBlackBox(t *testing.T) {
	r := newRig(t)
	got := structured(t, r.call("fixture.refs", "limit=5", "maybe=null", `filter={"kind":"open"}`))
	if !reflect.DeepEqual(got, map[string]any{"limit": float64(5), "maybe": nil, "filter": map[string]any{"kind": "open"}}) {
		t.Fatal(got)
	}
	if got = structured(t, r.call("fixture.refs", "maybe=3")); got["maybe"] != float64(3) {
		t.Fatal(got)
	}
	if got = structured(t, r.call("fixture.rootref", "limit=7")); got["limit"] != float64(7) {
		t.Fatal(got)
	}
	r.check(r.run("call", "fixture.rootref", "--json", "limit=x"), 2, "invalid_arguments")
}

func TestLargeResultBlackBox(t *testing.T) {
	r := newRig(t)
	// '<' grows sixfold in the IPC frame: 3 MiB becomes an 18 MiB frame, past the
	// 16 MiB the daemon reads. Larger sizes only cost time in race builds.
	const size = 3 << 20
	v := r.call("fixture.large", "bytes="+strconv.Itoa(size), "char=<")
	if text := resultText(t, v); len(text) != size || strings.Trim(text, "<") != "" {
		t.Fatal(len(text))
	}
}

func TestOversizeResultBlackBox(t *testing.T) {
	r := newRig(t)
	v := r.check(r.run("call", "fixture.large", "--json", "bytes="+strconv.Itoa(16<<20+1)), 6, "result_too_large")
	if v.envelope.Error.Details["dispatched"] != true || v.envelope.Error.Details["outcome"] != "unknown" || len(v.envelope.Data) > 0 && string(v.envelope.Data) != "null" {
		t.Fatal(v.stdout)
	}
	if structured(t, r.call("fixture.echo", "text=after"))["text"] != "after" {
		t.Fatal("next call failed")
	}
}

func TestSIGINTDuringLargeCallBlackBox(t *testing.T) {
	r := newRig(t)
	const size = 4 << 20
	a := r.start(binaryA, "", "call", "fixture.large", "--json", "bytes="+strconv.Itoa(size), "gate=true")
	r.waitFile(r.root+"/started", "large", a)
	pid := r.status().PID
	if e := a.cmd.Process.Signal(os.Interrupt); e != nil {
		t.Fatal(e)
	}
	v := r.check(r.finish(a), 130, "canceled")
	if v.envelope.Error.Details["dispatched"] != true || v.envelope.Error.Details["outcome"] != "unknown" {
		t.Fatal(v.stdout)
	}
	if r.status().PID != pid {
		t.Fatal("SIGINT killed the daemon")
	}
	r.write(r.root+"/release", "", 0o600)
	if text := resultText(t, r.call("fixture.large", "bytes="+strconv.Itoa(size), "gate=true")); len(text) != size {
		t.Fatal(len(text))
	}
}

func TestServerErrorBlackBox(t *testing.T) {
	r := newRig(t)
	v := r.check(r.run("call", "fixture.rpcfail", "--json"), 6, "server_error")
	d := v.envelope.Error.Details
	if d["rpcCode"] != float64(-32602) || d["dispatched"] != true || d["outcome"] != nil || !strings.Contains(v.stdout, "fixture rejected limit") {
		t.Fatal(v.stdout)
	}
	if structured(t, r.call("fixture.echo", "text=after"))["text"] != "after" {
		t.Fatal("next call failed")
	}
}
