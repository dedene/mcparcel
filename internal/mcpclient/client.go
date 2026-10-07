package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

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
	// MaxMessageBytes bounds one MCP message (a stdio line, or an HTTP
	// response with its streamed events); 0 means 16 MiB.
	MaxMessageBytes int
}
type (
	Result struct {
		JSON       json.RawMessage
		IsError    bool
		NeedsInput bool
		Dispatched bool
		// Declined is the sanitized message of the first elicitation not
		// accepted during the call, empty when there was none; DeclineReason
		// says why (see output.ElicitationDeclined).
		Declined      string
		DeclineReason string
		// Retire means the session must not be reused.
		Retire bool
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
	reason    string
	prompter  *Prompter
	tap       *callTap
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
	if opts.MaxMessageBytes <= 0 {
		opts.MaxMessageBytes = defaultMaxMessageBytes
	}
	tap := &callTap{}
	transport, cleanup, status, diagnose, err := makeTransport(opts, tap)
	if err != nil {
		return nil, err
	}
	s := &session{cleanup: cleanup, status: status, timeout: opts.ConnectTimeout, closeDone: make(chan struct{}), tap: tap}
	client := mcp.NewClient(&mcp.Implementation{Name: "MCParcel", Version: opts.Version}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}})
	client.AddReceivingMiddleware(s.elicitation)
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
		defer cleanup(context.Background())
		if abandonedAuth(c, err, status) {
			return nil, contextError(c.Err(), false)
		}
		if failure := authFailure(err, status); failure != nil {
			return nil, failure
		}
		if c.Err() != nil {
			return nil, contextError(c.Err(), false)
		}
		failure := status()
		if failure == nil && diagnose != nil {
			// Before cleanup, so cleanup also drops the probe's connection.
			failure = diagnose(c)
		}
		if failure != nil {
			return nil, failure
		}
		return nil, output.NewError("connection_failed", nil)
	}
	s.sdk = sdk
	return s, nil
}

// authFailure finds a sign-in error in err's chain, else the transport's
// auth_required after a 401; nil when neither applies. A client_credentials
// failure that kept the request from being sent comes first, also when it
// is a token endpoint outage (connection_failed) or a canceled wait: the
// transport's 401 flag would otherwise turn it into auth_required.
func authFailure(err error, status func() error) error {
	var e *output.Error
	var unsent *auth.UnsentError
	if errors.As(err, &unsent) {
		if errors.As(unsent.Err, &e) && e != nil {
			return e
		}
		return contextError(unsent.Err, false)
	}
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

// abandonedAuth reports whether ctx ended while the OAuth handler was
// answering the server's 401, so the request was never resent and did not
// run (D7). The SDK then returns the bare context error, without the
// handler's auth.UnsentError, and the transport still reports the 401 as
// auth_required, which must not become token_rejected. A non-OAuth 401
// carries its own auth_required in err and is not matched.
func abandonedAuth(ctx context.Context, err error, status func() error) bool {
	var e *output.Error
	if ctx.Err() == nil || errors.As(err, &e) {
		return false
	}
	return errors.As(status(), &e) && e != nil && e.Code == "auth_required"
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
			if abandonedAuth(ctx, err, s.status) {
				return nil, contextError(ctx.Err(), false)
			}
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
	s.declined, s.reason, s.prompter = "", "", PrompterFrom(ctx)
	s.mu.Unlock()
	s.tap.arm()
	r, err := s.sdk.CallTool(auth.WithToolCall(ctx), &mcp.CallToolParams{Meta: meta, Name: tool, Arguments: arguments})
	t := s.tap.take()
	s.mu.Lock()
	out.Declined, out.DeclineReason, s.prompter = s.declined, s.reason, nil
	s.mu.Unlock()
	if err != nil {
		if abandonedAuth(ctx, err, s.status) {
			// The SDK fails the connection when a wait for authorization
			// is canceled, so the session cannot be reused.
			out.Dispatched, out.Retire = false, true
			return out, contextError(ctx.Err(), false)
		}
		var unsent *auth.UnsentError
		if errors.As(err, &unsent) {
			// No token was sent, or the server's 401 came before the tool ran.
			out.Dispatched = false
		}
		if failure := authFailure(err, s.status); failure != nil {
			return out, failure
		}
		if ctx.Err() != nil {
			return out, contextError(ctx.Err(), true)
		}
	}
	if t.TooLarge {
		out.Retire = true
		return out, output.NewError("result_too_large", nil)
	}
	if t.RPCError != nil {
		if out.Declined != "" {
			return out, output.ElicitationDeclined(out.DeclineReason, out.Declined)
		}
		// A non-2xx answer can mean the HTTP session is gone (404).
		out.Retire = t.Status >= 300
		return out, output.ServerError(int(t.RPCError.Code), t.RPCError.Message)
	}
	if t.Result != nil {
		// The raw result keeps unknown blocks and fields and exact numbers,
		// even when the SDK's typed decoding failed.
		isError, needsInput, e := inspectResult(t.Result)
		if e != nil {
			return out, e
		}
		out.JSON, out.IsError, out.NeedsInput = t.Result, isError, needsInput
		if out.NeedsInput {
			return out, output.NewError("input_required", nil)
		}
		return out, nil
	}
	if err != nil {
		var wire *jsonrpc.Error
		if out.Declined != "" && errors.As(err, &wire) {
			return out, output.ElicitationDeclined(out.DeclineReason, out.Declined)
		}
		// SDK WireErrors can also originate locally, without a response-origin marker.
		return out, contextError(err, true)
	}
	// Defensive: the SDK answered but the tap saw no response.
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
