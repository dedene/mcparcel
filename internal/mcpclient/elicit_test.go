package mcpclient_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/dedene/mcparcel/internal/elicit"
	"github.com/dedene/mcparcel/internal/mcpclient"
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
	if !strings.HasPrefix(r.Declined, "Allow Computer Use to use Calculator? xxx") || r.DeclineReason != "unavailable" || utf8.RuneCountInString(r.Declined) > 300 || strings.IndexFunc(r.Declined, func(c rune) bool { return unicode.IsControl(c) || unicode.Is(unicode.Cf, c) }) >= 0 {
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

func TestElicitationForwarded(t *testing.T) {
	s := stdioSession(t, map[string]string{"MCP_TEST_LEGACY": "1"})
	var seen []elicit.Prompt
	answer := elicit.Answer{Action: "accept", Persist: "always"}
	asker := mcpclient.WithPrompter(ctx(t), &mcpclient.Prompter{Forms: true, Ask: func(_ context.Context, p elicit.Prompt) elicit.Answer {
		seen = append(seen, p)
		return answer
	}})
	approval := map[string]any{"message": "Allow Calculator?", "schema": "none", "persist": "session,always", "risk": "high", "display": "click"}
	r, e := s.Call(asker, "elicit", approval, nil, nil)
	if e != nil || resultText(t, r.JSON) != "action=accept persist=always" || r.Declined != "" || r.DeclineReason != "" {
		t.Fatal(string(r.JSON), r.Declined, e)
	}
	want := elicit.Prompt{Message: "Allow Calculator?", RiskLevel: "high", Details: "click", Persist: []string{"session", "always"}}
	if len(seen) != 1 || !reflect.DeepEqual(seen[0], want) {
		t.Fatalf("%#v", seen)
	}
	answer = elicit.Answer{Action: "accept", Content: map[string]any{"allow": true}}
	if r, e = s.Call(asker, "elicit", map[string]any{"message": "Allow?"}, nil, nil); e != nil || resultText(t, r.JSON) != `action=accept content={"allow":true}` || r.Declined != "" {
		t.Fatal(string(r.JSON), e)
	}
	for action, reason := range map[string]string{"decline": "declined", "cancel": "canceled"} {
		answer = elicit.Answer{Action: action}
		r, e = s.Call(asker, "elicit", approval, nil, nil)
		if e != nil || resultText(t, r.JSON) != "action="+action || r.Declined != "Allow Calculator?" || r.DeclineReason != reason {
			t.Fatal(string(r.JSON), r.Declined, r.DeclineReason, e)
		}
	}
	answer = elicit.Answer{Action: "accept", Persist: "always"}
	if r, e = s.Call(asker, "elicit", map[string]any{"message": "Allow?", "persist": "session", "schema": "none"}, nil, nil); e != nil || resultText(t, r.JSON) != "action=cancel" || r.DeclineReason != "canceled" {
		t.Fatal(string(r.JSON), e)
	}
	before := len(seen)
	r, e = s.Call(ctx(t), "elicit", approval, nil, nil)
	if e != nil || resultText(t, r.JSON) != "action=decline" || r.DeclineReason != "unavailable" || len(seen) != before {
		t.Fatal(string(r.JSON), r.DeclineReason, e)
	}
	r, e = s.Call(asker, "elicit", map[string]any{"message": "Allow?", "schema": "nested"}, nil, nil)
	if e != nil || resultText(t, r.JSON) != "action=decline" || r.DeclineReason != "unsupported" || len(seen) != before {
		t.Fatal(string(r.JSON), r.DeclineReason, e)
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
