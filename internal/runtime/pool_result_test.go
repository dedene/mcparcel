package runtime

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
)

// scriptedSession answers tools/list with one tool and every call with
// result and err.
type scriptedSession struct {
	schema string
	result mcpclient.Result
	err    error
	calls  atomic.Int32
	args   map[string]any
}

func (s *scriptedSession) Tools(context.Context) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"name":"t","inputSchema":` + s.schema + `}`)}, nil
}

func (s *scriptedSession) Call(_ context.Context, _ string, a, _ map[string]any, before func() error) (mcpclient.Result, error) {
	if before != nil {
		if e := before(); e != nil {
			return mcpclient.Result{}, e
		}
	}
	s.calls.Add(1)
	s.args = a
	r := s.result
	r.Dispatched = true
	return r, s.err
}

func (s *scriptedSession) Close(context.Context) error { return nil }

func scriptedRig(t *testing.T, s *scriptedSession) *poolRig {
	t.Helper()
	if s.schema == "" {
		s.schema = `{"type":"object"}`
	}
	r := newRig(t)
	r.stdio("a", false)
	r.opts.Connect = func(context.Context, mcpclient.ConnectOptions) (mcpclient.Session, error) {
		r.connects.Add(1)
		return &observedSession{Session: s, r: r}, nil
	}
	r.start()
	return r
}

func callWith(r *poolRig, values map[string]string) Response {
	a := emptyArgs()
	for k, v := range values {
		a.Values[k] = args.Value{Text: v}
	}
	return r.h.Handle(testCtx(r.t), testID, Request{Method: "call", Connection: "a", Tool: "t", Arguments: a}, func() error { return nil })
}

func decodeCallData(t *testing.T, r Response) output.CallData {
	t.Helper()
	var d output.CallData
	if e := json.Unmarshal(r.Data, &d); e != nil {
		t.Fatal(e, string(r.Data))
	}
	return d
}

func TestPoolPassesRawResultUnchanged(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/protocol/unknown_block.json")
	if err != nil {
		t.Fatal(err)
	}
	big := `{"content":[],"_meta":{"n":123456789012345678901234567890}}`
	for _, result := range []string{string(raw), big} {
		r := scriptedRig(t, &scriptedSession{result: mcpclient.Result{JSON: json.RawMessage(result)}})
		resp := callWith(r, nil)
		success(t, resp)
		got := decodeCallData(t, resp).Result
		var a, b any
		if json.Unmarshal(got, &a) != nil || json.Unmarshal([]byte(result), &b) != nil || !strings.Contains(string(got), "hologram") && result != big {
			t.Fatal(string(got))
		}
		if result == big && !strings.Contains(string(got), "123456789012345678901234567890") {
			t.Fatal("big integer lost", string(got))
		}
	}
}

func TestPoolValidationBeforeDispatch(t *testing.T) {
	s := &scriptedSession{schema: `{"type":"object","properties":{"limit":{"type":"integer","minimum":1}},"required":["limit"]}`}
	r := scriptedRig(t, s)
	for _, values := range []map[string]string{nil, {"limit": "0"}, {"limit": "x"}} {
		resp := callWith(r, values)
		responseCode(t, resp, "invalid_arguments", false)
		if resp.Data != nil || resp.Error.Details != nil {
			t.Fatal(resp)
		}
	}
	if s.calls.Load() != 0 || r.calls.Load() != 0 {
		t.Fatal("dispatched invalid arguments")
	}
	if resp := callWith(r, map[string]string{"limit": "0"}); !strings.Contains(resp.Error.Message, "/properties/limit (minimum)") {
		t.Fatal(resp.Error.Message)
	}
	success(t, callWith(r, map[string]string{"limit": "2"}))
	if s.args["limit"] != json.Number("2") {
		t.Fatalf("%#v", s.args)
	}
}

func TestPoolSchemaUncheckedWarning(t *testing.T) {
	s := &scriptedSession{schema: `{"$schema":"http://json-schema.org/draft-04/schema#","type":"object","required":["limit"]}`, result: mcpclient.Result{JSON: json.RawMessage(`{"content":[]}`)}}
	resp := callWith(scriptedRig(t, s), nil)
	success(t, resp)
	w := decodeCallData(t, resp).Warnings
	if len(w) != 1 || w[0].Code != "schema_unchecked" || s.calls.Load() != 1 {
		t.Fatalf("%+v", w)
	}
	s = &scriptedSession{schema: `{"type":"object"}`, result: mcpclient.Result{JSON: json.RawMessage(`{"content":[]}`)}}
	if w = decodeCallData(t, callWith(scriptedRig(t, s), nil)).Warnings; len(w) != 0 {
		t.Fatalf("%+v", w)
	}
}

func TestPoolResultTooLargeForFrame(t *testing.T) {
	old := maxCallDataBytes
	maxCallDataBytes = 1000
	t.Cleanup(func() { maxCallDataBytes = old })
	s := &scriptedSession{result: mcpclient.Result{JSON: json.RawMessage(`{"content":[{"type":"text","text":"` + strings.Repeat("<", 200) + `"}]}`)}}
	r := scriptedRig(t, s)
	resp := callWith(r, nil)
	responseCode(t, resp, "result_too_large", true)
	if resp.Data != nil || resp.Error.Details == nil || resp.Error.Details.Outcome != "unknown" || resp.Error.Details.RequestID != testID {
		t.Fatalf("%+v", resp.Error)
	}
	s.result.JSON = json.RawMessage(`{"content":[]}`)
	success(t, callWith(r, nil))
	if r.connects.Load() != 1 || r.closed.Load() != 0 {
		t.Fatal("retired a healthy session")
	}
}

func TestPoolServerErrorKeepsSession(t *testing.T) {
	s := &scriptedSession{err: output.ServerError(-32602, "bad limit")}
	r := scriptedRig(t, s)
	resp := callWith(r, nil)
	responseCode(t, resp, "server_error", true)
	d := resp.Error.Details
	if d == nil || d.RPCCode == nil || *d.RPCCode != -32602 || d.Outcome != "" || d.RequestID != testID || resp.Data != nil || resp.Error.Message != "The MCP server returned an error: bad limit" {
		t.Fatalf("%+v %+v", resp.Error, d)
	}
	callWith(r, nil)
	if r.connects.Load() != 1 || r.closed.Load() != 0 {
		t.Fatal("server_error retired the session")
	}
}

func TestPoolRetireFlag(t *testing.T) {
	s := &scriptedSession{err: output.ServerError(0, "gone"), result: mcpclient.Result{Retire: true}}
	r := scriptedRig(t, s)
	responseCode(t, callWith(r, nil), "server_error", true)
	s.err, s.result = nil, mcpclient.Result{JSON: json.RawMessage(`{"content":[]}`)}
	success(t, callWith(r, nil))
	if r.connects.Load() != 2 || r.closed.Load() != 1 {
		t.Fatal("Retire ignored", r.connects.Load(), r.closed.Load())
	}
}

func TestPoolErrorCopiesRPCCode(t *testing.T) {
	e := poolError(output.ServerError(5, "m"), nil, testID, true)
	if e.Code != "server_error" || e.Details.RPCCode == nil || *e.Details.RPCCode != 5 || !e.Details.Dispatched || e.Details.Outcome != "" || e.Message != "The MCP server returned an error: m" {
		t.Fatalf("%+v", e)
	}
	for _, code := range []string{"result_too_large", "protocol_error", "outcome_unknown", "canceled"} {
		if e = poolError(output.NewError(code, nil), nil, testID, true); e.Details.Outcome != "unknown" {
			t.Fatal(code, e.Details)
		}
		if e = poolError(output.NewError(code, nil), nil, testID, false); e.Details != nil {
			t.Fatal(code, e.Details)
		}
	}
}
