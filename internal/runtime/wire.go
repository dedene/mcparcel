package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

const (
	ProtocolVersion = 1
	MaxFrameBytes   = 16 * 1024 * 1024
)

var (
	ErrFrameTooLarge = errors.New("IPC frame exceeds limit")
	ErrInvalidFrame  = errors.New("invalid IPC frame")
)

type Frame struct {
	ProtocolVersion int             `json:"protocolVersion"`
	Kind            string          `json:"kind"`
	RequestID       string          `json:"requestId"`
	Body            json.RawMessage `json:"body"`
}
type Hello struct {
	ConfigRoot    string `json:"configRoot,omitempty"`
	BinaryVersion string `json:"binaryVersion"`
	Intent        string `json:"intent"`
}
type HelloAck struct {
	BinaryVersion string        `json:"binaryVersion"`
	PID           int           `json:"pid"`
	Compatible    bool          `json:"compatible"`
	Error         *output.Error `json:"error"`
}
type Request struct {
	Method     string   `json:"method"`
	Connection string   `json:"connection,omitempty"`
	Tool       string   `json:"tool,omitempty"`
	Arguments  args.Raw `json:"arguments"`
	Cached     bool     `json:"cached,omitempty"`
	Timeout    string   `json:"timeout,omitempty"`
	NoInput    bool     `json:"noInput,omitempty"`
	Force      bool     `json:"force,omitempty"`
	// Meta is a call's _meta object, validated by args.ParseMeta.
	Meta json.RawMessage `json:"meta,omitempty"`
}

// AuthURL streams a sign-in's authorization URL to the waiting CLI.
type AuthURL struct {
	URL string `json:"url"`
}
type Response struct {
	Data       json.RawMessage `json:"data"`
	Error      *output.Error   `json:"error"`
	Dispatched bool            `json:"dispatched"`
}
type Handler interface {
	Handle(ctx context.Context, requestID string, req Request,
		beforeDispatch func() error) Response
	Active() int
	Shutdown(ctx context.Context, force bool) error
}

func validateRequest(intent string, r Request) error {
	if r.Arguments.Values == nil {
		return ErrInvalidFrame
	}
	if r.Meta != nil && r.Method != "call" {
		return ErrInvalidFrame
	}
	if (intent == "restart" || intent == "stop") && r.Method != intent || intent == "status" && r.Method != "status" || intent == "work" && r.Method != "tools" && r.Method != "call" && r.Method != "login" && r.Method != "logout" {
		return ErrInvalidFrame
	}
	switch r.Method {
	case "call":
		if r.Tool == "" || r.Cached || r.Force {
			return ErrInvalidFrame
		}
		if r.Meta != nil {
			if _, e := args.ParseMeta(r.Meta); e != nil {
				return ErrInvalidFrame
			}
		}
		if r.Timeout != "" {
			d, e := time.ParseDuration(r.Timeout)
			if e != nil || d <= 0 {
				return ErrInvalidFrame
			}
		}
	case "tools":
		if r.Tool != "" || r.Timeout != "" || len(r.Arguments.Values) != 0 || r.Force {
			return ErrInvalidFrame
		}
	case "login", "logout":
		if r.Connection == "" || r.Tool != "" || r.Timeout != "" || len(r.Arguments.Values) != 0 || r.Cached || r.Force {
			return ErrInvalidFrame
		}
		if r.Method == "logout" && config.ValidateCanonicalID(r.Connection) != nil {
			return ErrInvalidFrame
		}
	case "status", "restart", "stop":
		if r.Connection != "" || r.Tool != "" || r.Timeout != "" || len(r.Arguments.Values) != 0 || r.Cached || r.Method == "status" && r.Force {
			return ErrInvalidFrame
		}
	default:
		return ErrInvalidFrame
	}
	return nil
}

func validateControl(f Frame, id string) error {
	if f.ProtocolVersion != ProtocolVersion || f.RequestID != id || f.Kind != "cancel" {
		return ErrInvalidFrame
	}
	return nil
}

func validateBody(f Frame) error {
	switch f.Kind {
	case "hello":
		var v Hello
		return decodeBody(f.Body, &v)
	case "hello_ack":
		var v HelloAck
		return decodeBody(f.Body, &v)
	case "request":
		var v Request
		if e := decodeBody(f.Body, &v); e != nil {
			return e
		}
		intent := "work"
		if v.Method == "status" || v.Method == "restart" || v.Method == "stop" {
			intent = v.Method
		}
		return validateRequest(intent, v)
	case "response":
		var v Response
		return decodeBody(f.Body, &v)
	case "auth_url":
		var v AuthURL
		if e := decodeBody(f.Body, &v); e != nil {
			return e
		}
		if v.URL == "" {
			return ErrInvalidFrame
		}
		return nil
	case "dispatch", "cancel":
		var v struct{}
		return decodeBody(f.Body, &v)
	default:
		return ErrInvalidFrame
	}
}
