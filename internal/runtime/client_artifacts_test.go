package runtime

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"

	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

// TestCallRejectsDaemonArtifacts: only the CLI writes files, so a daemon
// response naming artifacts is a protocol violation, not a list to trust.
func TestCallRejectsDaemonArtifacts(t *testing.T) {
	for name, artifacts := range map[string]string{"non-empty": `[{"index":0,"type":"image","mimeType":"image/png","path":"/etc/passwd","bytes":1}]`, "empty": `[]`} {
		t.Run(name, func(t *testing.T) {
			p, _ := testutil.IsolatedPaths(t)
			l, e := net.ListenUnix("unix", &net.UnixAddr{Name: p.SocketFile, Net: "unix"})
			if e != nil {
				t.Fatal(e)
			}
			_ = os.Chmod(p.SocketFile, 0o600)
			defer func() { _ = l.Close() }()
			done := make(chan struct{})
			go func() {
				defer close(done)
				// The first connection is Ensure's handshake probe.
				for range 2 {
					conn, e := l.AcceptUnix()
					if e != nil {
						return
					}
					if _, _, e = ServerHandshake(conn, "dev", 1, configRoot(p.ConfigDir)); e == nil {
						if f, e := ReadFrame(conn); e == nil && f.Kind == "request" {
							data := json.RawMessage(`{"connection":"local:fixture","tool":"wait","result":{"content":[]},"artifacts":` + artifacts + `}`)
							body, _ := json.Marshal(Response{Data: data, Dispatched: true})
							_ = WriteFrame(conn, Frame{ProtocolVersion, "dispatch", f.RequestID, json.RawMessage(`{}`)})
							_ = WriteFrame(conn, Frame{ProtocolVersion, "response", f.RequestID, body})
							_, _ = ReadFrame(conn)
						}
					}
					_ = conn.Close()
				}
			}()
			r, e := (&Client{Paths: p, Version: "dev"}).Call(testCtx(t), callReq())
			defer func() { <-done }()
			if name == "empty" {
				if e != nil || len(r.Data.Artifacts) != 0 || r.Data.Tool != "wait" {
					t.Fatal(r, e)
				}
				return
			}
			wantCode(t, e, "protocol_error")
			var oe *output.Error
			_ = errors.As(e, &oe)
			if oe.Details == nil || !oe.Details.Dispatched || oe.Details.Outcome != "unknown" || len(oe.Details.RequestID) != 32 {
				t.Fatal(oe.Details)
			}
			if len(r.Data.Artifacts) != 0 || len(r.Data.Result) != 0 || !r.Dispatched {
				t.Fatal("daemon artifacts reached the caller", r)
			}
		})
	}
}
