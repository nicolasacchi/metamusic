// Package mcpgen derives MCP tools from the Huma operation registry and
// dispatches tool calls in-process into the same API.
package mcpgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

type entry struct {
	spec *toolSpec
	meta api.Meta
}

type gen struct {
	inner     http.Handler
	callable  map[string]entry // tool + generic operations
	transport reqinfo.Transport
	// principal, when set, resolves the caller before every tool call (stdio).
	principal func(ctx context.Context) (auth.Principal, error)
}

// run resolves the caller (stdio) and dispatches one tool call.
func (g *gen) run(ctx context.Context, req *mcp.CallToolRequest, spec *toolSpec, args json.RawMessage) *mcp.CallToolResult {
	ctx, code := g.resolve(ctx)
	if code != "" {
		if code == "UNAUTHENTICATED" {
			return problemResult(code, "the credential is no longer valid")
		}
		return problemResult(code, "could not resolve the caller")
	}
	return dispatch(ctx, g.inner, spec, args, origin{transport: g.transport, client: clientLabel(req)})
}

// resolve puts the stdio caller on ctx; a non-empty code is the problem code to refuse with.
func (g *gen) resolve(ctx context.Context) (context.Context, string) {
	if g.principal == nil {
		return ctx, ""
	}
	p, err := g.principal(ctx)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidToken) {
			return ctx, "UNAUTHENTICATED"
		}
		return ctx, "INTERNAL"
	}
	return auth.WithPrincipal(ctx, p), ""
}

// StdioOptions configures NewStdioServer.
type StdioOptions struct {
	// Principal is called before EVERY tool call; its result is the caller of
	// that call. An error wrapping auth.ErrInvalidToken answers UNAUTHENTICATED,
	// any other error INTERNAL (never the credential).
	Principal func(ctx context.Context) (auth.Principal, error)
	Version   string
}

// NewServer builds an MCP server whose tools are derived from api's operations
// and dispatch into inner (the gated Huma mux, not the outer auth chain).
func NewServer(a huma.API, inner http.Handler, version string) (*mcp.Server, error) {
	return newServer(a, inner, version, reqinfo.MCP, nil)
}

// NewStdioServer builds the same tools as NewServer for the stdio face: every
// call runs as the principal o.Principal returns, with transport "stdio".
func NewStdioServer(a huma.API, gated http.Handler, o StdioOptions) (*mcp.Server, error) {
	if o.Principal == nil {
		return nil, errors.New("mcpgen: StdioOptions.Principal is required")
	}
	return newServer(a, gated, o.Version, reqinfo.Stdio, o.Principal)
}

func newServer(a huma.API, inner http.Handler, version string, tr reqinfo.Transport,
	principal func(context.Context) (auth.Principal, error)) (*mcp.Server, error) {
	// A registry without a spec's operation (a test fixture) serves no such template; one that has the
	// operation but cannot present it is a startup error (newServerWith). TestResources_RealRegistry pins that
	// the real registry serves every entry of Resources.
	have := map[string]bool{}
	for _, op := range api.Operations(a) {
		have[op.ID] = true
	}
	var specs []ResourceSpec
	for _, rs := range Resources {
		if have[rs.Op] {
			specs = append(specs, rs)
		}
	}
	return newServerWith(a, inner, version, tr, principal, specs)
}

func newServerWith(a huma.API, inner http.Handler, version string, tr reqinfo.Transport,
	principal func(context.Context) (auth.Principal, error), resources []ResourceSpec) (*mcp.Server, error) {
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
	g := &gen{inner: inner, callable: map[string]entry{}, transport: tr, principal: principal}
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
		if op.Meta.Idempotency == "create" {
			if err := spec.markIdempotent(); err != nil {
				return nil, err
			}
		}
		spec.Image = op.Meta.MCPImage
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
			t.OutputSchema = slimOutputSchema(spec.Output)
		}
		// Unset openWorldHint means true to clients, so free ops say false.
		open := op.Meta.Cost == "spend"
		t.Annotations.OpenWorldHint = &open
		srv.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return g.run(ctx, req, spec, req.Params.Arguments), nil
		})
	}
	g.addGeneric(srv)
	if err := g.addResources(srv, resources); err != nil {
		return nil, err
	}
	g.addPrompts(srv)
	return srv, nil
}

// Handler serves srv over stateless streamable HTTP with JSON responses;
// transport-level errors are rewritten to application/problem+json.
func Handler(srv *mcp.Server) http.Handler {
	return problemJSON(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			Stateless: true, JSONResponse: true,
			// /mcp is bearer-only, so DNS rebinding cannot ride ambient
			// credentials; the guard would only break reverse-proxied
			// loopback deployments (Host != loopback).
			DisableLocalhostProtection: true,
		}))
}
