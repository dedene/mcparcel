package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/jsonutil"
	"github.com/dedene/mcparcel/internal/output"
)

type ConnectOptions struct {
	Connection      config.Connection
	Env             map[string]string
	Headers         map[string]string
	Home            string
	Version         string
	ConnectTimeout  time.Duration
	ShutdownTimeout time.Duration
	// OAuth authorizes HTTP requests; leave it a nil interface when unused.
	OAuth sdkauth.OAuthHandler
}
type (
	Result struct {
		JSON       json.RawMessage
		IsError    bool
		NeedsInput bool
		Dispatched bool
		// Declined is the sanitized message of an elicitation declined during
		// the call, empty when there was none.
		Declined string
	}
	Session interface {
		Tools(context.Context) ([]json.RawMessage, error)
		Call(ctx context.Context, tool string, arguments, meta map[string]any, beforeDispatch func() error) (Result, error)
		Close(context.Context) error
	}
)

type session struct {
	closeOnce sync.Once
	closeDone chan struct{}
	sdk       *mcp.ClientSession
	cleanup   func(context.Context)
	status    func() error
	timeout   time.Duration
	mu        sync.Mutex
	declined  string
}

func Connect(ctx context.Context, opts ConnectOptions) (Session, error) {
	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = 30 * time.Second
	}
	if opts.ShutdownTimeout == 0 {
		opts.ShutdownTimeout = 5 * time.Second
	}
	if ctx.Err() != nil {
		return nil, contextError(ctx.Err(), false)
	}
	transport, cleanup, status, err := makeTransport(opts)
	if err != nil {
		return nil, err
	}
	s := &session{cleanup: cleanup, status: status, timeout: opts.ConnectTimeout, closeDone: make(chan struct{})}
	client := mcp.NewClient(&mcp.Implementation{Name: "MCParcel", Version: opts.Version}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}})
	client.AddReceivingMiddleware(s.declineElicitation)
	client.AddSendingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(c context.Context, method string, req mcp.Request) (mcp.Result, error) {
			r, e := next(c, method, req)
			if v, ok := r.(*mcp.ListToolsResult); ok && method == "tools/list" {
				v.TTLMs = 0
			}
			return r, e
		}
	})
	c, cancel := context.WithTimeout(ctx, opts.ConnectTimeout)
	defer cancel()
	// A strict stdio server may ignore or exit on the server/discover probe.
	var session *mcp.ClientSessionOptions
	if opts.Connection.Transport.Stdio != nil {
		session = &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"}
	}
	sdk, err := client.Connect(c, transport, session)
	if err != nil {
		cleanup(context.Background())
		if failure := authFailure(err, status); failure != nil {
			return nil, failure
		}
		if c.Err() != nil {
			return nil, contextError(c.Err(), false)
		}
		if status() != nil {
			return nil, status()
		}
		return nil, output.NewError("connection_failed", nil)
	}
	s.sdk = sdk
	return s, nil
}

// declineElicitation answers every elicitation/create with decline, before the
// SDK validates it: a consent request is never accepted on the user's behalf.
func (s *session) declineElicitation(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "elicitation/create" {
			return next(ctx, method, req)
		}
		text := ""
		if r, ok := req.(*mcp.ElicitRequest); ok && r.Params != nil {
			text = r.Params.Message
		}
		s.mu.Lock()
		if s.declined == "" {
			s.declined = sanitizeMessage(text)
		}
		s.mu.Unlock()
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
}

// sanitizeMessage makes untrusted server text one line of at most 300 runes
// without control or format characters.
func sanitizeMessage(text string) string {
	text = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, text)), " ")
	if text == "" {
		return "(no message)"
	}
	if runes := []rune(text); len(runes) > 300 {
		text = string(runes[:300]) + "..."
	}
	return text
}

// authFailure finds a sign-in error in err's chain, else the transport's
// auth_required after a 401; nil when neither applies.
func authFailure(err error, status func() error) error {
	var e *output.Error
	if errors.As(err, &e) && e != nil {
		switch e.Code {
		case "auth_required", "auth_expired", "auth_failed", "keychain_unavailable", "auth_callback_unavailable", "invalid_arguments":
			return e
		}
	}
	if failure := status(); errors.As(failure, &e) && e != nil && e.Code == "auth_required" {
		return e
	}
	return nil
}

