package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"time"

	"github.com/dedene/mcparcel/internal/jsonutil"
	"github.com/dedene/mcparcel/internal/output"
)

// ReadFrame reads one frame of at most MaxFrameBytes, as the daemon does.
func ReadFrame(r io.Reader) (Frame, error) { return readFrame(r, MaxFrameBytes) }

// readFrame reads one strict-JSON frame of at most limit bytes; only the CLI
// reads response frames up to MaxResponseFrameBytes.
func readFrame(r io.Reader, limit uint32) (Frame, error) {
	var f Frame
	var header [4]byte
	n, err := io.ReadFull(r, header[:])
	if err != nil {
		if n == 0 && errors.Is(err, io.EOF) {
			return f, io.EOF
		}
		return f, ErrInvalidFrame
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		return f, ErrInvalidFrame
	}
	if size > limit {
		return f, ErrFrameTooLarge
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r, payload); err != nil {
		return f, ErrInvalidFrame
	}
	// One pass checks the whole payload, body included, without building it.
	if err := jsonutil.Check(payload, 128); err != nil {
		return f, ErrInvalidFrame
	}
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return Frame{}, ErrInvalidFrame
	}
	if !validID(f.RequestID) || len(f.Body) == 0 || f.Kind == "" {
		return Frame{}, ErrInvalidFrame
	}
	if body := bytes.TrimLeft(f.Body, " \t\r\n"); len(body) == 0 || body[0] != '{' {
		return Frame{}, ErrInvalidFrame
	}
	if err := validateBody(f); err != nil {
		return Frame{}, err
	}
	return f, nil
}

func WriteFrame(w io.Writer, frame Frame) error {
	buf, err := encodeFrame(frame)
	if err != nil {
		return err
	}
	return writeEncoded(w, buf)
}

// encodeFrame returns the frame's length-prefixed bytes.
func encodeFrame(frame Frame) ([]byte, error) {
	payload, err := json.Marshal(frame)
	if err != nil {
		return nil, ErrInvalidFrame
	}
	if len(payload) == 0 || len(payload) > MaxResponseFrameBytes {
		return nil, ErrFrameTooLarge
	}
	buf := make([]byte, 4, 4+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload)))
	return append(buf, payload...), nil
}

func writeEncoded(w io.Writer, buf []byte) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(buf) {
			return io.ErrShortWrite
		}
		buf = buf[n:]
	}
	return nil
}

func decodeBody(raw json.RawMessage, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return ErrInvalidFrame
	}
	return nil // ReadFrame already checked duplicates and trailing JSON.
}

func ClientHandshake(conn *net.UnixConn, version, intent, id, root string) (HelloAck, error) {
	var ack HelloAck
	if err := CheckPeer(conn); err != nil {
		return ack, err
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	if intent == "restart" || intent == "stop" {
		root = ""
	}
	body, _ := json.Marshal(Hello{BinaryVersion: version, Intent: intent, ConfigRoot: root})
	if err := WriteFrame(conn, Frame{ProtocolVersion, "hello", id, body}); err != nil {
		return ack, err
	}
	f, err := readFrame(conn, MaxResponseFrameBytes)
	if err != nil {
		return ack, err
	}
	if f.ProtocolVersion != ProtocolVersion || f.Kind != "hello_ack" || f.RequestID != id {
		return ack, output.NewError("runtime_version_mismatch", nil)
	}
	if err := decodeBody(f.Body, &ack); err != nil {
		return ack, err
	}
	if ack.Error != nil {
		return ack, ack.Error
	}
	if !ack.Compatible || (ack.BinaryVersion != version && intent != "restart" && intent != "stop") {
		return ack, output.NewError("runtime_version_mismatch", nil)
	}
	return ack, nil
}

func ServerHandshake(conn *net.UnixConn, version string, pid int, root string) (Hello, string, error) {
	var hello Hello
	if err := CheckPeer(conn); err != nil {
		return hello, "", err
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	f, err := ReadFrame(conn)
	if err != nil {
		return hello, "", err
	}
	if f.Kind != "hello" {
		return hello, f.RequestID, ErrInvalidFrame
	}
	if err := decodeBody(f.Body, &hello); err != nil {
		return hello, f.RequestID, err
	}
	validIntent := hello.Intent == "work" || hello.Intent == "status" || (hello.Intent == "restart" || hello.Intent == "stop")
	compatible := validIntent && f.ProtocolVersion == ProtocolVersion &&
		(hello.BinaryVersion == version || (hello.Intent == "restart" || hello.Intent == "stop"))
	ack := HelloAck{BinaryVersion: version, PID: pid, Compatible: compatible}
	if !compatible {
		ack.Error = output.NewError("runtime_version_mismatch", nil)
	}
	if compatible && hello.Intent != "restart" && hello.Intent != "stop" && hello.ConfigRoot != root {
		ack.Compatible = false
		ack.Error = output.NewError("runtime_config_mismatch", nil)
	}
	body, _ := json.Marshal(ack)
	if err := WriteFrame(conn, Frame{ProtocolVersion, "hello_ack", f.RequestID, body}); err != nil {
		return hello, f.RequestID, err
	}
	if ack.Error != nil {
		return hello, f.RequestID, ack.Error
	}
	return hello, f.RequestID, nil
}

func configRoot(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(absolute))
	return hex.EncodeToString(sum[:])
}
