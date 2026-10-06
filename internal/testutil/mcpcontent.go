package testutil

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addContentTools adds the fixtures for schema refs, large results,
// JSON-RPC errors and media blocks.
func addContentTools(server *mcp.Server, opts FixtureOptions) {
	addMediaTool(server)
	echoArgs := func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in map[string]any
		d := json.NewDecoder(strings.NewReader(string(req.Params.Arguments)))
		d.UseNumber()
		if err := d.Decode(&in); err != nil {
			return nil, err
		}
		r := textResult("refs")
		r.StructuredContent = in
		return r, nil
	}
	server.AddTool(&mcp.Tool{Name: "refs", InputSchema: map[string]any{
		"type": "object",
		"$defs": map[string]any{
			"Limit":  map[string]any{"type": "integer"},
			"Filter": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind"}, "properties": map[string]any{"kind": map[string]any{"enum": []string{"open", "closed"}}}},
		},
		"properties": map[string]any{
			"limit":  map[string]any{"$ref": "#/$defs/Limit"},
			"filter": map[string]any{"$ref": "#/$defs/Filter"},
			"maybe":  map[string]any{"anyOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "null"}}},
		},
	}}, echoArgs)
	// The SDK requires a root type; the properties sit behind the root $ref.
	server.AddTool(&mcp.Tool{Name: "rootref", InputSchema: map[string]any{
		"type":  "object",
		"$ref":  "#/$defs/Args",
		"$defs": map[string]any{"Args": map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer"}}}},
	}}, echoArgs)
	server.AddTool(&mcp.Tool{Name: "large", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"bytes": map[string]any{"type": "integer"}, "char": map[string]any{"type": "string"}, "gate": map[string]any{"type": "boolean"}}}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			Bytes int
			Char  string
			Gate  bool
		}
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return nil, err
		}
		if in.Char == "" {
			in.Char = "x"
		}
		if in.Gate {
			if err := opts.started(ctx, "large"); err != nil {
				return nil, err
			}
			if opts.Release != nil {
				select {
				case <-opts.Release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
		return textResult(strings.Repeat(in.Char[:1], max(0, in.Bytes))), nil
	})
	server.AddTool(&mcp.Tool{Name: "rpcfail", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "fixture rejected limit"}
	})
}
