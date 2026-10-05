package config

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/dedene/mcparcel/internal/jsonutil"
)

func Literal(text string) Value { return Value{Literal: &text} }
func LiteralText(value Value) (string, error) {
	if value.Literal == nil || value.Input != nil || value.Secret != nil {
		return "", ErrConfigRequired
	}
	return *value.Literal, nil
}

func (v *Value) UnmarshalJSON(data []byte) error {
	decoded, err := jsonutil.Decode(data)
	if err != nil {
		return fieldError("value", "invalid JSON")
	}
	if err = checkShape(decoded, reflect.TypeFor[Value](), "value"); err != nil {
		return err
	}
	var next Value
	if literal, ok := decoded.(string); ok {
		next.Literal = &literal
	} else if obj := decoded.(map[string]any); obj["input"] != nil {
		var ref InputRef
		if err = json.Unmarshal(data, &ref); err != nil || !identifier.MatchString(ref.Input) {
			return fieldError("value.input", "invalid input reference")
		}
		next.Input = &ref
	} else {
		var ref SecretRef
		if err = json.Unmarshal(data, &ref); err != nil {
			return fieldError("value", "invalid secret binding")
		}
		if !validRef(ref.Secret) || strings.ContainsRune(ref.Prefix+ref.Suffix, 0) {
			return fieldError("value.secret", "invalid secret reference or affix")
		}
		next.Secret = &ref
	}
	*v = next
	return nil
}

func (v Value) MarshalJSON() ([]byte, error) {
	arms := 0
	if v.Literal != nil {
		arms++
	}
	if v.Input != nil {
		arms++
	}
	if v.Secret != nil {
		arms++
	}
	if arms != 1 {
		return nil, fieldError("value", "expected one value shape")
	}
	if v.Literal != nil {
		return json.Marshal(*v.Literal)
	}
	if v.Input != nil {
		return json.Marshal(v.Input)
	}
	return json.Marshal(v.Secret)
}

func (t *Transport) UnmarshalJSON(data []byte) error {
	decoded, err := jsonutil.Decode(data)
	if err != nil {
		return fieldError("transport", "invalid JSON")
	}
	if err = checkShape(decoded, reflect.TypeFor[Transport](), "transport"); err != nil {
		return err
	}
	obj := decoded.(map[string]any)
	typ := obj["type"]
	delete(obj, "type")
	body, err := json.Marshal(obj)
	if err != nil {
		return fieldError("transport", "invalid shape")
	}
	var next Transport
	if typ == "stdio" {
		next.Stdio = &Stdio{}
		err = json.Unmarshal(body, next.Stdio)
	} else {
		next.HTTP = &HTTP{}
		err = json.Unmarshal(body, next.HTTP)
	}
	if err != nil {
		return fieldError("transport", "invalid shape")
	}
	*t = next
	return nil
}

func (t Transport) MarshalJSON() ([]byte, error) {
	if (t.Stdio == nil) == (t.HTTP == nil) {
		return nil, fieldError("transport", "expected one transport shape")
	}
	var body []byte
	var err error
	typ := "stdio"
	if t.Stdio != nil {
		body, err = json.Marshal(t.Stdio)
	} else {
		typ = "http"
		body, err = json.Marshal(t.HTTP)
	}
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err = json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	obj["type"], _ = json.Marshal(typ)
	return json.Marshal(obj)
}
