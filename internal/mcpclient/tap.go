package mcpclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/dedene/mcparcel/internal/jsonutil"
	"github.com/dedene/mcparcel/internal/output"
)

// defaultMaxMessageBytes bounds one MCP message: a stdio line, or an HTTP
// response including the events streamed before the result.
const defaultMaxMessageBytes = 16 << 20

// maxResultDepth is the deepest result whose call frame still passes the IPC
// reader's depth limit of 128: the result sits under frame, body and data.
const maxResultDepth = 125

var errMessageTooLarge = errors.New("MCP message exceeds the size limit")

// callTap captures the raw answer to the one tools/call in flight on a
// session, below the SDK's typed decoding. The pool gate keeps calls on one
// session from overlapping, so a single armed slot is enough.
type callTap struct {
	mu                               sync.Mutex
	armed, hasID, answered, tooLarge bool
	id                               jsonrpc.ID
	status                           int
	gen                              int // HTTP POST attempt the buffer belongs to
	buf                              []byte
	sse, eof                         bool // eof: the captured body ended
	result                           json.RawMessage
	rpcErr                           *jsonrpc.Error
}

type tapResult struct {
	Answered, TooLarge bool
	Status             int
	Result             json.RawMessage
	RPCError           *jsonrpc.Error
}

// arm resets the tap before a call.
func (t *callTap) arm() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reset(true)
}

// reset clears the call state; gen moves on so stale bodies stop capturing.
func (t *callTap) reset(armed bool) {
	t.armed, t.hasID, t.answered, t.tooLarge = armed, false, false, false
	t.id, t.status, t.buf, t.sse, t.eof, t.result, t.rpcErr = jsonrpc.ID{}, 0, nil, false, false, nil, nil
	t.gen++
}

