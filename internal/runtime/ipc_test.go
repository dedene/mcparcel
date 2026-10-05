package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

const testID = "0123456789abcdef0123456789abcdef"

func frame(kind string, v any) Frame {
	b, _ := json.Marshal(v)
	return Frame{ProtocolVersion, kind, testID, b}
}

func rawFrame(s string) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(s)))
	b.WriteString(s)
	return b.Bytes()
}

type byteIO struct{ bytes.Buffer }

func (b *byteIO) Write(p []byte) (int, error) { return b.Buffer.Write(p[:1]) }
func (b *byteIO) Read(p []byte) (int, error)  { return b.Buffer.Read(p[:1]) }
func TestFrameFragmentation(t *testing.T) {
	b := new(byteIO)
	f := frame("dispatch", map[string]any{})
	for range 2 {
		if e := WriteFrame(b, f); e != nil {
			t.Fatal(e)
		}
	}
	for range 2 {
		g, e := ReadFrame(b)
		if e != nil || string(g.Body) != "{}" || g.RequestID != testID {
			t.Fatalf("%+v %v", g, e)
		}
	}
}

func TestFrameShortAndOversized(t *testing.T) {
	for _, tc := range []struct {
		b []byte
		e error
	}{{nil, io.EOF}, {[]byte{0, 1}, ErrInvalidFrame}, {[]byte{0, 0, 0, 2, '{'}, ErrInvalidFrame}, {[]byte{0, 0, 0, 0}, ErrInvalidFrame}, {[]byte{1, 0, 0, 1}, ErrFrameTooLarge}} {
		_, e := ReadFrame(bytes.NewReader(tc.b))
		if !errors.Is(e, tc.e) {
			t.Fatalf("%v want %v", e, tc.e)
		}
	}
}

func TestFrameStrictJSON(t *testing.T) {
	for _, s := range []string{`{"kind":"dispatch","kind":"cancel"}`, `{"protocolVersion":1,"kind":"dispatch","requestId":"` + testID + `","body":{},"secret":"sentinel"}`, `{"protocolVersion":1,"kind":"dispatch","requestId":"` + testID + `","body":[]}`, `{"protocolVersion":1,"kind":"dispatch","requestId":"bad","body":{}}`, `{"protocolVersion":1,"kind":"request","requestId":"` + testID + `","body":{"method":"unknown","arguments":{"values":{}}}}`} {
		_, e := ReadFrame(bytes.NewReader(rawFrame(s)))
		if !errors.Is(e, ErrInvalidFrame) || strings.Contains(e.Error(), "sentinel") {
			t.Fatal(e)
		}
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }
func TestFrameShortWrite(t *testing.T) {
	if e := WriteFrame(zeroWriter{}, frame("dispatch", map[string]any{})); !errors.Is(e, io.ErrShortWrite) {
		t.Fatal(e)
	}
}

func unixPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	p, _ := testutil.IsolatedPaths(t)
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: p.SocketFile, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = l.Close() })
	ch := make(chan *net.UnixConn, 1)
	go func() { c, _ := l.AcceptUnix(); ch <- c }()
	c, e := net.DialUnix("unix", nil, l.Addr().(*net.UnixAddr))
	if e != nil {
		t.Fatal(e)
	}
	s := <-ch
	t.Cleanup(func() { _ = c.Close(); _ = s.Close() })
	return c, s
}

func handshake(t *testing.T, intent, version string) (HelloAck, error) {
	t.Helper()
	c, s := unixPair(t)
	done := make(chan error, 1)
	go func() { _, _, e := ServerHandshake(s, "old", os.Getpid(), ""); done <- e }()
	a, e := ClientHandshake(c, version, intent, testID, "")
	<-done
	return a, e
}

func wantCode(t *testing.T, e error, code string) {
	t.Helper()
	var v *output.Error
	if !errors.As(e, &v) || v.Code != code {
		t.Fatalf("%v want %s", e, code)
	}
}

