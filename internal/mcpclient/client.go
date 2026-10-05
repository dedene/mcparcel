package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
}
type (
	Result struct {
		JSON       json.RawMessage
		IsError    bool
		NeedsInput bool
		Dispatched bool
	}
	Session interface {
		Tools(context.Context) ([]json.RawMessage, error)
		Call(context.Context, string, map[string]any, func() error) (Result, error)
		Close(context.Context) error
	}
)

type session struct {
	closeOnce sync.Once
	closeDone chan struct{}
	sdk       *mcp.ClientSession
	cleanup   func(context.Context)
	timeout   time.Duration
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
	client := mcp.NewClient(&mcp.Implementation{Name: "MCParcel", Version: opts.Version}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}})
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
	sdk, err := client.Connect(c, transport, nil)
	if err != nil {
		cleanup(context.Background())
		if c.Err() != nil {
			return nil, contextError(c.Err(), false)
		}
		if status() != nil {
			return nil, status()
		}
		return nil, output.NewError("connection_failed", nil)
	}
	return &session{sdk: sdk, cleanup: cleanup, timeout: opts.ConnectTimeout, closeDone: make(chan struct{})}, nil
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

func (s *session) Call(ctx context.Context, tool string, arguments map[string]any, beforeDispatch func() error) (Result, error) {
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
	r, err := s.sdk.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
	if err != nil {
		if ctx.Err() != nil {
			return out, contextError(ctx.Err(), true)
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
