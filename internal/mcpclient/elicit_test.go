package mcpclient_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func resultText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var v struct {
		Content []struct{ Text string } `json:"content"`
	}
	if e := json.Unmarshal(raw, &v); e != nil || len(v.Content) != 1 {
		t.Fatal(string(raw), e)
	}
	return v.Content[0].Text
}

func TestElicitationAlwaysDeclined(t *testing.T) {
	s := stdioSession(t, map[string]string{"MCP_TEST_LEGACY": "1"})
	raw := "Allow Computer Use\nto use \x1b[31mCalculator\u202e?\t" + strings.Repeat("x", 400)
	r, e := s.Call(ctx(t), "elicit", map[string]any{"message": raw}, nil, nil)
	if e != nil || r.IsError || resultText(t, r.JSON) != "action=decline" {
		t.Fatal(string(r.JSON), e)
	}
	if !strings.HasPrefix(r.Declined, "Allow Computer Use to use [31mCalculator? xxx") || utf8.RuneCountInString(r.Declined) > 303 || strings.IndexFunc(r.Declined, func(c rune) bool { return unicode.IsControl(c) || unicode.Is(unicode.Cf, c) }) >= 0 {
		t.Fatalf("declined message not sanitized: %q", r.Declined)
	}
	r, e = s.Call(ctx(t), "elicit", map[string]any{"message": "Allow?", "then": "error"}, nil, nil)
	if e != nil || !r.IsError || r.Declined != "Allow?" || resultText(t, r.JSON) != "action=decline" {
		t.Fatal(string(r.JSON), r.Declined, e)
	}
	r, e = s.Call(ctx(t), "elicit", map[string]any{"message": "Allow Calculator?", "then": "rpc"}, nil, nil)
	code(t, e, "elicitation_declined")
	if !r.Dispatched || !strings.Contains(e.Error(), "Allow Calculator?") {
		t.Fatal(e)
	}
	r, e = s.Call(ctx(t), "elicit", map[string]any{}, nil, nil)
	if e != nil || r.Declined != "(no message)" {
		t.Fatal(r.Declined, e)
	}
	if r = call(t, s, "counter", nil); r.Declined != "" {
		t.Fatal("decline leaked into the next call")
	}
}

func TestCallMeta(t *testing.T) {
	s := stdioSession(t, nil)
	turn := map[string]any{"session_id": "s1", "turn_id": "t1"}
	r, e := s.Call(ctx(t), "meta", nil, map[string]any{"x-codex-turn-metadata": turn}, nil)
	got, _ := structured(t, r)["meta"].(map[string]any)
	if e != nil || !reflect.DeepEqual(got["x-codex-turn-metadata"], turn) {
		t.Fatal(string(r.JSON), e)
	}
	got, _ = structured(t, call(t, s, "meta", nil))["meta"].(map[string]any)
	if _, ok := got["x-codex-turn-metadata"]; ok {
		t.Fatal("meta leaked into the next call")
	}
}
