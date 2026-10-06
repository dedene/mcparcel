package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func human(t *testing.T, data CallData) string {
	t.Helper()
	var b bytes.Buffer
	if err := WriteHuman(&b, data); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestHumanCallShowsArtifactPaths(t *testing.T) {
	data := CallData{
		Result:    json.RawMessage(`{"content":[{"type":"text","text":"look"},{"type":"image","data":"eA=="},{"type":"audio","data":"eA=="},{"type":"image","data":"eA=="}]}`),
		Artifacts: []Artifact{{Index: 1, Type: "image", Path: "/out/a-1.png"}, {Index: 2, Type: "audio", Path: "/out/a-2.wav"}},
	}
	if got := human(t, data); got != "look\n[image saved: /out/a-1.png]\n[audio saved: /out/a-2.wav]\n[image]\n" {
		t.Fatalf("%q", got)
	}
}

func TestHumanCallStructuredFallback(t *testing.T) {
	data := CallData{Result: json.RawMessage(`{"content":[{"type":"image","data":"eA=="}],"structuredContent":{ "answer": 42, "big": 12345678901234567890, "s": "a\u202eb" }}`)}
	if got := human(t, data); got != "[image]\n{\"answer\":42,\"big\":12345678901234567890,\"s\":\"a\\u202eb\"}\n" {
		t.Fatalf("%q", got)
	}
	data.Result = json.RawMessage("{\"content\":[],\"structuredContent\":{\"s\":\"a\u202eb\"}}")
	if got := human(t, data); got != "{\"s\":\"ab\"}\n" {
		t.Fatalf("%q", got)
	}
	data.Result = json.RawMessage(`{"content":[{"type":"text","text":"t"}],"structuredContent":{"x":1}}`)
	if got := human(t, data); got != "t\n" {
		t.Fatalf("%q", got)
	}
	data.Result = json.RawMessage(`{"content":[],"structuredContent":null}`)
	if got := human(t, data); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestHumanUnknownBlockLabel(t *testing.T) {
	data := CallData{Result: json.RawMessage(`{"content":[{"type":"resource_link","uri":"x"},{"type":"future_kind"},{"type":"Evil\u001b[2J"},{"type":"` + strings.Repeat("a", 33) + `"},{"type":""},{}]}`)}
	if got := human(t, data); got != "[resource_link]\n[future_kind]\n[content]\n[content]\n[content]\n[content]\n" {
		t.Fatalf("%q", got)
	}
}

func TestHumanCallToleratesOddBlocks(t *testing.T) {
	data := CallData{Result: json.RawMessage(`{"content":[{"type":"text","text":{"rich":true}},{"type":"text"},"string",7,null,[1],{"type":7},{"type":"text","text":"ok"}],"extra":{"kept":true}}`)}
	if got := human(t, data); got != "[text]\n[text]\n[content]\n[content]\n[content]\n[content]\n[content]\nok\n" {
		t.Fatalf("%q", got)
	}
	for _, odd := range []string{`{}`, `{"content":"x"}`, `{"content":{"a":1}}`, `{"content":null}`} {
		if got := human(t, CallData{Result: json.RawMessage(odd)}); got != "" {
			t.Fatalf("%s: %q", odd, got)
		}
	}
	var b bytes.Buffer
	if err := WriteHuman(&b, CallData{Result: json.RawMessage(`[]`)}); err == nil {
		t.Fatal("a non-object result must fail")
	}
}

func TestHumanCallStripsTerminalEscapes(t *testing.T) {
	text, _ := json.Marshal("line1\x1b]52;c;SGVsbG8=\x07\n\x1b[2Jline2\u202e\r\tend")
	data := CallData{Result: json.RawMessage(`{"content":[{"type":"text","text":` + string(text) + `}]}`)}
	if got := human(t, data); got != "line1\nline2\tend\n" {
		t.Fatalf("%q", got)
	}
}

func TestJSONExportFailedRetainsResult(t *testing.T) {
	data := CallData{Connection: "local:x", Tool: "media", Result: json.RawMessage(`{"content":[{"type":"image","mimeType":"image/png","data":"eA=="}]}`), Artifacts: []Artifact{{Index: 0, Type: "image", MIMEType: "image/png", Path: "/o/p-0.png", Bytes: 1}}}
	var b bytes.Buffer
	if err := WriteJSON(&b, data, NewError("export_failed", nil)); err != nil {
		t.Fatal(err)
	}
	want := `{"schemaVersion":1,"ok":false,"data":{"connection":"local:x","tool":"media","result":{"content":[{"type":"image","mimeType":"image/png","data":"eA=="}]},"artifacts":[{"index":0,"type":"image","mimeType":"image/png","path":"/o/p-0.png","bytes":1}]},"error":{"code":"export_failed","message":"The call finished, but MCParcel could not save its image or audio blocks.","nextAction":"The full result is in data.result; fix the output directory. Do not call the tool again just to export."}}` + "\n"
	if b.String() != want {
		t.Fatal(b.String())
	}
	b.Reset()
	data.Artifacts = nil
	if err := WriteJSON(&b, data, nil); err != nil || strings.Contains(b.String(), "artifacts") {
		t.Fatal(b.String(), err)
	}
}
