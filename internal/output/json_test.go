package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestJSONSuccess(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSON(&b, map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if b.String() != `{"schemaVersion":1,"ok":true,"data":{},"error":null}`+"\n" {
		t.Fatal(b.String())
	}
}

func TestJSONFailure(t *testing.T) {
	var b bytes.Buffer
	e := NewError("schema_cache_miss", nil)
	if err := WriteJSON(&b, "discard me", e); err != nil {
		t.Fatal(err)
	}
	want := `{"schemaVersion":1,"ok":false,"data":null,"error":{"code":"schema_cache_miss","message":"No cached tool schema is available.","nextAction":"Run mcparcel tools without --cached."}}` + "\n"
	if b.String() != want || ExitCode(e) != 6 {
		t.Fatal(b.String(), ExitCode(e))
	}
}

func TestJSONToolErrorRetainsResult(t *testing.T) {
	result := json.RawMessage(`{"content":[{"type":"text","text":"no"}],"isError":true,"_meta":{"x":1}}`)
	for _, code := range []string{"tool_error", "input_required"} {
		t.Run(code, func(t *testing.T) {
			var b bytes.Buffer
			data := CallData{Connection: "local:fixture", Tool: "tool", Result: result}
			if err := WriteJSON(&b, data, NewError(code, nil)); err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				OK    bool
				Data  CallData
				Error *Error
			}
			if err := json.Unmarshal(b.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.OK || envelope.Error.Code != code || envelope.Data.Connection != data.Connection || envelope.Data.Tool != data.Tool {
				t.Fatal(envelope)
			}
			compareJSON(t, envelope.Data.Result, result)
			if code == "tool_error" && ExitCode(envelope.Error) != 5 {
				t.Fatal(envelope.Error)
			}
		})
	}
}

func TestJSONCompleteContent(t *testing.T) {
	result := json.RawMessage(`{"structuredContent":{"exact":9007199254740993},"content":[{"type":"text","text":"hello"},{"type":"image","data":"aW1hZ2U=","mimeType":"image/png"},{"type":"audio","data":"YXVkaW8=","mimeType":"audio/wav"},{"type":"resource","resource":{"uri":"fixture://a","text":"body"}}],"_meta":{"x":1}}`)
	var b bytes.Buffer
	if err := WriteJSON(&b, CallData{Connection: "local:fixture", Tool: "tool", Result: result}, nil); err != nil {
		t.Fatal(err)
	}
	var envelope struct{ Data map[string]json.RawMessage }
	if err := json.Unmarshal(b.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 3 {
		t.Fatal(envelope.Data)
	}
	compareJSON(t, envelope.Data["result"], result)
}

func compareJSON(t *testing.T, a, b []byte) {
	t.Helper()
	decode := func(data []byte) any {
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if !reflect.DeepEqual(decode(a), decode(b)) {
		t.Fatal(string(a), string(b))
	}
}

func TestCancellationDetails(t *testing.T) {
	for _, after := range []bool{false, true} {
		var details *Details
		if after {
			details = &Details{RequestID: "request-1", Dispatched: true, Outcome: "unknown"}
		}
		e := NewError("canceled", details)
		var b bytes.Buffer
		if err := WriteJSON(&b, nil, e); err != nil {
			t.Fatal(err)
		}
		if ExitCode(e) != 130 {
			t.Fatal(e)
		}
		var envelope Envelope
		if err := json.Unmarshal(b.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(envelope.Error.Details, details) {
			t.Fatal(envelope)
		}
		if after {
			lost := NewError("outcome_unknown", details)
			if ExitCode(lost) != 6 {
				t.Fatal(lost)
			}
			if err := WriteJSON(&b, nil, lost); err != nil {
				t.Fatal(err)
			}
		}
	}
}

type failWriter struct {
	calls int
	data  []byte
	err   error
	short bool
}

func (w *failWriter) Write(p []byte) (int, error) {
	w.calls++
	w.data = append([]byte(nil), p...)
	if w.short {
		return len(p) - 1, nil
	}
	return 0, w.err
}

func TestWriterFailure(t *testing.T) {
	sentinel := errors.New("writer failed")
	w := &failWriter{err: sentinel}
	if err := WriteJSON(w, map[string]any{}, nil); err != sentinel || w.calls != 1 || string(w.data) != `{"schemaVersion":1,"ok":true,"data":{},"error":null}`+"\n" {
		t.Fatal(err, w)
	}
	w = &failWriter{}
	if err := WriteJSON(w, make(chan int), nil); err == nil || w.calls != 0 {
		t.Fatal(err, w)
	}
	w = &failWriter{short: true}
	if err := WriteJSON(w, nil, nil); !errors.Is(err, io.ErrShortWrite) || w.calls != 1 {
		t.Fatal(err, w)
	}
}

func TestHumanBlocks(t *testing.T) {
	data := CallData{Connection: "local:fixture", Tool: "fixture", Result: json.RawMessage(`{"content":[{"type":"text","text":"hello"},{"type":"image","data":"aW1hZ2U="},{"type":"audio","data":"YXVkaW8="}]}`)}
	var b bytes.Buffer
	if err := WriteHuman(&b, data); err != nil || b.String() != "hello\n[image]\n[audio]\n" {
		t.Fatal(b.String(), err)
	}
	b.Reset()
	data.Result = json.RawMessage(`{"content":[{"type":"resource","resource":{"uri":"fixture://resource"}},{"type":"resource_link","uri":"fixture://link"}]}`)
	if err := WriteHuman(&b, data); err != nil || b.String() != "[resource]\n[resource_link]\n" {
		t.Fatal(b.String(), err)
	}
}

func TestToolListAndHuman(t *testing.T) {
	list := ToolList{Connection: "local:fixture", SourceRevisions: map[string]string{"personal": "hash"}}
	var b bytes.Buffer
	if err := WriteJSON(&b, list, nil); err != nil {
		t.Fatal(err)
	}
	want := `{"schemaVersion":1,"ok":true,"data":{"connection":"local:fixture","items":[],"sourceRevisions":{"personal":"hash"},"cacheAgeSeconds":null},"error":null}` + "\n"
	if b.String() != want {
		t.Fatal(b.String())
	}
	list.Items = []json.RawMessage{json.RawMessage(`{"name":"zeta","description":"Last"}`), json.RawMessage(`{"name":"alpha","description":"First"}`)}
	b.Reset()
	if err := WriteHuman(&b, list); err != nil || b.String() != "alpha: First\nzeta: Last\n" {
		t.Fatal(b.String(), err)
	}
	if string(list.Items[0]) != `{"name":"zeta","description":"Last"}` {
		t.Fatal("mutated input")
	}
	b.Reset()
	if err := WriteHuman(&b, "Runtime ready.\n"); err != nil || b.String() != "Runtime ready.\n" {
		t.Fatal(b.String(), err)
	}
	b.Reset()
	if err := WriteHuman(&b, CallData{Result: json.RawMessage(`{"content":[{"type":"text","text":"secret"},`)}); err == nil || b.Len() != 0 {
		t.Fatal(err, b.String())
	}
}

func TestFailureRetainsOnlyCallData(t *testing.T) {
	for _, code := range []string{"tool_error", "input_required"} {
		var b bytes.Buffer
		if err := WriteJSON(&b, "arbitrary-failure-data", NewError(code, nil)); err != nil {
			t.Fatal(err)
		}
		var envelope Envelope
		if err := json.Unmarshal(b.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data != nil {
			t.Fatal(envelope.Data)
		}
	}
}

func TestImportFailureEnvelopeDataNull(t *testing.T) {
	var b bytes.Buffer
	failure := NewError("import_blocked", &Details{ImportReport: json.RawMessage(`{"entries":[]}`)})
	if e := WriteJSON(&b, "discard", failure); e != nil {
		t.Fatal(e)
	}
	want := `{"schemaVersion":1,"ok":false,"data":null,"error":{"code":"import_blocked","message":"Selected import entries require changes.","nextAction":"Review the import report, provide bindings, or choose an applicable --only subset.","details":{"importReport":{"entries":[]}}}}` + "\n"
	if b.String() != want {
		t.Fatal(b.String())
	}
}
