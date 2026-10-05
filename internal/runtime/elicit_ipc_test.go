package runtime

import (
	"context"
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/elicit"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
)

var testPrompt = elicit.Prompt{Message: `Allow Computer Use to use "Calculator"?`, RiskLevel: "high", Persist: []string{"session"}}

func TestElicitRequestValidation(t *testing.T) {
	empty := emptyArgs()
	for name, tc := range map[string]struct {
		r  Request
		ok bool
	}{
		"call terminal": {Request{Method: "call", Tool: "t", Arguments: empty, Prompt: "terminal"}, true},
		"call dialog":   {Request{Method: "call", Tool: "t", Arguments: empty, Prompt: "dialog"}, true},
		"call yes":      {Request{Method: "call", Tool: "t", Arguments: empty, Prompt: "yes"}, false},
		"call no input": {Request{Method: "call", Tool: "t", Arguments: empty, Prompt: "terminal", NoInput: true}, false},
		"tools":         {Request{Method: "tools", Connection: "a", Arguments: empty, Prompt: "terminal"}, false},
		"login":         {Request{Method: "login", Connection: "a", Arguments: empty, Prompt: "terminal"}, false},
		"status":        {Request{Method: "status", Arguments: empty, Prompt: "terminal"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if e := validateBody(frame("request", tc.r)); (e == nil) != tc.ok {
				t.Fatal(e)
			}
		})
	}
}

func TestElicitFrameValidation(t *testing.T) {
	body := func(kind, s string) Frame { return Frame{ProtocolVersion, kind, testID, json.RawMessage(s)} }
	pad := strings.Repeat(" ", elicit.MaxBody)
	form := elicit.Prompt{Message: "Name?", Fields: []elicit.Field{{Name: "name", Type: "string", Required: true}}}
	for name, tc := range map[string]struct {
		f  Frame
		ok bool
	}{
		"approval":         {frame("elicit", Elicit{testID, testPrompt}), true},
		"form":             {frame("elicit", Elicit{testID, form}), true},
		"bad prompt id":    {frame("elicit", Elicit{"ABC", testPrompt}), false},
		"upper prompt id":  {frame("elicit", Elicit{strings.ToUpper(testID), testPrompt}), false},
		"dirty message":    {frame("elicit", Elicit{testID, elicit.Prompt{Message: "\x1b[31mhi"}}), false},
		"overlong details": {frame("elicit", Elicit{testID, elicit.Prompt{Message: "hi", Details: strings.Repeat("a", 501)}}), false},
		"unknown field":    {body("elicit", `{"promptId":"`+testID+`","message":"hi","extra":1}`), false},
		"persist forever":  {frame("elicit", Elicit{testID, elicit.Prompt{Message: "hi", Persist: []string{"forever"}}}), false},
		"persist always":   {frame("elicit", Elicit{testID, elicit.Prompt{Message: "hi", Persist: []string{"session", "always"}}}), false},
		"elicit small":     {body("elicit", `{"promptId":"`+testID+`","message":"hi"}`), true},
		"elicit over cap":  {body("elicit", `{"promptId":"`+testID+`","message":"hi"`+pad+`}`), false},
		"accept session":   {frame("elicit_answer", ElicitAnswer{testID, elicit.Answer{Action: "accept", Persist: "session"}}), true},
		"accept always":    {frame("elicit_answer", ElicitAnswer{testID, elicit.Answer{Action: "accept", Persist: "always"}}), false},
		"accept content":   {frame("elicit_answer", ElicitAnswer{testID, elicit.Answer{Action: "accept", Content: map[string]any{"name": "x"}}}), true},
		"action maybe":     {frame("elicit_answer", ElicitAnswer{testID, elicit.Answer{Action: "maybe"}}), false},
		"decline persist":  {frame("elicit_answer", ElicitAnswer{testID, elicit.Answer{Action: "decline", Persist: "session"}}), false},
		"cancel content":   {frame("elicit_answer", ElicitAnswer{testID, elicit.Answer{Action: "cancel", Content: map[string]any{"a": true}}}), false},
		"answer bad id":    {frame("elicit_answer", ElicitAnswer{"x", elicit.Answer{Action: "decline"}}), false},
		"answer unknown":   {body("elicit_answer", `{"promptId":"`+testID+`","action":"decline","extra":1}`), false},
		"answer small":     {body("elicit_answer", `{"promptId":"`+testID+`","action":"decline"}`), true},
		"answer over cap":  {body("elicit_answer", `{"promptId":"`+testID+`","action":"decline"`+pad+`}`), false},
	} {
		t.Run(name, func(t *testing.T) {
			if e := validateBody(tc.f); (e == nil) != tc.ok {
				t.Fatal(e)
			}
		})
	}
}

