package mcpgen

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	describeSchema = map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"operation_id": map[string]any{"type": "string", "description": "Operation id to describe"}},
		"required":             []string{"operation_id"},
		"additionalProperties": false,
	}
	callSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"operation_id": map[string]any{"type": "string", "description": "Operation id to call"},
			"arguments":    map[string]any{"type": "object", "description": "Arguments as described by describe_operation"},
		},
		"required":             []string{"operation_id"},
		"additionalProperties": false,
	}
)

func (g *gen) addGeneric(srv *mcp.Server) {
	srv.AddTool(&mcp.Tool{
		Name: "describe_operation",
		Description: "Describe an API operation that is not exposed as its own tool: method, path, scope, cost, " +
			"description and the input/output JSON schemas. Use with call_operation.",
		InputSchema: describeSchema,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			OperationID string `json:"operation_id"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil || in.OperationID == "" {
			return problemResult("VALIDATION_FAILED", "operation_id is required"), nil
		}
		e, ok := g.callable[in.OperationID]
		if !ok {
			return problemResult("NOT_FOUND", "no such operation"), nil
		}
		doc := map[string]any{
			"operation_id": e.spec.ID,
			"method":       e.spec.Method,
			"path":         e.spec.Path,
			"scope":        e.meta.Scope,
			"cost":         e.meta.Cost,
			"mcp":          e.meta.MCP,
			"description":  strings.TrimSpace(e.spec.Description),
			"input_schema": e.spec.Input,
		}
		if e.spec.Output != nil {
			doc["output_schema"] = e.spec.Output
		}
		b, _ := json.Marshal(doc)
		return &mcp.CallToolResult{StructuredContent: doc, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
	})

	srv.AddTool(&mcp.Tool{
		Name: "call_operation",
		Description: "Call an API operation by id with the arguments described by describe_operation. " +
			"Operations excluded from MCP are refused.",
		InputSchema: callSchema,
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			OperationID string          `json:"operation_id"`
			Arguments   json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil || in.OperationID == "" {
			return problemResult("VALIDATION_FAILED", "operation_id is required"), nil
		}
		e, ok := g.callable[in.OperationID]
		if !ok {
			return problemResult("NOT_FOUND", "no such operation"), nil
		}
		return dispatch(ctx, g.inner, e.spec, in.Arguments), nil
	})
}
