package jsonutil

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeDuplicateNested(t *testing.T) {
	v, err := Decode([]byte(`{"q":[{"a":1,"a":2}]}`))
	if !errors.Is(err, ErrInvalidJSON) || v != nil {
		t.Fatalf("got %v, %v", v, err)
	}
}

func TestDecodeNumberAndTrailing(t *testing.T) {
	v, err := Decode([]byte(`9007199254740993`))
	if err != nil || v != json.Number("9007199254740993") {
		t.Fatalf("got %v, %v", v, err)
	}
	for _, s := range []string{`{} {}`, `007`, ``, `{"secret":`} {
		if _, err := Decode([]byte(s)); !errors.Is(err, ErrInvalidJSON) {
			t.Fatalf("accepted %q: %v", s, err)
		}
	}
}

func TestDecodeDepthLimit(t *testing.T) {
	if _, err := Decode([]byte(strings.Repeat("[", 129) + "0" + strings.Repeat("]", 129))); !errors.Is(err, ErrInvalidJSON) || !strings.Contains(err.Error(), "nesting exceeds 128") {
		t.Fatal(err)
	}
	if _, err := Decode([]byte(strings.Repeat("[", 128) + "0" + strings.Repeat("]", 128))); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRejectsInvalidUTF8(t *testing.T) {
	for _, raw := range []string{"\"fixture-\xff\"", "{\"\xff\":1}", "{\"nested\":[\"\xc3\"]}"} {
		if value, err := Decode([]byte(raw)); !errors.Is(err, ErrInvalidJSON) || value != nil {
			t.Fatalf("accepted malformed UTF-8: %v, %v", value, err)
		}
	}
	if value, err := Decode([]byte(`"fixture-�-目录"`)); err != nil || value != "fixture-�-目录" {
		t.Fatalf("valid UTF-8 rejected: %v %v", value, err)
	}
}
