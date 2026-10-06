package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/output"
)

// largeResponse is a call response whose frame is at least size bytes; '<'
// grows sixfold when encoding/json escapes it.
func largeResponse(t *testing.T, size int) Frame {
	t.Helper()
	text, _ := json.Marshal(strings.Repeat("<", size/6))
	data, err := json.Marshal(output.CallData{Connection: "local:x", Tool: "large", Result: json.RawMessage(`{"content":[{"type":"text","text":` + string(text) + `}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	return frame("response", Response{Data: data, Dispatched: true})
}

func TestFrameAcceptsLargeCallResult(t *testing.T) {
	var b bytes.Buffer
	if err := WriteFrame(&b, largeResponse(t, 48<<20)); err != nil {
		t.Fatal(err)
	}
	if b.Len() < 48<<20 {
		t.Fatal("frame smaller than intended", b.Len())
	}
	raw := b.Bytes()
	f, err := readFrame(bytes.NewReader(raw), MaxResponseFrameBytes)
	if err != nil || f.Kind != "response" {
		t.Fatal(err)
	}
	var r Response
	if err = decodeBody(f.Body, &r); err != nil || !r.Dispatched {
		t.Fatal(err)
	}
	if _, err = ReadFrame(bytes.NewReader(raw)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatal("daemon reader accepted a 48 MiB frame", err)
	}
	if err = WriteFrame(&b, largeResponse(t, MaxResponseFrameBytes+1)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatal(err)
	}
}

func TestDaemonFrameLimitUnchanged(t *testing.T) {
	if MaxFrameBytes != 16<<20 || MaxResponseFrameBytes != 64<<20 {
		t.Fatal("limits changed")
	}
	var b bytes.Buffer
	if err := WriteFrame(&b, largeResponse(t, MaxFrameBytes+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFrame(bytes.NewReader(b.Bytes())); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatal(err)
	}
	for _, header := range [][]byte{{4, 0, 0, 1}, {255, 255, 255, 255}} {
		if _, err := readFrame(bytes.NewReader(header), MaxResponseFrameBytes); !errors.Is(err, ErrFrameTooLarge) {
			t.Fatal(err)
		}
	}
}

func TestFrameResultDepthBoundary(t *testing.T) {
	nest := func(levels int) json.RawMessage {
		return json.RawMessage(`{"x":` + strings.Repeat("[", levels-1) + strings.Repeat("]", levels-1) + `}`)
	}
	for levels, ok := range map[int]bool{125: true, 126: false} {
		data, _ := json.Marshal(output.CallData{Connection: "local:x", Tool: "t", Result: nest(levels)})
		var b bytes.Buffer
		if err := WriteFrame(&b, frame("response", Response{Data: data, Dispatched: true})); err != nil {
			t.Fatal(err)
		}
		if _, err := readFrame(&b, MaxResponseFrameBytes); (err == nil) != ok {
			t.Fatal(levels, err)
		}
	}
}