// promptHandler asks its prompter asks times (once by default) after
// dispatch and returns what it saw as the call result.
type promptHandler struct {
	daemonHandler
	asks  int
	asked chan elicit.Answer
}

type promptResult struct {
	Prompter bool            `json:"prompter"`
	Forms    bool            `json:"forms"`
	Answers  []elicit.Answer `json:"answers"`
}

func (h *promptHandler) Handle(ctx context.Context, id string, r Request, before func() error) Response {
	if e := before(); e != nil {
		return Response{Error: output.NewError("canceled", nil)}
	}
	var out promptResult
	if p := mcpclient.PrompterFrom(ctx); p != nil {
		out.Prompter, out.Forms = true, p.Forms
		for range max(h.asks, 1) {
			a := p.Ask(ctx, testPrompt)
			if h.asked != nil {
				h.asked <- a
			}
			out.Answers = append(out.Answers, a)
		}
	}
	if ctx.Err() != nil {
		return Response{Dispatched: true, Error: output.NewError("canceled", &output.Details{RequestID: id, Dispatched: true, Outcome: "unknown"})}
	}
	res, _ := json.Marshal(out)
	b, _ := json.Marshal(output.CallData{Connection: r.Connection, Tool: r.Tool, Result: res})
	return Response{Data: b, Dispatched: true}
}

// promptOutcome decodes a raw response's call data.
func promptOutcome(t *testing.T, raw json.RawMessage) promptResult {
	t.Helper()
	var d output.CallData
	if e := json.Unmarshal(raw, &d); e != nil {
		t.Fatal(e)
	}
	return resultOf(t, d.Result)
}

func resultOf(t *testing.T, result json.RawMessage) promptResult {
	t.Helper()
	var out promptResult
	if e := json.Unmarshal(result, &out); e != nil {
		t.Fatal(e)
	}
	return out
}

func actions(r promptResult) string {
	var s []string
	for _, a := range r.Answers {
		s = append(s, a.Action+"/"+a.Persist)
	}
	return strings.Join(s, ",")
}

// rawCall sends a call on a raw socket and reads its dispatch.
func rawCall(t *testing.T, c *Client, prompt string) *rawConn {
	t.Helper()
	conn, e := dialSocket(testCtx(t), c.Paths)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, e = ClientHandshake(conn, "dev", "work", testID, configRoot(c.Paths.ConfigDir)); e != nil {
		t.Fatal(e)
	}
	if e = WriteFrame(conn, frame("request", Request{Method: "call", Connection: "fixture", Tool: "t", Arguments: emptyArgs(), Prompt: prompt})); e != nil {
		t.Fatal(e)
	}
	rc := &rawConn{t, conn}
	rc.read("dispatch")
	return rc
}

type rawConn struct {
	t *testing.T
	*net.UnixConn
}

func (c *rawConn) read(kind string) Frame {
	c.t.Helper()
	f, e := ReadFrame(c.UnixConn)
	if e != nil || f.Kind != kind {
		c.t.Fatalf("%+v %v want %s", f, e, kind)
	}
	return f
}

func (c *rawConn) elicit() Elicit {
	c.t.Helper()
	var e Elicit
	if err := decodeBody(c.read("elicit").Body, &e); err != nil {
		c.t.Fatal(err)
	}
	return e
}

func (c *rawConn) answer(id string, a elicit.Answer) {
	c.t.Helper()
	if e := WriteFrame(c.UnixConn, frame("elicit_answer", ElicitAnswer{id, a})); e != nil {
		c.t.Fatal(e)
	}
}

func (c *rawConn) response() Response {
	c.t.Helper()
	var r Response
	if e := decodeBody(c.read("response").Body, &r); e != nil {
		c.t.Fatal(e)
	}
	return r
}

func waitAnswer(t *testing.T, ch chan elicit.Answer) elicit.Answer {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("no answer")
		return elicit.Answer{}
	}
}

