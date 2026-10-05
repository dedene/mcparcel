package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestToolsJSONBlackBox(t *testing.T) {
	for _, mode := range []string{"stdio", "http"} {
		t.Run(mode, func(t *testing.T) {
			r := newRig(t)
			if mode == "http" {
				server := testutil.NewFixtureServer()
				h := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return server }, nil))
				r.closers = append(r.closers, h.Close)
				r.personal.Connections["fixture"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(h.URL), AllowInsecureHTTP: "loopback"}}}
				r.save()
			}
			v := r.check(r.run("tools", "fixture", "--json"), 0, "")
			var d struct {
				Connection      string
				Items           []struct{ Name string }
				SourceRevisions map[string]string
				CacheAgeSeconds *float64
			}
			if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
				t.Fatal(e)
			}
			if d.Connection != "local:fixture" || len(d.Items) != 11 || d.CacheAgeSeconds != nil || len(d.SourceRevisions) == 0 {
				t.Fatalf("tools: %s", v.stdout)
			}
			for i := 1; i < len(d.Items); i++ {
				if d.Items[i-1].Name >= d.Items[i].Name {
					t.Fatal("tools unsorted")
				}
			}
			snapshot, err := config.Load(r.paths)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(d.SourceRevisions, map[string]string{"personal": snapshot.Hash}) {
				t.Fatalf("source revision differs from loaded config: %s", v.stdout)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(v.envelope.Data, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["cacheAgeSeconds"]) != "null" {
				t.Fatal("cacheAgeSeconds must be explicit null")
			}
		})
	}
}

func TestCallJSONBlackBox(t *testing.T) {
	r := newRig(t)
	v := r.call("fixture.echo", "text=two words")
	if structured(t, v)["text"] != "two words" {
		t.Fatal(v.stdout)
	}
}

func TestArgumentFormsBlackBox(t *testing.T) {
	forms := [][]string{{"limit=5", "enabled=true", "queries=[{\"a\":1},{\"b\":2}]"}, {"limit:5", "enabled:true", "queries:[{\"a\":1},{\"b\":2}]"}, {"limit:=5", "enabled:=true", "queries:=[{\"a\":1},{\"b\":2}]"}, {"--args", `{"limit":5,"enabled":true,"queries":[{"a":1},{"b":2}]}`}, {"--args-file", "FILE"}, {"--args-file", "-"}}
	for i, args := range forms {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			r := newRig(t)
			input := `{"limit":5,"enabled":true,"queries":[{"a":1},{"b":2}]}`
			if len(args) == 2 && args[1] == "FILE" {
				r.write(r.root+"/args", input, 0o600)
				args = []string{"--args-file", r.root + "/args"}
			}
			v := r.check(r.finish(r.start(binaryA, input, append([]string{"call", "fixture.typed", "--json"}, args...)...)), 0, "")
			d := structured(t, v)
			if d["limit"] != float64(5) || d["enabled"] != true || !reflect.DeepEqual(d["queries"], []any{map[string]any{"a": float64(1)}, map[string]any{"b": float64(2)}}) {
				t.Fatal(v.stdout)
			}
		})
	}
}

func TestArgumentErrorsBlackBox(t *testing.T) {
	inputs := [][]string{{"text=a", "text:b"}, {"limit=bad", "enabled=true", "queries=[]"}, {"text=a", "--args", "{}"}, {"--args", ""}, {"--args-file", "MISSING"}, {"--timeout", "0"}, {"--timeout", "-1s"}, {"--timeout", "bad"}, {"--args", "{}", "--args", "{}"}, {"--args-file", ""}}
	for i, args := range inputs {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			r := newRig(t)
			target := "fixture.echo"
			if i == 1 {
				target = "fixture.typed"
			}
			v := r.check(r.run(append([]string{"call", target, "--json"}, args...)...), 2, "invalid_arguments")
			if i == 1 && !strings.Contains(v.stdout, "integer") {
				t.Fatal(v.stdout)
			}
			if b, _ := os.ReadFile(r.root + "/started"); len(b) > 0 {
				t.Fatalf("tool effect %q", b)
			}
			if r.countEvents("bootstrap") != 0 {
				t.Fatal("argument validation authenticated")
			}
		})
	}
}

func TestLargeIntegerStringAndDottedTool(t *testing.T) {
	r := newRig(t)
	if v := r.call("fixture.echo", "text=007"); structured(t, v)["text"] != "007" {
		t.Fatal(v.stdout)
	}
	v := r.call("fixture.typed", "limit=9007199254740993", "enabled=true", "queries=[]")
	if !strings.Contains(v.stdout, "limit-received=9007199254740993") {
		t.Fatal(v.stdout)
	}
	if v := r.call("fixture.echo.dotted", "text=dotted"); structured(t, v)["text"] != "dotted" || !strings.Contains(v.stdout, `"tool":"echo.dotted"`) {
		t.Fatal(v.stdout)
	}
}

func TestToolErrorEnvelopeBlackBox(t *testing.T) {
	r := newRig(t)
	v := r.check(r.run("call", "fixture.fail", "--json"), 5, "tool_error")
	if !strings.Contains(string(v.envelope.Data), "fixture failure") {
		t.Fatal(v.stdout)
	}
}

func TestOutcomeUnknownBlackBox(t *testing.T) {
	r := newRig(t)
	r.check(r.run("call", "fixture.write_drop", "--json"), 6, "outcome_unknown")
	r.waitFile(r.root+"/writes", "write")
	b, e := os.ReadFile(r.root + "/writes")
	if e != nil || string(b) != "write\n" {
		t.Fatalf("unexpected repeated write: %q %v", b, e)
	}
}

func TestJSONUsageAndHelp(t *testing.T) {
	r := newRig(t)
	r.check(r.run("call", "--json"), 2, "invalid_arguments")
	v := r.run("--help")
	if v.code != 0 || v.stderr != "" || !strings.Contains(v.stdout, "tools") || !strings.Contains(v.stdout, "runtime") || strings.Contains(v.stdout, "daemon") || strings.Contains(v.stdout, "spike") {
		t.Fatalf("help: %+v", v)
	}
	v = r.run("version")
	if v.code != 0 || !strings.Contains(v.stdout, "stage2-test-a") {
		t.Fatal(v.stdout)
	}
}

func TestPersonalReloadBlackBox(t *testing.T) {
	r := newRig(t)
	r.stdio("fixture", "")
	c := r.personal.Connections["fixture"]
	c.Transport.Stdio.Env["RELOAD"] = literal("before")
	r.personal.Connections["fixture"] = c
	r.save()
	if v := r.call("fixture.env", "name=RELOAD"); structured(t, v)["value"] != "before" {
		t.Fatal(v.stdout)
	}
	pid := r.status().PID
	c.Transport.Stdio.Env["RELOAD"] = literal("after")
	r.personal.Connections["fixture"] = c
	r.save()
	if v := r.call("fixture.env", "name=RELOAD"); structured(t, v)["value"] != "after" {
		t.Fatal(v.stdout)
	}
	if r.status().PID != pid {
		t.Fatal("reload restarted daemon")
	}
}
