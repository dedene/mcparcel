package mcpclient_test

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"
)

// Regression: the SDK accepts a final SSE event that ends at EOF without a
// blank line, and trims the event name; the tap missed both and fell back to
// the lossy typed result, or to outcome_unknown.
func TestRawResultSSEFinalEventAtEOF(t *testing.T) {
	frames := map[string]func(data string) string{
		"no blank line":   func(d string) string { return "event: message\ndata: " + d + "\n" },
		"no final LF":     func(d string) string { return "event: message\ndata: " + d },
		"CRLF at EOF":     func(d string) string { return "event: message\r\ndata: " + d + "\r\n" },
		"padded name":     func(d string) string { return "event:  message \ndata: " + d + "\n\n" },
		"padded name EOF": func(d string) string { return "id: 1\nevent:\tmessage\t\ndata:" + d },
	}
	for _, name := range []string{"unknown_block.json", "bigint_meta.json"} {
		want := sample(t, name)
		for label, frame := range frames {
			t.Run(name+"/"+strconv.Quote(label), func(t *testing.T) {
				s := rawHTTPServer(t, 0, func(id json.RawMessage) (int, string, []byte) {
					return 200, "text/event-stream", []byte(frame(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":` + string(want) + `}`))
				})
				r := call(t, s, "echo", nil)
				if !bytes.Equal(r.JSON, want) {
					t.Fatalf("got %s", r.JSON)
				}
			})
		}
	}
	t.Run("rpc error", func(t *testing.T) {
		s := rawHTTPServer(t, 0, func(id json.RawMessage) (int, string, []byte) {
			return 200, "text/event-stream", []byte(`data: {"jsonrpc":"2.0","id":` + string(id) + `,"error":{"code":-32602,"message":"nope"}}`)
		})
		_, e := s.Call(ctx(t), "echo", nil, nil, nil)
		code(t, e, "server_error")
		if rpcCode(t, e) != -32602 {
			t.Fatal(e)
		}
	})
}
