package jsonutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

var ErrInvalidJSON = errors.New("invalid JSON")

func Decode(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: invalid UTF-8", ErrInvalidJSON)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := readValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, invalid(d, "expected one value")
	}
	return v, nil
}

func invalid(d *json.Decoder, reason string) error {
	return fmt.Errorf("%w: %s at byte %d", ErrInvalidJSON, reason, d.InputOffset())
}

func readValue(d *json.Decoder, depth int) (any, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, invalid(d, "invalid syntax")
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	if depth >= 128 {
		return nil, invalid(d, "nesting exceeds 128")
	}
	switch delim {
	case '{':
		obj := make(map[string]any)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, invalid(d, "invalid object")
			}
			s, ok := key.(string)
			if !ok {
				return nil, invalid(d, "expected object key")
			}
			if _, exists := obj[s]; exists {
				return nil, invalid(d, "duplicate object key")
			}
			v, err := readValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			obj[s] = v
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return nil, invalid(d, "invalid object")
		}
		return obj, nil
	case '[':
		arr := make([]any, 0)
		for d.More() {
			v, err := readValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		if end, err := d.Token(); err != nil || end != json.Delim(']') {
			return nil, invalid(d, "invalid array")
		}
		return arr, nil
	default:
		return nil, invalid(d, "unexpected delimiter")
	}
}
