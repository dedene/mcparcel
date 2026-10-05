package mcpclient

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/elicit"
)

func approval(meta mcp.Meta) *mcp.ElicitParams {
	return &mcp.ElicitParams{Message: "Allow?", Meta: meta, RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}}}
}

func form(props map[string]any, required ...any) *mcp.ElicitParams {
	return &mcp.ElicitParams{Mode: "form", Message: "Fill", RequestedSchema: map[string]any{"$schema": "x", "type": "object", "properties": props, "required": required, "additionalProperties": false}}
}

func TestPromptFrom(t *testing.T) {
	both := []any{"session", "always"}
	for name, c := range map[string]struct {
		params *mcp.ElicitParams
		forms  bool
		want   elicit.Prompt
	}{
		"persist both":    {approval(mcp.Meta{"persist": []any{"always", "session"}, "riskLevel": "high", "subtitle": "Sub\x1b[31m", "tool_params_display": "click(1,2)"}), false, elicit.Prompt{Message: "Allow?", Subtitle: "Sub", RiskLevel: "high", Details: "click(1,2)", Persist: []string{"session"}}},
		"persist session": {approval(mcp.Meta{"persist": []any{"session"}}), false, elicit.Prompt{Message: "Allow?", Persist: []string{"session"}}},
		"persist always":  {approval(mcp.Meta{"persist": []any{"always"}}), false, elicit.Prompt{Message: "Allow?"}},
		"persist bogus":   {approval(mcp.Meta{"persist": []any{"bogus", "always", "session", "session", 3}}), false, elicit.Prompt{Message: "Allow?", Persist: []string{"session"}}},
		"persist string":  {approval(mcp.Meta{"persist": "session", "riskLevel": 3}), false, elicit.Prompt{Message: "Allow?"}},
		"display json":    {approval(mcp.Meta{"tool_params_display": map[string]any{"b": 1, "a": []any{"x"}}}), false, elicit.Prompt{Message: "Allow?", Details: `{"a":["x"],"b":1}`}},
		"no schema":       {&mcp.ElicitParams{Message: "  dirty\n\u202emessage\x07 "}, false, elicit.Prompt{Message: "dirty message"}},
		"empty message":   {&mcp.ElicitParams{RequestedSchema: map[string]any{"type": "object"}}, false, elicit.Prompt{Message: "(no message)"}},
		"form": {form(map[string]any{
			"zeta":  map[string]any{"type": "string", "enum": []any{"a", "b"}, "default": "a"},
			"alpha": map[string]any{"type": "integer", "title": "Alpha", "description": "Count"},
			"mid":   map[string]any{"type": "boolean"},
		}, "zeta", "mid"), true, elicit.Prompt{Message: "Fill", Fields: []elicit.Field{
			{Name: "alpha", Title: "Alpha", Description: "Count", Type: "integer"},
			{Name: "mid", Type: "boolean", Required: true},
			{Name: "zeta", Type: "string", Enum: []string{"a", "b"}, Required: true},
		}}},
		"form drops persist": {&mcp.ElicitParams{Message: "Fill", Meta: mcp.Meta{"persist": both}, RequestedSchema: map[string]any{"properties": map[string]any{"n": map[string]any{"type": "number"}}}}, true, elicit.Prompt{Message: "Fill", Fields: []elicit.Field{{Name: "n", Type: "number"}}}},
	} {
		got, ok := promptFrom(c.params, c.forms)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v %#v", name, ok, got)
		}
	}
	many := map[string]any{}
	for _, n := range "abcdefghijk" {
		many[string(n)] = map[string]any{"type": "string"}
	}
	for name, c := range map[string]struct {
		params *mcp.ElicitParams
		forms  bool
	}{
		"nested":       {form(map[string]any{"x": map[string]any{"type": "object"}}), true},
		"array":        {form(map[string]any{"x": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}), true},
		"format":       {form(map[string]any{"x": map[string]any{"type": "string", "format": "email"}}), true},
		"oneOf":        {form(map[string]any{"x": map[string]any{"oneOf": []any{}}}), true},
		"number enum":  {form(map[string]any{"x": map[string]any{"type": "string", "enum": []any{1}}}), true},
		"enum on bool": {form(map[string]any{"x": map[string]any{"type": "boolean", "enum": []any{"a"}}}), true},
		"dirty enum":   {form(map[string]any{"x": map[string]any{"type": "string", "enum": []any{"a\x1b[1m"}}}), true},
		"bad name":     {form(map[string]any{"a b": map[string]any{"type": "string"}}), true},
		"11 fields":    {form(many), true},
		"unknown req":  {form(map[string]any{"x": map[string]any{"type": "string"}}, "y"), true},
		"top key":      {&mcp.ElicitParams{Message: "m", RequestedSchema: map[string]any{"type": "object", "oneOf": []any{}}}, true},
		"top type":     {&mcp.ElicitParams{Message: "m", RequestedSchema: map[string]any{"type": "string"}}, true},
		"url mode":     {&mcp.ElicitParams{Mode: "url", Message: "m", URL: "https://example.invalid"}, true},
		"url only":     {&mcp.ElicitParams{Message: "m", URL: "https://example.invalid"}, true},
		"no forms":     {form(map[string]any{"allow": map[string]any{"type": "boolean"}}), false},
		"nil":          {nil, true},
	} {
		if got, ok := promptFrom(c.params, c.forms); ok {
			t.Errorf("%s: %#v", name, got)
		}
	}
}