func TestHandshakeSameVersion(t *testing.T) {
	a, e := handshake(t, "work", "old")
	if e != nil || !a.Compatible || a.PID != os.Getpid() || a.Error != nil {
		t.Fatalf("%+v %v", a, e)
	}
}

func TestHandshakeVersionMismatch(t *testing.T) {
	for _, intent := range []string{"work", "status"} {
		_, e := handshake(t, intent, "new")
		wantCode(t, e, "runtime_version_mismatch")
		if output.ExitCode(e) != 6 {
			t.Fatal(e)
		}
	}
}

func TestHandshakeRestartException(t *testing.T) {
	_, e := handshake(t, "restart", "new")
	if e != nil {
		t.Fatal(e)
	}
	if e = validateRequest("restart", Request{Method: "call", Tool: "x", Arguments: args.Raw{Values: map[string]args.Value{}}}); e == nil {
		t.Fatal("restart admitted call")
	}
}

func TestHandshakeProtocolMismatch(t *testing.T) {
	c, s := unixPair(t)
	done := make(chan error, 1)
	go func() { _, _, e := ServerHandshake(s, "old", 1, ""); done <- e }()
	f := frame("hello", Hello{BinaryVersion: "new", Intent: "restart"})
	f.ProtocolVersion = 2
	if e := WriteFrame(c, f); e != nil {
		t.Fatal(e)
	}
	a, e := ReadFrame(c)
	if e != nil {
		t.Fatal(e)
	}
	var ack HelloAck
	_ = json.Unmarshal(a.Body, &ack)
	wantCode(t, ack.Error, "runtime_version_mismatch")
	wantCode(t, <-done, "runtime_version_mismatch")
}

func TestPeerUID(t *testing.T) {
	c, _ := unixPair(t)
	if e := CheckPeer(c); e != nil {
		t.Fatal(e)
	}
	if e := peerUID(uint32(os.Getuid() + 1)); !errors.Is(e, config.ErrUnsafePath) {
		t.Fatal(e)
	}
}

func TestRequestSequenceAndCancellation(t *testing.T) {
	h := &daemonHandler{started: make(chan struct{}, 3), release: make(chan struct{})}
	c, _ := service(t, h, 0)
	stable := make(chan error, 1)
	go func() { _, e := c.Call(testCtx(t), callReq()); stable <- e }()
	<-h.started
	dial := func() *net.UnixConn {
		conn, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: c.Paths.SocketFile, Net: "unix"})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if _, e = ClientHandshake(conn, "dev", "work", testID, configRoot(c.Paths.ConfigDir)); e != nil {
			t.Fatal(e)
		}
		return conn
	}
	req := Request{Method: "call", Connection: "fixture", Tool: "wait", Arguments: emptyArgs()}
	bad := dial()
	f := frame("request", req)
	f.RequestID = strings.Repeat("a", 32)
	_ = WriteFrame(bad, f)
	got, e := ReadFrame(bad)
	if e != nil {
		t.Fatal(e)
	}
	var res Response
	_ = decodeBody(got.Body, &res)
	responseCode(t, res, "protocol_error", false)
	second := dial()
	_ = WriteFrame(second, frame("request", req))
	got, e = ReadFrame(second)
	if e != nil || got.Kind != "dispatch" {
		t.Fatal(got, e)
	}
	<-h.started
	_ = WriteFrame(second, frame("request", req))
	got, e = ReadFrame(second)
	if e != nil {
		t.Fatal(e)
	}
	_ = decodeBody(got.Body, &res)
	responseCode(t, res, "protocol_error", true)
	if res.Error.Details == nil || res.Error.Details.RequestID != testID || !res.Error.Details.Dispatched || res.Error.Details.Outcome != "unknown" {
		t.Fatal("lost post-dispatch uncertainty", res.Error)
	}
	ctx, cancel := context.WithCancel(testCtx(t))
	cancelled := make(chan error, 1)
	go func() { _, e := c.Call(ctx, callReq()); cancelled <- e }()
	<-h.started
	cancel()
	wantCode(t, <-cancelled, "canceled")
	select {
	case e := <-stable:
		t.Fatal("unrelated handler canceled", e)
	default:
	}
	close(h.release)
	if e := <-stable; e != nil {
		t.Fatal(e)
	}
}

