package args

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/dedene/mcparcel/internal/jsonutil"
)

var ErrInvalidSchema = errors.New("invalid tool schema")

func Coerce(raw Raw, inputSchema json.RawMessage) (map[string]any, error) {
	var root, properties map[string]any
	if len(inputSchema) > 0 {
		v, err := jsonutil.Decode(inputSchema)
		schema, ok := v.(map[string]any)
		if err != nil || !ok {
			return nil, ErrInvalidSchema
		}
		root = schema
		if properties, err = rootProperties(schema); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(raw.Values))
	for key := range raw.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(keys))
	for _, key := range keys {
		value := raw.Values[key]
		var v any
		var err error
		if value.JSON != nil {
			if value.Text != "" {
				return nil, fmt.Errorf("%w: argument %q has inconsistent wire value", ErrInvalidArgs, key)
			}
			v, err = jsonutil.Decode(value.JSON)
			if err != nil {
				return nil, fmt.Errorf("%w: argument %q requires valid JSON", ErrInvalidArgs, key)
			}
		} else {
			typ, nullable := propertyType(root, properties[key])
			if nullable && typ != "string" && value.Text == "null" {
				v = nil
			} else if v, err = coerceText(key, value.Text, typ); err != nil {
				return nil, err
			}
		}
		out[key] = v
	}
	return out, nil
}

func coerceText(key, text, typ string) (any, error) {
	if typ == "string" || typ == "" {
		return text, nil
	}
	switch typ {
	case "integer", "number", "boolean", "array", "object", "null":
	default:
		return text, nil
	}
	v, err := jsonutil.Decode([]byte(text))
	valid := err == nil
	if valid {
		switch typ {
		case "integer":
			n, ok := v.(json.Number)
			valid = ok
			if ok {
				r, parsed := new(big.Rat).SetString(n.String())
				valid = parsed && r.IsInt()
			}
		case "number":
			_, valid = v.(json.Number)
		case "boolean":
			_, valid = v.(bool)
		case "array":
			_, valid = v.([]any)
		case "object":
			_, valid = v.(map[string]any)
		case "null":
			valid = v == nil
		}
	}
	if !valid {
		return nil, fmt.Errorf("%w: argument %q must be %s", ErrInvalidArgs, key, typ)
	}
	return v, nil
}