func TestPromptDetails(t *testing.T) {
	entry := func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	long := []any{}
	for range 100 {
		long = append(long, entry("display_name", "Name", "value", "0123456789"))
	}
	for name, c := range map[string]struct {
		display any
		want    string
	}{
		"real":          {[]any{entry("name", "app", "display_name", "App", "value", "TextEdit")}, "App: TextEdit"},
		"several":       {[]any{entry("display_name", "App", "value", "TextEdit"), entry("name", "x", "value", 10.0), entry("display_name", "On", "value", true), entry("name", "r", "value", 1.5), entry("name", "n", "value", 1e6)}, "App: TextEdit; x: 10; On: true; r: 1.5; n: 1000000"},
		"name fallback": {[]any{entry("display_name", 3, "name", "app", "value", "y"), entry("display_name", "\x1b[1m", "name", "b", "value", "z")}, "app: y; b: z"},
		"hostile":       {[]any{entry("display_name", "A\x1b[31mpp\n\u202e", "value", "Text\x1b]0;t\x07Edit\r\nnext\u200b line\x00")}, "App: TextEdit next line"},
		"empty":         {[]any{}, ""},
		"capped":        {long, strings.Repeat("Name: 0123456789; ", 27) + "Name: 01234..."},
		"string":        {"click (1,2)\x1b[1m", "click (1,2)"},
		"mixed":         {[]any{entry("display_name", "App", "value", "x"), "str"}, `[{"display_name":"App","value":"x"},"str"]`},
		"no label":      {[]any{entry("value", "x")}, `[{"value":"x"}]`},
		"blank label":   {[]any{entry("display_name", "\u200b", "value", "x")}, `[{"display_name":"","value":"x"}]`},
		"nested value":  {[]any{entry("name", "a", "value", map[string]any{"k": 1})}, `[{"name":"a","value":{"k":1}}]`},
		"null value":    {[]any{entry("name", "a", "value", nil)}, `[{"name":"a","value":null}]`},
		"no value":      {[]any{entry("name", "a")}, `[{"name":"a"}]`},
		"hostile json":  {[]any{entry("name", 1, "value", "a\nb\u202ec")}, `[{"name":1,"value":"a\nbc"}]`},
	} {
		got, ok := promptFrom(approval(mcp.Meta{"tool_params_display": c.display}), false)
		if !ok || got.Details != c.want {
			t.Errorf("%s: %v %q, want %q", name, ok, got.Details, c.want)
		}
	}
}

func TestElicitationMiddleware(t *testing.T) {
	s := &session{}
	h := s.elicitation(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		t.Fatal("next called")
		return nil, nil
	})
	req := &mcp.ElicitRequest{Params: approval(mcp.Meta{"persist": []any{"session"}})}
	res, e := h(context.Background(), "elicitation/create", req)
	if r, _ := res.(*mcp.ElicitResult); e != nil || r == nil || r.Action != "decline" || s.declined != "Allow?" || s.reason != "unavailable" {
		t.Fatal(res, e, s.declined, s.reason)
	}
	asked := 0
	s = &session{prompter: &Prompter{Ask: func(context.Context, elicit.Prompt) elicit.Answer {
		asked++
		return elicit.Answer{Action: "accept", Persist: "always"}
	}}}
	h = s.elicitation(nil)
	res, e = h(context.Background(), "elicitation/create", req)
	if r, _ := res.(*mcp.ElicitResult); e != nil || r == nil || r.Action != "cancel" || r.Meta != nil || asked != 1 || s.reason != "canceled" {
		t.Fatal(res, e, asked, s.reason)
	}
	s.declined, s.reason = "", ""
	res, _ = h(context.Background(), "elicitation/create", &mcp.ElicitRequest{Params: form(map[string]any{"x": map[string]any{"type": "boolean"}})})
	if r, _ := res.(*mcp.ElicitResult); r == nil || r.Action != "decline" || asked != 1 || s.reason != "unsupported" || !strings.HasPrefix(s.declined, "Fill") {
		t.Fatal(res, asked, s.reason)
	}
}
