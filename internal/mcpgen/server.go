// Package mcpgen derives MCP tools from the Huma operation registry and
// dispatches tool calls in-process into the same API.
package mcpgen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
)

type entry struct {
	spec *toolSpec
	meta api.Meta
}

type gen struct {
	inner    http.Handler
	callable map[string]entry // tool + generic operations
}

// NewServer builds an MCP server whose tools are derived from api's operations
// and dispatch into inner (the Huma mux, not the outer auth chain).
func NewServer(a huma.API, inner http.Handler, version string) (*mcp.Server, error) {
	raw, err := json.Marshal(a.OpenAPI())
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	comps := asMap(asMap(doc["components"])["schemas"])
	paths := asMap(doc["paths"])

	srv := mcp.NewServer(&mcp.Implementation{Name: "metamusic", Version: version}, nil)
	g := &gen{inner: inner, callable: map[string]entry{}}
	for _, op := range api.Operations(a) {
		if op.Meta.MCP != "tool" && op.Meta.MCP != "generic" {
			if !strings.HasPrefix(op.Meta.MCP, "exclude:") {
				return nil, fmt.Errorf("mcpgen: operation %s has invalid mcp %q", op.ID, op.Meta.MCP)
			}
			continue
		}
		if op.ID == "describe_operation" || op.ID == "call_operation" {
			return nil, fmt.Errorf("mcpgen: operation id %q is reserved", op.ID)
		}
		o := asMap(asMap(paths[op.Path])[strings.ToLower(op.Method)])
		if o == nil {
			return nil, fmt.Errorf("mcpgen: operation %s missing from OpenAPI document", op.ID)
		}
		spec, err := buildSpec(op.ID, op.Method, op.Path, o, comps)
		if err != nil {
			return nil, err
		}
		g.callable[op.ID] = entry{spec, op.Meta}
		if op.Meta.MCP != "tool" {
			continue
		}
		t := &mcp.Tool{
			Name:        op.ID,
			Description: spec.Description,
			InputSchema: spec.Input,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: op.Method == http.MethodGet, IdempotentHint: op.Method == http.MethodGet},
		}
		if spec.Output != nil {
			t.OutputSchema = spec.Output
		}
		// Unset openWorldHint means true to clients, so free ops say false.
		open := op.Meta.Cost == "spend"
		t.Annotations.OpenWorldHint = &open
		srv.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return dispatch(ctx, inner, spec, req.Params.Arguments), nil
		})
	}
	g.addGeneric(srv)
	return srv, nil
}

// Handler serves srv over stateless streamable HTTP with JSON responses.
func Handler(srv *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			Stateless: true, JSONResponse: true,
			// /mcp is bearer-only, so DNS rebinding cannot ride ambient
			// credentials; the guard would only break reverse-proxied
			// loopback deployments (Host != loopback).
			DisableLocalhostProtection: true,
		})
}
