package jsonutil

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckMatchesDecode(t *testing.T) {
	corpus := []string{
		`{}`, `[]`, `0`, `"x"`, ` {"a":1} `, `{"q":[{"a":1,"a":2}]}`, `{"a":1,"a":2}`, `{"a":{"a":1},"b":{"a":1}}`,
		`[{"a":1},{"a":1}]`, `{"a":"b","b":"a"}`, `{"a\"":1,"a\\":2}`, `{"x":["a","a"]}`, `{"a":[{"b":1}],"a":2}`,
		`{} {}`, `007`, ``, `{"secret":`, `[1,]`, `{"a" 1}`, `nul`, "\"fixture-\xff\"", "{\"\xff\":1}",
		`"fixture-�-目录"`, `{"k":"\ud800"}`, `{"":1,"":2}`, `[[[]]]`, `{"a":{"b":{"c":{}}},"d":1}`,
	}
	for _, raw := range corpus {
		_, decodeErr := Decode([]byte(raw))
		checkErr := Check([]byte(raw), 128)
		if (decodeErr == nil) != (checkErr == nil) {
			t.Fatalf("%q: Decode %v, Check %v", raw, decodeErr, checkErr)
		}
		if checkErr != nil && !errors.Is(checkErr, ErrInvalidJSON) {
			t.Fatalf("%q: %v does not wrap ErrInvalidJSON", raw, checkErr)
		}
	}
}

func TestCheckDepthParameter(t *testing.T) {
	nest := func(n int) []byte { return []byte(strings.Repeat("[", n) + "0" + strings.Repeat("]", n)) }
	for _, depth := range []int{1, 3, 125, 128} {
		if err := Check(nest(depth), depth); err != nil {
			t.Fatalf("depth %d rejected: %v", depth, err)
		}
		if err := Check(nest(depth+1), depth); !errors.Is(err, ErrInvalidJSON) {
			t.Fatalf("depth %d accepted at limit %d", depth+1, depth)
		}
	}
	if _, err := Decode(nest(129)); err == nil || Check(nest(129), 128) == nil {
		t.Fatal("Check and Decode disagree at the default depth")
	}
	if err := Check([]byte(`{"a":`+strings.Repeat(`{"b":`, 3)+"1"+strings.Repeat("}", 4)), 4); err != nil {
		t.Fatal(err)
	}
}