func TestRawArgumentsWireRoundTrip(t *testing.T) {
	r := Request{Method: "call", Tool: "x", Arguments: args.Raw{Values: map[string]args.Value{"n": {JSON: json.RawMessage(`9007199254740993`)}, "s": {Text: "007"}}}}
	var b bytes.Buffer
	_ = WriteFrame(&b, frame("request", r))
	f, e := ReadFrame(&b)
	if e != nil {
		t.Fatal(e)
	}
	var got Request
	if e = decodeBody(f.Body, &got); e != nil {
		t.Fatal(e)
	}
	if string(got.Arguments.Values["n"].JSON) != "9007199254740993" || got.Arguments.Values["s"].Text != "007" {
		t.Fatal(got)
	}
}

func TestHandshakeAckProtocolMismatch(t *testing.T) {
	c, s := unixPair(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ReadFrame(s)
		f := frame("hello_ack", HelloAck{BinaryVersion: "dev", PID: 1, Compatible: true})
		f.ProtocolVersion = 2
		_ = WriteFrame(s, f)
	}()
	_, e := ClientHandshake(c, "dev", "restart", testID, "")
	<-done
	wantCode(t, e, "runtime_version_mismatch")
}

func TestHandshakeConfigRootAndShutdownExemptions(t *testing.T) {
	for _, intent := range []string{"work", "status", "restart", "stop"} {
		t.Run(intent, func(t *testing.T) {
			c, s := unixPair(t)
			done := make(chan error, 1)
			go func() { _, _, e := ServerHandshake(s, "dev", 1, configRoot("/config/a")); done <- e }()
			version := "dev"
			if intent == "restart" || intent == "stop" {
				version = "other"
			}
			_, err := ClientHandshake(c, version, intent, testID, configRoot("/config/b"))
			serverErr := <-done
			if intent == "work" || intent == "status" {
				wantCode(t, err, "runtime_config_mismatch")
				wantCode(t, serverErr, "runtime_config_mismatch")
				if output.ExitCode(err) != 6 {
					t.Fatal("incorrect exit")
				}
			} else if err != nil || serverErr != nil {
				t.Fatal(err, serverErr)
			}
		})
	}
}

func TestRestartHandshakeRetainsLegacyShape(t *testing.T) {
	c, s := unixPair(t)
	done := make(chan error, 1)
	go func() {
		defer s.Close()
		f, e := ReadFrame(s)
		if e != nil {
			done <- e
			return
		}
		var legacy struct {
			BinaryVersion string `json:"binaryVersion"`
			Intent        string `json:"intent"`
		}
		if e = decodeBody(f.Body, &legacy); e != nil {
			done <- e
			return
		}
		e = WriteFrame(s, requestFrame("hello_ack", f.RequestID, HelloAck{BinaryVersion: "old", Compatible: true, PID: os.Getpid()}))
		done <- e
	}()
	_, err := ClientHandshake(c, "new", "restart", testID, configRoot("/new/config"))
	serverErr := <-done
	if err != nil || serverErr != nil {
		t.Fatal("legacy restart handshake failed", err, serverErr)
	}
}

func TestRestartResponseRetainsLegacyShape(t *testing.T) {
	c, _ := service(t, &daemonHandler{}, 0)
	r, _, e := c.exchange(testCtx(t), "restart", Request{Method: "restart", Arguments: emptyArgs()}, false)
	if e != nil {
		t.Fatal(e)
	}
	var legacy struct {
		Stopped bool `json:"stopped"`
	}
	if e := decodeBody(r.Data, &legacy); e != nil || !legacy.Stopped {
		t.Fatal("legacy restart response failed", string(r.Data), e)
	}
}
