package jsonutil

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// Check applies Decode's rules (valid UTF-8, one value, no duplicate object
// keys, fewer than maxDepth nested delimiters) without building the value:
// it holds only the key sets of the objects currently open.
func Check(data []byte, maxDepth int) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("%w: invalid UTF-8", ErrInvalidJSON)
	}
	if !json.Valid(data) {
		return fmt.Errorf("%w: invalid syntax", ErrInvalidJSON)
	}
	type open struct {
		object, wantKey bool
		keys            map[string]struct{}
	}
	var stack []open
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '{', '[':
			if len(stack) >= maxDepth {
				return fmt.Errorf("%w: nesting exceeds %d at byte %d", ErrInvalidJSON, maxDepth, i)
			}
			stack = append(stack, open{object: data[i] == '{', wantKey: data[i] == '{'})
		case '}', ']':
			stack = stack[:len(stack)-1]
		case ',':
			if top := len(stack) - 1; stack[top].object {
				stack[top].wantKey = true
			}
		case '"':
			end, escaped := stringEnd(data, i)
			if top := len(stack) - 1; top >= 0 && stack[top].object && stack[top].wantKey {
				key := string(data[i+1 : end])
				if escaped {
					if err := json.Unmarshal(data[i:end+1], &key); err != nil {
						return fmt.Errorf("%w: invalid object key at byte %d", ErrInvalidJSON, i)
					}
				}
				if stack[top].keys == nil {
					stack[top].keys = map[string]struct{}{}
				}
				if _, exists := stack[top].keys[key]; exists {
					return fmt.Errorf("%w: duplicate object key at byte %d", ErrInvalidJSON, i)
				}
				stack[top].keys[key] = struct{}{}
				stack[top].wantKey = false
			}
			i = end
		}
	}
	return nil
}

// stringEnd returns the index of the quote closing the string that starts at
// start, in syntactically valid JSON, and whether the string has escapes.
func stringEnd(data []byte, start int) (int, bool) {
	escaped := false
	for i := start + 1; i < len(data); i++ {
		switch data[i] {
		case '\\':
			escaped = true
			i++
		case '"':
			return i, escaped
		}
	}
	return len(data) - 1, escaped
}
