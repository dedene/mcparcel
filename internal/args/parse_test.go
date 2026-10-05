package args

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func parseError(t *testing.T, assignments []string, inline string, reader io.Reader, message string) {
	t.Helper()
	_, err := Parse(assignments, inline, reader)
	if !errors.Is(err, ErrInvalidArgs) || err.Error() != "invalid arguments: "+message {
		t.Fatalf("got %v", err)
	}
}

func TestParseEmpty(t *testing.T) {
	got, err := Parse(nil, "", nil)
	if err != nil || !reflect.DeepEqual(got, Raw{Values: map[string]Value{}}) {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestParseDelimiters(t *testing.T) {
	got, err := Parse([]string{"limit=5", "name:Bob", "url=https://x.invalid:a=b"}, "", nil)
	want := Raw{Values: map[string]Value{"limit": {Text: "5"}, "name": {Text: "Bob"}, "url": {Text: "https://x.invalid:a=b"}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestParseExplicitJSON(t *testing.T) {
	got, err := Parse([]string{"limit:=5", "nil:=null", `s:="007"`}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, w := range map[string]string{"limit": "5", "nil": "null", "s": `"007"`} {
		if string(got.Values[k].JSON) != w || got.Values[k].Text != "" {
			t.Fatal(got)
		}
	}
}

func TestParseEmptyAndDottedKey(t *testing.T) {
	got, err := Parse([]string{"name=", "a.b=x"}, "", nil)
	if err != nil || len(got.Values) != 2 || got.Values["name"].JSON != nil || got.Values["name"].Text != "" || got.Values["a.b"].Text != "x" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestParseDuplicate(t *testing.T) {
	parseError(t, []string{"a=1", "a:2"}, "", nil, `duplicate key "a"`)
}

func TestParseSourceConflict(t *testing.T) {
	parseError(t, []string{"a=1"}, "{}", nil, "choose one argument source")
	parseError(t, []string{"a=1"}, "", strings.NewReader("{}"), "choose one argument source")
	parseError(t, nil, "{}", strings.NewReader("{}"), "choose one argument source")
}

func TestParseObjectOnly(t *testing.T) {
	for _, s := range []string{"[]", "null", "1"} {
		parseError(t, nil, s, nil, "payload must be one JSON object")
		parseError(t, nil, "", strings.NewReader(s), "payload must be one JSON object")
	}
	parseError(t, nil, "", strings.NewReader(""), "payload must be one JSON object")
}

func TestParseInvalidAssignment(t *testing.T) {
	for _, s := range []string{"a", "=x", ":x", "a b=x", "a\x00=x"} {
		parseError(t, []string{s}, "", nil, "expected key=value, key:value or key:=json")
	}
}

func TestParseBadForcedJSON(t *testing.T) {
	for _, s := range []string{"x:=NaN", "x:=", `x:={"a":1,"a":2}`} {
		parseError(t, []string{s}, "", nil, `argument "x" requires valid JSON`)
	}
}

func TestParseLimit(t *testing.T) {
	s := `{"x":"` + strings.Repeat("a", MaxPayloadBytes-8) + `"}`
	if len(s) != MaxPayloadBytes {
		t.Fatal(len(s))
	}
	for _, r := range []bool{false, true} {
		var err error
		if r {
			_, err = Parse(nil, "", strings.NewReader(s))
		} else {
			_, err = Parse(nil, s, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	parseError(t, nil, "", strings.NewReader(s+" "), "payload exceeds 2097152 bytes")
	parseError(t, nil, s+" ", nil, "payload exceeds 2097152 bytes")
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("secret-reader-token") }
func TestParseReaderFailure(t *testing.T) {
	_, err := Parse(nil, "", failedReader{})
	if !errors.Is(err, ErrInvalidArgs) || strings.Contains(err.Error(), "secret-reader-token") {
		t.Fatal(err)
	}
}