var (
	accept  = elicit.Answer{Action: "accept"}
	decline = elicit.Answer{Action: "decline"}
)

func TestSocketPrompter(t *testing.T) {
	sent := make(chan Elicit, 4)
	sp := newSocketPrompter(context.Background(), func(_ context.Context, e Elicit) error { sent <- e; return nil }, 50*time.Millisecond)
	if a := sp.ask(testCtx(t), testPrompt); a.Action != "cancel" {
		t.Fatal(a)
	}
	first := <-sent
	if !reflect.DeepEqual(first.Prompt, testPrompt) || len(first.PromptID) != 32 {
		t.Fatalf("%+v", first)
	}
	sp.deliver(ElicitAnswer{first.PromptID, accept})
	sp.timeout = time.Hour
	got := make(chan elicit.Answer, 2)
	go func() { got <- sp.ask(testCtx(t), testPrompt) }()
	go func() { got <- sp.ask(testCtx(t), testPrompt) }()
	second := <-sent
	if len(sent) != 0 || second.PromptID == first.PromptID {
		t.Fatal("second prompt opened while one was open")
	}
	sp.deliver(ElicitAnswer{first.PromptID, accept})
	sp.deliver(ElicitAnswer{second.PromptID, decline})
	sp.deliver(ElicitAnswer{second.PromptID, accept})
	if a := waitAnswer(t, got); a.Action != "decline" {
		t.Fatal(a)
	}
	third := <-sent
	sp.deliver(ElicitAnswer{third.PromptID, accept})
	if a := waitAnswer(t, got); a.Action != "accept" {
		t.Fatal(a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if a := sp.ask(ctx, testPrompt); a.Action != "cancel" {
		t.Fatal(a)
	}
}

func TestPromptsDropSuperseded(t *testing.T) {
	var n atomic.Int32
	var firstErr error
	p := newPrompts(testCtx(t), func(ctx context.Context, _ elicit.Prompt) elicit.Answer {
		if n.Add(1) == 1 {
			<-ctx.Done()
			firstErr = ctx.Err()
			return elicit.Answer{Action: "cancel"}
		}
		return elicit.Answer{Action: "accept", Persist: "session"}
	})
	defer func() { p.stop() }()
	one, two := testID, strings.Repeat("e", 32)
	p.start(Elicit{one, testPrompt})
	p.start(Elicit{two, testPrompt})
	if a := <-p.answers; a.PromptID != two || a.Action != "accept" || firstErr != context.Canceled {
		t.Fatal(a, firstErr)
	}
	select {
	case a := <-p.answers:
		t.Fatal("stale answer", a)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestClientRelaysElicitation(t *testing.T) {
	c, _ := service(t, &promptHandler{}, 0)
	var mu sync.Mutex
	var seen []elicit.Prompt
	reply := elicit.Answer{Action: "accept", Persist: "session"}
	c.Prompt = "terminal"
	c.OnElicit = func(_ context.Context, p elicit.Prompt) elicit.Answer {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, p)
		return reply
	}
	r, e := c.Call(testCtx(t), callReq())
	if e != nil {
		t.Fatal(e)
	}
	got := resultOf(t, r.Data.Result)
	mu.Lock()
	if !got.Prompter || !got.Forms || actions(got) != "accept/session" || len(seen) != 1 || !reflect.DeepEqual(seen[0], testPrompt) {
		t.Fatalf("%+v %+v", got, seen)
	}
	reply = elicit.Answer{Action: "maybe"}
	mu.Unlock()
	c.Prompt = "dialog"
	r, e = c.Call(testCtx(t), callReq())
	if got = resultOf(t, r.Data.Result); e != nil || got.Forms || actions(got) != "cancel/" {
		t.Fatalf("%+v %v", got, e)
	}
	c.OnElicit = nil
	r, e = c.Call(testCtx(t), callReq())
	if got = resultOf(t, r.Data.Result); e != nil || got.Prompter {
		t.Fatalf("%+v %v", got, e)
	}
}

func TestRawElicitIgnoresStaleAnswers(t *testing.T) {
	c, _ := service(t, &promptHandler{asks: 2}, 0)
	conn := rawCall(t, c, "terminal")
	one := conn.elicit()
	conn.answer(strings.Repeat("f", 32), decline)
	conn.answer(one.PromptID, accept)
	two := conn.elicit()
	conn.answer(one.PromptID, accept)
	conn.answer(two.PromptID, decline)
	r := conn.response()
	if got := promptOutcome(t, r.Data); r.Error != nil || actions(got) != "accept/,decline/" {
		t.Fatalf("%+v %+v", got, r.Error)
	}
}

func TestRawElicitAnswerWithoutPrompt(t *testing.T) {
	h := &daemonHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
	c, _ := service(t, h, 0)
	conn := rawCall(t, c, "")
	<-h.started
	conn.answer(testID, decline)
	if r := conn.response(); r.Error == nil || r.Error.Code != "protocol_error" {
		t.Fatalf("%+v", r)
	}
}

func TestRawElicitDisconnectCancels(t *testing.T) {
	h := &promptHandler{asked: make(chan elicit.Answer, 1)}
	c, _ := service(t, h, 0)
	conn := rawCall(t, c, "terminal")
	conn.elicit()
	_ = conn.Close()
	if a := waitAnswer(t, h.asked); a.Action != "cancel" {
		t.Fatal(a)
	}
}

func TestRawElicitPromptTimeout(t *testing.T) {
	c, _ := serviceWith(t, DaemonOptions{Handler: &promptHandler{}, PromptTimeout: 50 * time.Millisecond})
	conn := rawCall(t, c, "terminal")
	conn.elicit()
	r := conn.response()
	if got := promptOutcome(t, r.Data); r.Error != nil || actions(got) != "cancel/" {
		t.Fatalf("%+v %+v", got, r.Error)
	}
}

func TestClientSupersedesPrompt(t *testing.T) {
	c, _ := serviceWith(t, DaemonOptions{Handler: &promptHandler{asks: 2}, PromptTimeout: 50 * time.Millisecond})
	var n atomic.Int32
	firstErr := make(chan error, 1)
	c.Prompt = "terminal"
	c.OnElicit = func(ctx context.Context, _ elicit.Prompt) elicit.Answer {
		if n.Add(1) == 1 {
			<-ctx.Done()
			firstErr <- ctx.Err()
			return elicit.Answer{Action: "cancel"}
		}
		return accept
	}
	r, e := c.Call(testCtx(t), callReq())
	if got := resultOf(t, r.Data.Result); e != nil || actions(got) != "cancel/,accept/" {
		t.Fatalf("%+v %v", got, e)
	}
	if e = <-firstErr; e != context.Canceled {
		t.Fatal(e)
	}
}

func TestOpenPromptDoesNotBlockOthers(t *testing.T) {
	c, _ := service(t, &promptHandler{}, 0)
	conn := rawCall(t, c, "terminal")
	one := conn.elicit()
	if s, e := c.Status(testCtx(t)); e != nil || s.ActiveCalls != 1 {
		t.Fatal(s, e)
	}
	r, e := c.Call(testCtx(t), callReq())
	if got := resultOf(t, r.Data.Result); e != nil || got.Prompter {
		t.Fatalf("%+v %v", got, e)
	}
	conn.answer(one.PromptID, accept)
	if r := conn.response(); r.Error != nil || actions(promptOutcome(t, r.Data)) != "accept/" {
		t.Fatalf("%+v", r)
	}
}

func TestClientInterruptDuringPrompt(t *testing.T) {
	h := &promptHandler{asked: make(chan elicit.Answer, 1)}
	c, _ := service(t, h, 0)
	ctx, cancel := context.WithCancel(testCtx(t))
	defer cancel()
	opened := make(chan struct{})
	var returned atomic.Bool
	c.Prompt = "terminal"
	c.OnElicit = func(ctx context.Context, _ elicit.Prompt) elicit.Answer {
		close(opened)
		<-ctx.Done()
		returned.Store(true)
		return elicit.Answer{Action: "cancel"}
	}
	done := make(chan error, 1)
	go func() { _, e := c.Call(ctx, callReq()); done <- e }()
	<-opened
	cancel()
	wantCode(t, <-done, "canceled")
	if !returned.Load() {
		t.Fatal("prompt still open after the call returned")
	}
	if a := waitAnswer(t, h.asked); a.Action != "cancel" {
		t.Fatal(a)
	}
}
