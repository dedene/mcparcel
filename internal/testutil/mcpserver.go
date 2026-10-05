// Package testutil holds deterministic fixtures shared by tests.
package testutil

import (
	"context"
	"encoding/json"
	"strconv"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// EchoInput is the argument object of the fixture "echo" tool.
type EchoInput struct {
	Text string `json:"text"`
}

// EchoOutput is the structured result of the fixture "echo" tool.
type EchoOutput struct {
	Text string `json:"text"`
}

// CounterOutput is the structured result of the fixture "counter" tool.
type CounterOutput struct {
	Count int64 `json:"count"`
}

func NewFixtureServer() *mcp.Server { return NewFixtureServerWithOptions(FixtureOptions{}) }

func NewFixtureServerWithOptions(opts FixtureOptions) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcparcel-fixture", Version: "0.0.0"}, &mcp.ServerOptions{PageSize: opts.PageSize})

	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Return the given text."},
		func(_ context.Context, _ *mcp.CallToolRequest, in EchoInput) (*mcp.CallToolResult, EchoOutput, error) {
			return nil, EchoOutput(in), nil
		})

	var calls atomic.Int64
	mcp.AddTool(server, &mcp.Tool{Name: "counter", Description: "Count calls on this server instance."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, CounterOutput, error) {
			return nil, CounterOutput{Count: calls.Add(1)}, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "echo.dotted"}, func(_ context.Context, _ *mcp.CallToolRequest, in EchoInput) (*mcp.CallToolResult, EchoOutput, error) {
		return nil, EchoOutput(in), nil
	})
	server.AddTool(&mcp.Tool{Name: "typed", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer"}, "enabled": map[string]any{"type": "boolean"}, "queries": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}, "required": []string{"limit", "enabled", "queries"}}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in TypedInput
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return nil, err
		}
		r := textResult("limit-received=" + strconv.FormatInt(in.Limit, 10))
		r.StructuredContent = in
		return r, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "env"}, func(_ context.Context, _ *mcp.CallToolRequest, in EnvInput) (*mcp.CallToolResult, EnvOutput, error) {
		return nil, EnvOutput{Value: opts.lookup(in.Name)}, nil
	})
	server.AddTool(&mcp.Tool{Name: "wait", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := opts.started(ctx, "wait"); err != nil {
			return nil, err
		}
		if opts.Release != nil {
			select {
			case <-opts.Release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return textResult("released"), nil
	})
	server.AddTool(&mcp.Tool{Name: "write_drop", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := opts.started(ctx, "write_drop"); err != nil {
			return nil, err
		}
		if opts.OnWrite != nil {
			opts.OnWrite()
		}
		if opts.Crash != nil {
			opts.Crash()
		}
		return textResult("written"), nil
	})
	server.AddTool(&mcp.Tool{Name: "fail", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		r := textResult("fixture failure")
		r.IsError = true
		return r, nil
	})
	server.AddTool(&mcp.Tool{Name: "rich", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		r := textResult("rich")
		r.StructuredContent = map[string]any{"answer": 42}
		r.SetMeta(mcp.Meta{"fixture": "rich"})
		return r, nil
	})
	server.AddTool(&mcp.Tool{Name: "elicit", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}, "then": map[string]any{"type": "string"}}}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct{ Message, Then string }
		_ = json.Unmarshal(req.Params.Arguments, &in)
		res, err := req.Session.Elicit(ctx, &mcp.ElicitParams{Message: in.Message, RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"allow": map[string]any{"type": "boolean"}}}})
		if err != nil {
			return nil, err
		}
		switch in.Then {
		case "error":
			r := textResult("action=" + res.Action)
			r.IsError = true
			return r, nil
		case "rpc":
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "action=" + res.Action}
		}
		return textResult("action=" + res.Action), nil
	})
	server.AddTool(&mcp.Tool{Name: "meta", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		r := textResult("meta")
		r.StructuredContent = map[string]any{"meta": req.Params.Meta}
		return r, nil
	})
	if opts.Legacy {
		server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method == "server/discover" {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "expect initialized request"}
				}
				return next(ctx, method, req)
			}
		})
	}
	return server
}
