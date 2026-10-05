package args

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/dedene/mcparcel/internal/jsonutil"
)

const MaxPayloadBytes = 2 * 1024 * 1024

var ErrInvalidArgs = errors.New("invalid arguments")

type Value struct {
	Text string          `json:"text,omitempty"`
	JSON json.RawMessage `json:"json,omitempty"`
}
type Raw struct {
	Values map[string]Value `json:"values"`
}

func Parse(assignments []string, inlineJSON string, payload io.Reader) (Raw, error) {
	out := Raw{Values: make(map[string]Value)}
	sources := 0
	if len(assignments) > 0 {
		sources++
	}
	if inlineJSON != "" {
		sources++
	}
	if payload != nil {
		sources++
	}
	if sources > 1 {
		return Raw{}, fmt.Errorf("%w: choose one argument source", ErrInvalidArgs)
	}
	for _, assignment := range assignments {
		i := strings.IndexAny(assignment, "=:")
		if i <= 0 || strings.IndexFunc(assignment[:i], func(r rune) bool { return unicode.IsSpace(r) || r == 0 }) >= 0 {
			return Raw{}, fmt.Errorf("%w: expected key=value, key:value or key:=json", ErrInvalidArgs)
		}
		key := assignment[:i]
		if _, exists := out.Values[key]; exists {
			return Raw{}, fmt.Errorf("%w: duplicate key %q", ErrInvalidArgs, key)
		}
		forced := assignment[i] == ':' && len(assignment) > i+1 && assignment[i+1] == '='
		start := i + 1
		if forced {
			start++
		}
		value := Value{Text: assignment[start:]}
		if forced {
			v, err := jsonutil.Decode([]byte(value.Text))
			if err != nil {
				return Raw{}, fmt.Errorf("%w: argument %q requires valid JSON", ErrInvalidArgs, key)
			}
			value.JSON, _ = json.Marshal(v)
			value.Text = ""
		}
		out.Values[key] = value
	}
	if payload == nil && inlineJSON == "" {
		return out, nil
	}
	data := []byte(inlineJSON)
	if payload != nil {
		var err error
		data, err = io.ReadAll(io.LimitReader(payload, MaxPayloadBytes+1))
		if err != nil {
			return Raw{}, fmt.Errorf("%w: could not read payload", ErrInvalidArgs)
		}
	}
	if len(data) > MaxPayloadBytes {
		return Raw{}, fmt.Errorf("%w: payload exceeds %d bytes", ErrInvalidArgs, MaxPayloadBytes)
	}
	v, err := jsonutil.Decode(data)
	obj, ok := v.(map[string]any)
	if err != nil || !ok {
		return Raw{}, fmt.Errorf("%w: payload must be one JSON object", ErrInvalidArgs)
	}
	for key, val := range obj {
		encoded, _ := json.Marshal(val)
		out.Values[key] = Value{JSON: encoded}
	}
	return out, nil
}
