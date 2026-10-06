package mcpclient

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

const callBody = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"x"}}`

func TestTapResetsOnRetriedPost(t *testing.T) {
	tap := &callTap{}
	tap.arm()
	if tap.sentBody([]byte(`{"jsonrpc":"2.0","id":6,"method":"tools/list"}`)) || tap.sentBody([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)) {
		t.Fatal("tapped another request")
	}
	if !tap.sentBody([]byte(callBody)) {
		t.Fatal("call not tapped")
	}
	first := tap.capture(io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":7,"result":{"first":true}}`)), 500, "application/json")
	if !tap.sentBody([]byte(callBody)) {
		t.Fatal("resent call not tapped")
	}
	if tap.sentBody([]byte(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{}}`)) {
		t.Fatal("tapped a second call")
	}
	second := tap.capture(io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":7,"result":{"second":true}}`)), 200, "application/json; charset=utf-8")
	_, _ = io.ReadAll(second)
	_, _ = io.ReadAll(first) // a stale body drained late must not capture
	got := tap.take()
	if string(got.Result) != `{"second":true}` || got.Status != 200 || !got.Answered {
		t.Fatalf("%+v %s", got, got.Result)
	}
	if again := tap.take(); again.Answered || again.Result != nil || tap.sentBody([]byte(callBody)) {
		t.Fatal("take did not disarm")
	}
}

func TestTapSkipsUnauthorizedBodies(t *testing.T) {
	tap := &callTap{}
	tap.arm()
	tap.sentBody([]byte(callBody))
	for _, status := range []int{401, 403} {
		body := io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":7,"error":{"code":1,"message":"no"}}`))
		if rc := tap.capture(body, status, "application/json"); rc != body {
			t.Fatal("captured", status)
		}
	}
	if got := tap.take(); got.RPCError != nil || got.Answered {
		t.Fatalf("%+v", got)
	}
}

func TestTapStdioHooks(t *testing.T) {
	tap := &callTap{}
	req := &jsonrpc.Request{ID: mustID(t, 3), Method: "tools/call"}
	tap.sent(req) // not armed
	tap.arm()
	tap.received(&jsonrpc.Response{ID: mustID(t, 3), Result: []byte(`{}`)})
	tap.sent(&jsonrpc.Request{Method: "notifications/cancelled"})
	tap.sent(req)
	tap.sent(&jsonrpc.Request{ID: mustID(t, 4), Method: "tools/call"})
	tap.received(&jsonrpc.Response{ID: mustID(t, 4), Result: []byte(`{"other":1}`)})
	tap.received(&jsonrpc.Response{ID: mustID(t, 3), Error: &jsonrpc.Error{Code: -1, Message: "m", Data: []byte(`"secret"`)}})
	got := tap.take()
	if got.RPCError == nil || got.RPCError.Code != -1 || got.RPCError.Message != "m" || got.RPCError.Data != nil || got.Result != nil {
		t.Fatalf("%+v", got)
	}
	tap.overflow()
	if tap.take().TooLarge {
		t.Fatal("overflow recorded while disarmed")
	}
	var none *callTap
	none.arm()
	none.sent(req)
	none.received(req)
	none.overflow()
	if none.sentBody([]byte(callBody)) || none.take().Answered {
		t.Fatal("nil tap")
	}
}

func mustID(t *testing.T, v float64) jsonrpc.ID {
	t.Helper()
	id, err := jsonrpc.MakeID(v)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSSEData(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n"} {
		raw := strings.ReplaceAll("event:  message \ndata: a \ndata:b\n\n: comment\nevent: ping\ndata: skipped\n\ndata:\n\nid: 1\ndata: c\n\ndata: partial", "\n", eol)
		got := sseData([]byte(raw), false)
		if len(got) != 2 || string(got[0]) != "a\nb" || string(got[1]) != "c" {
			t.Fatalf("%q: %q", eol, got)
		}
		// At EOF the SDK yields the pending event without its blank line.
		got = sseData([]byte(raw), true)
		if len(got) != 3 || string(got[2]) != "partial" {
			t.Fatalf("%q at EOF: %q", eol, got)
		}
	}
	// The SDK's reader ends lines at LF only, and stops at a line without a colon.
	for raw, want := range map[string]string{"data: a\rdata: b\r\r\n\n": "a\rdata: b", "data: a\n\nbad line\n\ndata: b\n\n": "a", "data: a\r\n\r\n": "a"} {
		if got := sseData([]byte(raw), true); len(got) != 1 || string(got[0]) != want {
			t.Fatalf("%q: %q", raw, got)
		}
	}
}

func TestLineLimitReader(t *testing.T) {
	tap := &callTap{}
	tap.arm()
	r := &lineLimitReader{r: io.NopCloser(strings.NewReader("12345\n1234\n123456")), limit: 5, tap: tap}
	b, err := io.ReadAll(r)
	if !errors.Is(err, errMessageTooLarge) || !tap.take().TooLarge || bytes.Contains(b, []byte("123456")) {
		t.Fatal(string(b), err)
	}
	r = &lineLimitReader{r: io.NopCloser(strings.NewReader("12345\n12345\n")), limit: 5}
	if b, err = io.ReadAll(r); err != nil || string(b) != "12345\n12345\n" {
		t.Fatal(string(b), err)
	}
}

func TestInspectResult(t *testing.T) {
	for raw, want := range map[string][2]bool{
		`{"content":[]}`: {false, false}, `{"isError":true}`: {true, false}, `{"isError":false}`: {false, false},
		`{"resultType":"input_required"}`: {false, true}, `{"IsError":true}`: {false, false}, `{"resultType":5}`: {false, false},
	} {
		isError, needsInput, err := inspectResult([]byte(raw))
		if err != nil || isError != want[0] || needsInput != want[1] {
			t.Fatal(raw, isError, needsInput, err)
		}
	}
	for _, raw := range []string{`[]`, `null`, `"x"`, `{"isError":null}`, `{"isError":"true"}`, `{"a":1,"a":2}`, `{`} {
		if _, _, err := inspectResult([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}
