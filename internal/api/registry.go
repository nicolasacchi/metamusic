package api

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// Meta is the x-mm extension: how a capability is exposed everywhere.
type Meta struct {
	Scope     auth.Scope `json:"scope"`
	Cost      string     `json:"cost"`
	MCP       string     `json:"mcp"`
	UI        string     `json:"ui"`
	HumanOnly bool       `json:"human_only"`
}

const humanPrefix = "/human/api/"

var opIDRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidateMeta applies the registration rules to one operation; tests use it
// to check the serialized spec.
func ValidateMeta(op huma.Operation, m Meta) error { return validate(op, m) }

func validate(op huma.Operation, m Meta) error {
	switch {
	case !opIDRe.MatchString(op.OperationID):
		return fmt.Errorf("operation id %q must match %s", op.OperationID, opIDRe)
	case op.Summary == "":
		return fmt.Errorf("%s: summary required", op.OperationID)
	case op.Description == "":
		return fmt.Errorf("%s: description required", op.OperationID)
	}
	switch m.Scope {
	case auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin:
	default:
		return fmt.Errorf("%s: invalid scope %q", op.OperationID, m.Scope)
	}
	switch m.Cost {
	case "free", "spend", "approve":
	default:
		return fmt.Errorf("%s: invalid cost %q", op.OperationID, m.Cost)
	}
	if !(m.MCP == "tool" || m.MCP == "generic" || hasReason(m.MCP, "exclude:")) {
		return fmt.Errorf("%s: invalid mcp %q", op.OperationID, m.MCP)
	}
	if !(strings.HasPrefix(m.UI, "/") || hasReason(m.UI, "none:")) {
		return fmt.Errorf("%s: invalid ui %q", op.OperationID, m.UI)
	}
	if !strings.HasPrefix(op.Path, "/api/") && !strings.HasPrefix(op.Path, humanPrefix) {
		return fmt.Errorf("%s: path %q must be under /api/ or %s", op.OperationID, op.Path, humanPrefix)
	}
	if m.HumanOnly && !hasReason(m.MCP, "exclude:") {
		return fmt.Errorf("%s: human_only ops must use mcp exclude:<reason>, got %q", op.OperationID, m.MCP)
	}
	inHuman := strings.HasPrefix(op.Path, humanPrefix)
	if m.HumanOnly != inHuman {
		return fmt.Errorf("%s: human_only=%v inconsistent with path %q", op.OperationID, m.HumanOnly, op.Path)
	}
	return nil
}

func hasReason(s, prefix string) bool {
	return strings.HasPrefix(s, prefix) && len(s) > len(prefix)
}

// Register adds a capability. It panics on a malformed registration (a
// programming error caught by tests) and wraps h with the principal checks.
func Register[I, O any](a huma.API, op huma.Operation, meta Meta, h func(context.Context, *I) (*O, error)) {
	if err := validate(op, meta); err != nil {
		panic("api.Register: " + err.Error())
	}
	exts := make(map[string]any, len(op.Extensions)+1)
	for k, v := range op.Extensions {
		exts[k] = v
	}
	exts["x-mm"] = meta
	op.Extensions = exts
	op.Middlewares = append(huma.Middlewares{func(ctx huma.Context, next func(huma.Context)) {
		if err := authorize(ctx.Context(), meta); err != nil {
			writeProblem(ctx, err.(*Problem))
			return
		}
		next(ctx)
	}}, op.Middlewares...)
	huma.Register(a, op, func(ctx context.Context, in *I) (*O, error) {
		// Defense in depth: the middleware already refused unauthorized callers.
		if err := authorize(ctx, meta); err != nil {
			return nil, err
		}
		return h(ctx, in)
	})
}

// authorize checks the principal against the operation metadata.
func authorize(ctx context.Context, meta Meta) error {
	p := auth.FromContext(ctx)
	if p.Kind == auth.KindAnonymous || p.Kind == "" {
		return Err(401, "UNAUTHENTICATED", "authentication required", nil)
	}
	if meta.HumanOnly && p.Kind != auth.KindHuman {
		return Err(403, "HUMAN_ONLY", "this operation is restricted to human users", nil)
	}
	if !p.Has(meta.Scope) {
		return Err(403, "FORBIDDEN_SCOPE", "missing required scope", map[string]any{"scope": string(meta.Scope)})
	}
	return nil
}

// writeProblem writes p as application/problem+json before any input is read.
func writeProblem(ctx huma.Context, p *Problem) {
	ctx.SetHeader("Content-Type", "application/problem+json")
	ctx.SetStatus(p.Status)
	_ = json.NewEncoder(ctx.BodyWriter()).Encode(p)
}

// OpMeta returns the x-mm metadata of a registered operation.
func OpMeta(op *huma.Operation) (Meta, bool) {
	if op == nil {
		return Meta{}, false
	}
	m, ok := op.Extensions["x-mm"].(Meta)
	return m, ok
}

// OpInfo describes one registered operation.
type OpInfo struct {
	ID, Method, Path string
	Meta             Meta
}

// Operations lists every operation of a, sorted by path then method.
func Operations(a huma.API) []OpInfo {
	var out []OpInfo
	for path, item := range a.OpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{
			"GET": item.Get, "PUT": item.Put, "POST": item.Post, "DELETE": item.Delete,
			"OPTIONS": item.Options, "HEAD": item.Head, "PATCH": item.Patch, "TRACE": item.Trace,
		} {
			if op == nil {
				continue
			}
			m, _ := OpMeta(op)
			out = append(out, OpInfo{ID: op.OperationID, Method: method, Path: path, Meta: m})
		}
	}
	sortOps(out)
	return out
}
