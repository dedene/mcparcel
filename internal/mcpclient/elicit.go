package mcpclient

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/elicit"
)

// Prompter asks the user behind the in-flight call to answer an elicitation;
// Forms is false when only the approval case can be shown.
type Prompter struct {
	Ask   func(context.Context, elicit.Prompt) elicit.Answer
	Forms bool
}

type prompterKey struct{}

func WithPrompter(ctx context.Context, p *Prompter) context.Context {
	return context.WithValue(ctx, prompterKey{}, p)
}

func PrompterFrom(ctx context.Context) *Prompter {
	p, _ := ctx.Value(prompterKey{}).(*Prompter)
	return p
}

// elicitation answers elicitation/create before the SDK validates it. Only
// the user of the in-flight call can accept; without one it declines.
func (s *session) elicitation(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "elicitation/create" {
			return next(ctx, method, req)
		}
		var params *mcp.ElicitParams
		if r, ok := req.(*mcp.ElicitRequest); ok {
			params = r.Params
		}
		s.mu.Lock()
		prompter := s.prompter
		s.mu.Unlock()
		if prompter == nil {
			return s.notAccepted(params, "unavailable", "decline"), nil
		}
		prompt, ok := promptFrom(params, prompter.Forms)
		if !ok {
			return s.notAccepted(params, "unsupported", "decline"), nil
		}
		a := prompter.Ask(ctx, prompt)
		if prompt.Check(a) != nil {
			a = elicit.Answer{Action: "cancel"}
		}
		switch a.Action {
		case "decline":
			return s.notAccepted(params, "declined", a.Action), nil
		case "cancel":
			return s.notAccepted(params, "canceled", a.Action), nil
		}
		res := &mcp.ElicitResult{Action: "accept", Content: a.Content}
		if a.Persist != "" {
			res.Meta = mcp.Meta{"persist": a.Persist}
		}
		return res, nil
	}
}

// notAccepted records the call's first non-accepted elicitation.
func (s *session) notAccepted(params *mcp.ElicitParams, reason, action string) *mcp.ElicitResult {
	text := ""
	if params != nil {
		text = params.Message
	}
	s.mu.Lock()
	if s.declined == "" {
		s.declined, s.reason = noMessage(elicit.Clean(text, 300)), reason
	}
	s.mu.Unlock()
	return &mcp.ElicitResult{Action: action}
}

func noMessage(text string) string {
	if text == "" {
		return "(no message)"
	}
	return text
}

// promptFrom builds the cleaned prompt for a form-mode request with no
// schema, an empty one or flat primitive fields; false when it cannot be shown.
func promptFrom(params *mcp.ElicitParams, forms bool) (elicit.Prompt, bool) {
	if params == nil || params.Mode != "" && params.Mode != "form" || params.URL != "" {
		return elicit.Prompt{}, false
	}
	p := elicit.Prompt{Message: noMessage(elicit.Clean(params.Message, 500))}
	if v, ok := params.Meta["subtitle"].(string); ok {
		p.Subtitle = elicit.Clean(v, 200)
	}
	if v, ok := params.Meta["riskLevel"].(string); ok {
		p.RiskLevel = elicit.Clean(v, 32)
	}
	p.Details = details(params.Meta["tool_params_display"])
	fields, ok := schemaFields(params.RequestedSchema)
	if !ok || len(fields) > 0 && !forms {
		return elicit.Prompt{}, false
	}
	p.Fields = fields
	if persist, ok := params.Meta["persist"].([]any); ok && len(fields) == 0 {
		// "always" is never offered: the server does not store it.
		if slices.Contains(persist, any("session")) {
			p.Persist = []string{"session"}
		}
	}
	return p, p.Valid() == nil
}

// details renders tool_params_display: "Name: value" entries joined by "; "
// when each has a string display_name (else name) and a scalar value; a
// string as is; anything else as compact JSON.
func details(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return elicit.Clean(v, 500)
	case []any:
		if s, ok := entries(v); ok {
			return elicit.Clean(s, 500)
		}
	}
	b, e := json.Marshal(v)
	if e != nil {
		return ""
	}
	return elicit.Clean(string(b), 500)
}

func entries(list []any) (string, bool) {
	parts := make([]string, 0, len(list))
	for _, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			return "", false
		}
		label, _ := m["display_name"].(string)
		if label = elicit.Clean(label, 500); label == "" {
			name, _ := m["name"].(string)
			label = elicit.Clean(name, 500)
		}
		if label == "" {
			return "", false
		}
		var value string
		switch v := m["value"].(type) {
		case string:
			value = v
		case float64:
			value = strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			value = strconv.FormatBool(v)
		default:
			return "", false
		}
		parts = append(parts, label+": "+elicit.Clean(value, 500))
	}
	return strings.Join(parts, "; "), true
}

// schemaFields reads a flat object schema of primitive properties, sorted by
// name; nil for no schema or no properties.
func schemaFields(schema any) ([]elicit.Field, bool) {
	if schema == nil {
		return nil, true
	}
	top, ok := schema.(map[string]any)
	if !ok {
		return nil, false
	}
	for k, v := range top {
		switch k {
		case "type":
			if v != "object" {
				return nil, false
			}
		case "properties", "required", "$schema", "additionalProperties":
		default:
			return nil, false
		}
	}
	props, ok := top["properties"].(map[string]any)
	if !ok && top["properties"] != nil {
		return nil, false
	}
	var fields []elicit.Field
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		f := elicit.Field{Name: name}
		for k, v := range prop {
			s, isString := v.(string)
			switch k {
			case "type":
				f.Type = s
			case "title":
				f.Title = elicit.Clean(s, 200)
			case "description":
				f.Description = elicit.Clean(s, 300)
			case "enum":
				values, _ := v.([]any)
				f.Enum = []string{}
				for _, e := range values {
					e, ok := e.(string)
					if !ok || e != elicit.Clean(e, 100) {
						return nil, false
					}
					f.Enum = append(f.Enum, e)
				}
				isString = true
			case "default":
				isString = true
			default:
				return nil, false
			}
			if !isString {
				return nil, false
			}
		}
		fields = append(fields, f)
	}
	required, ok := top["required"].([]any)
	if !ok && top["required"] != nil {
		return nil, false
	}
	for _, r := range required {
		i := slices.IndexFunc(fields, func(f elicit.Field) bool { return f.Name == r })
		if i < 0 {
			return nil, false
		}
		fields[i].Required = true
	}
	slices.SortFunc(fields, func(a, b elicit.Field) int { return strings.Compare(a.Name, b.Name) })
	return fields, true
}
