package output

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Human tools output gives each tool's signature, so a reader sees what to
// pass without --json, and its description indented below it.
func TestHumanToolsSignatures(t *testing.T) {
	list := ToolList{Connection: "local:fixture", Items: []json.RawMessage{
		json.RawMessage(`{"name":"echo","description":"Echo the text back.","inputSchema":{"type":"object","properties":{"times":{"type":"integer"},"text":{"type":"string"}},"required":["text"]}}`),
		json.RawMessage(`{"name":"ping","inputSchema":{"type":"object"}}`),
		json.RawMessage(`{"name":"broken","description":"Bad schema.","inputSchema":[]}`),
		json.RawMessage(`{"name":"search","description":"Search.\n\n  - by title\n  - by tag   \n","inputSchema":{"type":"object","properties":{"mode":{"enum":["title","tag"]},"tags":{"type":"array","items":{"type":"string"}}}}}`),
	}}
	var b bytes.Buffer
	if err := WriteHuman(&b, list); err != nil {
		t.Fatal(err)
	}
	want := "broken\n  Bad schema.\n" +
		"echo(text: string, times?: integer)\n  Echo the text back.\n" +
		"ping()\n" +
		"search(mode?: title|tag, tags?: string[])\n  Search.\n    - by title\n    - by tag\n"
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
}

// A server cannot drive the terminal through a tool's name or description.
func TestHumanToolsCleansServerText(t *testing.T) {
	list := ToolList{Items: []json.RawMessage{
		json.RawMessage(`{"name":"ev\u001b[2Jil","description":"line\u001b]0;title\u0007 one\r\nline two\u202e"}`),
	}}
	var b bytes.Buffer
	if err := WriteHuman(&b, list); err != nil || b.String() != "evil()\n  line one\n  line two\n" {
		t.Fatalf("%q %v", b.String(), err)
	}
}
