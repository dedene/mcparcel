package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/elicit"
	"github.com/dedene/mcparcel/internal/output"
)

const (
	ProtocolVersion = 1
	// MaxFrameBytes bounds every frame the daemon reads.
	MaxFrameBytes = 16 << 20
	// MaxResponseFrameBytes bounds the frames the CLI reads, so a 16 MiB MCP
	// result still fits after encoding/json escapes it.
	MaxResponseFrameBytes = 64 << 20
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
	// Prompt is how a call's user can answer an elicitation: "terminal",
	// "dialog" (no forms), or "" when nobody can.
	Prompt string `json:"prompt,omitempty"`
}

// AuthURL streams a sign-in's authorization URL to the waiting CLI.
type AuthURL struct {
	URL string `json:"url"`
}

// Elicit forwards an elicitation of the in-flight call to its CLI.
type Elicit struct {
	PromptID string `json:"promptId"`
	elicit.Prompt
}

// ElicitAnswer is the CLI's answer to the Elicit with the same PromptID.
type ElicitAnswer struct {
	PromptID string `json:"promptId"`
	elicit.Answer
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
	if r.Prompt != "" && (r.Method != "call" || r.NoInput || r.Prompt != "terminal" && r.Prompt != "dialog") {
		return ErrInvalidFrame
	}
	if (intent == "restart" || intent == "stop") && r.Method != intent || intent == "status" && r.Method != "status" || intent == "work" && r.Method != "tools" && r.Method != "call" && r.Method != "auth" && r.Method != "logout" && r.Method != "lock" {
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
	case "auth", "logout":
		if r.Connection == "" || r.Tool != "" || r.Timeout != "" || len(r.Arguments.Values) != 0 || r.Cached || r.Force {
			return ErrInvalidFrame
		}
		if r.Method == "logout" && config.ValidateCanonicalID(r.Connection) != nil {
			return ErrInvalidFrame
		}
	case "lock":
		if r.Connection != "" || r.Tool != "" || r.Timeout != "" || len(r.Arguments.Values) != 0 || r.Cached || r.Force {
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

func validateControl(f Frame, id, kind string) error {
	if f.ProtocolVersion != ProtocolVersion || f.RequestID != id || f.Kind != kind {
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
	case "elicit":
		var v Elicit
		if len(f.Body) > elicit.MaxBody || decodeBody(f.Body, &v) != nil || !validID(v.PromptID) || v.Valid() != nil {
			return ErrInvalidFrame
		}
		return nil
	case "elicit_answer":
		var v ElicitAnswer
		if len(f.Body) > elicit.MaxBody || decodeBody(f.Body, &v) != nil || !validID(v.PromptID) || v.Valid() != nil {
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

// validID reports whether id is 32 lowercase hex characters.
func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