func contextError(err error, dispatched bool) error {
	if errors.Is(err, context.Canceled) {
		return output.NewError("canceled", nil)
	}
	if dispatched {
		return output.NewError("outcome_unknown", nil)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return output.NewError("timeout", nil)
	}
	return output.NewError("connection_failed", nil)
}

func (s *session) Tools(ctx context.Context) ([]json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	items := make([]json.RawMessage, 0)
	names := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	for range 1000 {
		r, err := s.sdk.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			if failure := authFailure(err, s.status); failure != nil {
				return nil, failure
			}
			if ctx.Err() != nil {
				return nil, contextError(ctx.Err(), false)
			}
			return nil, output.NewError("protocol_error", nil)
		}
		for _, tool := range r.Tools {
			if tool == nil || tool.Name == "" || names[tool.Name] {
				return nil, output.NewError("protocol_error", nil)
			}
			schema, e := json.Marshal(tool.InputSchema)
			if e != nil {
				return nil, output.NewError("invalid_schema", nil)
			}
			v, e := jsonutil.Decode(schema)
			if e != nil {
				return nil, output.NewError("invalid_schema", nil)
			}
			if _, ok := v.(map[string]any); !ok {
				return nil, output.NewError("invalid_schema", nil)
			}
			raw, e := json.Marshal(tool)
			if e != nil {
				return nil, output.NewError("protocol_error", nil)
			}
			names[tool.Name] = true
			items = append(items, raw)
		}
		if r.NextCursor == "" {
			sort.Slice(items, func(i, j int) bool {
				var a, b struct{ Name string }
				_ = json.Unmarshal(items[i], &a)
				_ = json.Unmarshal(items[j], &b)
				return a.Name < b.Name
			})
			return items, nil
		}
		if cursors[r.NextCursor] {
			return nil, output.NewError("protocol_error", nil)
		}
		cursors[r.NextCursor] = true
		cursor = r.NextCursor
	}
	return nil, output.NewError("protocol_error", nil)
}

func (s *session) Call(ctx context.Context, tool string, arguments, meta map[string]any, beforeDispatch func() error) (Result, error) {
	var out Result
	if ctx.Err() != nil {
		return out, contextError(ctx.Err(), false)
	}
	if beforeDispatch != nil {
		if err := beforeDispatch(); err != nil {
			var safe *output.Error
			if errors.As(err, &safe) && safe != nil {
				return out, safe
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return out, contextError(err, false)
			}
			return out, output.NewError("internal_error", nil)
		}
	}
	if ctx.Err() != nil {
		return out, contextError(ctx.Err(), false)
	}
	out.Dispatched = true
	s.mu.Lock()
	s.declined = ""
	s.mu.Unlock()
	r, err := s.sdk.CallTool(auth.WithToolCall(ctx), &mcp.CallToolParams{Meta: meta, Name: tool, Arguments: arguments})
	s.mu.Lock()
	out.Declined = s.declined
	s.mu.Unlock()
	if err != nil {
		if failure := authFailure(err, s.status); failure != nil {
			return out, failure
		}
		if ctx.Err() != nil {
			return out, contextError(ctx.Err(), true)
		}
		var wire *jsonrpc.Error
		if out.Declined != "" && errors.As(err, &wire) {
			return out, output.ElicitationDeclined(out.Declined)
		}
		// SDK WireErrors can also originate locally, without a response-origin marker.
		return out, contextError(err, true)
	}
	out.JSON, err = json.Marshal(r)
	if err != nil {
		return out, output.NewError("protocol_error", nil)
	}
	out.IsError = r.IsError
	out.NeedsInput = r.NeedsInput()
	if out.NeedsInput {
		return out, output.NewError("input_required", nil)
	}
	return out, nil
}

func (s *session) Close(ctx context.Context) error {
	s.closeOnce.Do(func() { go func() { s.cleanup(ctx); _ = s.sdk.Close(); s.cleanup(ctx); close(s.closeDone) }() })
	select {
	case <-s.closeDone:
		return nil
	case <-ctx.Done():
		s.cleanup(ctx)
		return contextError(ctx.Err(), false)
	}
}