// take returns what the tap saw during the call and disarms it.
func (t *callTap) take() tapResult {
	if t == nil {
		return tapResult{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.answered && t.hasID && len(t.buf) > 0 {
		t.parse()
	}
	out := tapResult{Answered: t.answered, TooLarge: t.tooLarge, Status: t.status, Result: t.result, RPCError: t.rpcErr}
	t.reset(false)
	return out
}

// sent records the ID of the first tools/call request written while armed;
// the stdio connection calls it before the write.
func (t *callTap) sent(msg jsonrpc.Message) {
	if t == nil {
		return
	}
	req, ok := msg.(*jsonrpc.Request)
	if !ok || req.Method != "tools/call" || !req.IsCall() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.armed && !t.hasID {
		t.id, t.hasID = req.ID, true
	}
}

// received keeps the response to the recorded call.
func (t *callTap) received(msg jsonrpc.Message) {
	if t == nil {
		return
	}
	resp, ok := msg.(*jsonrpc.Response)
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.armed && t.hasID && !t.answered && resp.ID == t.id {
		t.answer(resp)
	}
}

func (t *callTap) answer(resp *jsonrpc.Response) {
	t.answered = true
	if resp.Error != nil {
		var wire *jsonrpc.Error
		if errors.As(resp.Error, &wire) && wire != nil {
			t.rpcErr = &jsonrpc.Error{Code: wire.Code, Message: wire.Message}
		} else {
			t.rpcErr = &jsonrpc.Error{Code: jsonrpc.CodeInternalError}
		}
		return
	}
	t.result = append(json.RawMessage(nil), resp.Result...)
}

// overflow marks a message over the size limit seen during the call.
func (t *callTap) overflow() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.armed {
		t.tooLarge = true
	}
}

// sentBody reports whether an HTTP POST body is the armed tools/call. Each
// matching POST, such as the SDK's resend after OAuth, starts a new capture.
func (t *callTap) sentBody(body []byte) bool {
	if t == nil {
		return false
	}
	msg, err := jsonrpc.DecodeMessage(body)
	if err != nil {
		return false
	}
	req, ok := msg.(*jsonrpc.Request)
	if !ok || req.Method != "tools/call" || !req.IsCall() {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.armed || t.hasID && req.ID != t.id {
		return false
	}
	t.id, t.hasID = req.ID, true
	t.gen++
	t.buf, t.status, t.sse, t.eof, t.answered, t.result, t.rpcErr = nil, 0, false, false, false, nil, nil
	return true
}

// capture tees the body of the call's HTTP response into the tap; a 401 or
// 403 body belongs to the SDK's authorization path and is not captured.
func (t *callTap) capture(rc io.ReadCloser, status int, contentType string) io.ReadCloser {
	if t == nil || status == 401 || status == 403 {
		return rc
	}
	media, _, _ := mime.ParseMediaType(contentType)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status, t.sse, t.eof, t.buf = status, media == "text/event-stream", false, []byte{}
	return &tapBody{ReadCloser: rc, tap: t, gen: t.gen}
}

type tapBody struct {
	io.ReadCloser
	tap *callTap
	gen int
}

func (b *tapBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 || errors.Is(err, io.EOF) {
		b.tap.mu.Lock()
		if b.tap.armed && b.tap.gen == b.gen && !b.tap.answered {
			b.tap.buf = append(b.tap.buf, p[:n]...)
			b.tap.eof = b.tap.eof || errors.Is(err, io.EOF)
		}
		b.tap.mu.Unlock()
	}
	return n, err
}

// parse finds the call's response in the captured HTTP body.
func (t *callTap) parse() {
	payloads := [][]byte{t.buf}
	if t.sse {
		payloads = sseData(t.buf, t.eof)
	}
	for _, data := range payloads {
		msg, err := jsonrpc.DecodeMessage(data)
		if err != nil {
			continue
		}
		if resp, ok := msg.(*jsonrpc.Response); ok && resp.ID == t.id {
			t.answer(resp)
			return
		}
	}
}

// sseData returns the data of each "message" event in b as the SDK's SSE
// reader yields them: lines end at LF (a CR before it is dropped), field
// values are trimmed of spaces, multi-line data joins with newlines, and at
// eof a final event without its blank line still counts. A line without a
// colon ends the stream, as it fails the SDK's reader.
func sseData(b []byte, eof bool) [][]byte {
	var out [][]byte
	var data []byte
	hasData, name := false, ""
	flush := func() {
		if len(data) > 0 && (name == "" || name == "message") {
			out = append(out, data)
		}
		data, hasData, name = nil, false, ""
	}
	for len(b) > 0 {
		line, rest, complete := bytes.Cut(b, []byte("\n"))
		if !complete && !eof {
			break // the SDK has not read this line yet
		}
		b = rest
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			flush()
			continue
		}
		field, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			return out
		}
		switch string(field) {
		case "data":
			if hasData {
				data = append(data, '\n')
			}
			data, hasData = append(data, bytes.TrimSpace(value)...), true
		case "event":
			name = string(bytes.TrimSpace(value))
		}
	}
	if eof {
		flush()
	}
	return out
}

// lineLimitReader fails a stdio line longer than limit bytes before the SDK's
// own frame bound (set to limit+1) can, so the tap can name the overflow.
type lineLimitReader struct {
	r        io.ReadCloser
	limit, n int
	tap      *callTap
}

func (l *lineLimitReader) Close() error { return l.r.Close() }

func (l *lineLimitReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	for chunk := p[:n]; len(chunk) > 0; {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			l.n += len(chunk)
			chunk = nil
		} else {
			l.n += i
			chunk = chunk[i+1:]
		}
		if l.n > l.limit {
			l.tap.overflow()
			return 0, errMessageTooLarge
		}
		if i >= 0 {
			l.n = 0
		}
	}
	return n, err
}

// inspectResult checks a raw result as the IPC reader will and reads its
// isError and resultType flags with exact key matching.
func inspectResult(raw json.RawMessage) (isError, needsInput bool, err error) {
	failure := func() (bool, bool, error) {
		e := output.NewError("protocol_error", nil)
		e.Message = "The server's result is not valid strict JSON or is nested too deeply."
		return false, false, e
	}
	if jsonutil.Check(raw, maxResultDepth) != nil {
		return failure()
	}
	if trimmed := bytes.TrimLeft(raw, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		return failure()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return failure()
	}
	if v, has := fields["isError"]; has {
		switch string(bytes.TrimSpace(v)) {
		case "true":
			isError = true
		case "false":
		default:
			return failure()
		}
	}
	var resultType string
	if v, has := fields["resultType"]; has && json.Unmarshal(v, &resultType) == nil {
		needsInput = resultType == "input_required"
	}
	return isError, needsInput, nil
}
