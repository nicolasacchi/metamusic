package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
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

	Idempotency  string   `json:"idempotency,omitempty"`   // "" | "create" (D-M2a-3)
	Rate         string   `json:"rate,omitempty"`          // "" (default bucket) | "heavy"
	SecretFields []string `json:"secret_fields,omitempty"` // top-level response keys that carry a secret (D-M2a-14)
	MCPImage     bool     `json:"mcp_image,omitempty"`     // the 2xx body has image {mime_type, data_base64 (optional)}; MCP returns ImageContent (C52)
	AliasOf      string   `json:"alias_of,omitempty"`      // a human-only alias of another operation (design §4.1, M3 C80)
}

const humanPrefix = "/human/api/"

var (
	opIDRe        = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	secretFieldRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

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
	switch m.Idempotency {
	case "":
	case "create":
		if op.Method != http.MethodPost {
			return fmt.Errorf("%s: idempotency create requires POST, got %s", op.OperationID, op.Method)
		}
	default:
		return fmt.Errorf("%s: invalid idempotency %q", op.OperationID, m.Idempotency)
	}
	switch m.Rate {
	case "", "heavy":
	default:
		return fmt.Errorf("%s: invalid rate %q", op.OperationID, m.Rate)
	}
	// D-M4a-24: a secret may be minted by an admin-scope agent too, so the rule is "a create operation" (the
	// idempotent replay nulls the secrets through idem.Redact), no longer "a human-only create operation".
	if len(m.SecretFields) > 0 && m.Idempotency != "create" {
		return fmt.Errorf("%s: secret_fields require an idempotency create op", op.OperationID)
	}
	for _, f := range m.SecretFields {
		if !secretFieldRe.MatchString(f) {
			return fmt.Errorf("%s: secret field %q must match %s", op.OperationID, f, secretFieldRe)
		}
	}
	// M3 C80: spend and approval rules, aliases.
	if m.Cost == "spend" {
		switch {
		case op.Method != http.MethodPost:
			return fmt.Errorf("%s: cost spend requires POST, got %s", op.OperationID, op.Method)
		case m.Idempotency != "create":
			return fmt.Errorf("%s: cost spend requires idempotency create", op.OperationID)
		case m.Scope != auth.ScopeGenerate:
			return fmt.Errorf("%s: cost spend requires scope generate, got %q", op.OperationID, m.Scope)
		}
	}
	if m.Cost == "approve" && !m.HumanOnly {
		return fmt.Errorf("%s: cost approve requires a human-only operation", op.OperationID)
	}
	if m.AliasOf != "" {
		if !opIDRe.MatchString(m.AliasOf) {
			return fmt.Errorf("%s: alias_of %q must be an operation id", op.OperationID, m.AliasOf)
		}
		if !m.HumanOnly || m.MCP != "exclude:human-only" {
			return fmt.Errorf("%s: an alias must be human-only with mcp exclude:human-only", op.OperationID)
		}
	}
	if m.MCPImage {
		if op.Method != http.MethodGet {
			return fmt.Errorf("%s: mcp_image requires GET, got %s", op.OperationID, op.Method)
		}
		if hasReason(m.MCP, "exclude:") {
			return fmt.Errorf("%s: mcp_image is meaningless on an op excluded from MCP", op.OperationID)
		}
	}
	return nil
}

// imageType is the type of an mcp_image body's `image` field.
var imageType = reflect.TypeFor[*ImageData]()

// checkImageShape verifies by reflection that O has a Body whose struct carries
// a field with JSON name `image` of type *ImageData (C52).
func checkImageShape(id string, out reflect.Type) error {
	for out.Kind() == reflect.Pointer {
		out = out.Elem()
	}
	if out.Kind() != reflect.Struct {
		return fmt.Errorf("%s: mcp_image output must be a struct with a Body", id)
	}
	bf, ok := out.FieldByName("Body")
	if !ok {
		return fmt.Errorf("%s: mcp_image output has no Body field", id)
	}
	body := bf.Type
	for body.Kind() == reflect.Pointer {
		body = body.Elem()
	}
	if body.Kind() != reflect.Struct {
		return fmt.Errorf("%s: mcp_image Body must be a struct", id)
	}
	for i := range body.NumField() {
		f := body.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "image" && f.Type == imageType {
			return nil
		}
	}
	return fmt.Errorf("%s: mcp_image Body needs a field with JSON name image of type *api.ImageData", id)
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
	if meta.MCPImage {
		if err := checkImageShape(op.OperationID, reflect.TypeFor[O]()); err != nil {
			panic("api.Register: " + err.Error())
		}
	}
	exts := make(map[string]any, len(op.Extensions)+1)
	for k, v := range op.Extensions {
		exts[k] = v
	}
	exts["x-mm"] = meta
	op.Extensions = exts
	op.Middlewares = append(huma.Middlewares{func(ctx huma.Context, next func(huma.Context)) {
		if err := Authorize(ctx.Context(), meta); err != nil {
			var pr *Problem
			if !errors.As(err, &pr) {
				pr = &Problem{Status: 500, Code: "INTERNAL", Message: "internal error", Details: map[string]any{}}
			}
			writeProblem(ctx, pr)
			return
		}
		next(ctx)
	}}, op.Middlewares...)
	huma.Register(a, op, func(ctx context.Context, in *I) (*O, error) {
		// Defense in depth: the middleware already refused unauthorized callers.
		if err := Authorize(ctx, meta); err != nil {
			return nil, err
		}
		return h(ctx, in)
	})
}

// Authorize checks the principal in ctx against the operation metadata. The
// registration wrapper and the request gate share it.
func Authorize(ctx context.Context, meta Meta) error {
	p := auth.FromContext(ctx)
	if p.Kind == auth.KindAnonymous || p.Kind == "" {
		return Err(401, "UNAUTHENTICATED", "authentication required", nil)
	}
	if meta.HumanOnly && p.Kind != auth.KindHuman {
		return Err(403, "HUMAN_ONLY", "this operation is restricted to human users", nil)
	}
	if !p.Has(meta.Scope) {
		details := map[string]any{"scope": string(meta.Scope)}
		if p.Via == "oauth" { // a connector's scopes are the grant's: only the operator can widen them (M4a C106)
			details["hint"] = "the operator can enable this scope for the connector at /human/connectors"
		}
		return Err(403, "FORBIDDEN_SCOPE", "missing required scope", details)
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

// ValidateAliases checks every human-only alias (Meta.AliasOf): the target
// operation exists and its request-body and 2xx response schemas are identical
// to the alias's (resolved structurally, so two Go types of one shape match).
// api.New calls it last; it panics on a dangling alias or a mismatch.
func ValidateAliases(a huma.API) {
	if err := checkAliases(a); err != nil {
		panic("api.ValidateAliases: " + err.Error())
	}
}

func checkAliases(a huma.API) error {
	spec := a.OpenAPI()
	byID := map[string]*huma.Operation{}
	for _, item := range spec.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Put, item.Post, item.Delete, item.Patch} {
			if op != nil {
				byID[op.OperationID] = op
			}
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		alias := byID[id]
		m, _ := OpMeta(alias)
		if m.AliasOf == "" {
			continue
		}
		target, ok := byID[m.AliasOf]
		if !ok {
			return fmt.Errorf("%s: alias_of %q does not exist", id, m.AliasOf)
		}
		for _, side := range []struct {
			name string
			get  func(*huma.Operation) (json.RawMessage, error)
		}{{"request body", func(o *huma.Operation) (json.RawMessage, error) { return bodySchema(spec, o) }},
			{"2xx response", func(o *huma.Operation) (json.RawMessage, error) { return responseSchema(spec, o) }}} {
			want, err1 := side.get(target)
			got, err2 := side.get(alias)
			if err1 != nil || err2 != nil {
				return fmt.Errorf("%s: %s schema: %w", id, side.name, errors.Join(err1, err2))
			}
			if !bytes.Equal(want, got) {
				return fmt.Errorf("%s: %s schema differs from %s", id, side.name, m.AliasOf)
			}
		}
	}
	return nil
}

func bodySchema(spec *huma.OpenAPI, op *huma.Operation) (json.RawMessage, error) {
	if op.RequestBody == nil {
		return json.RawMessage("null"), nil
	}
	return resolvedContent(spec, op.RequestBody.Content)
}

// responseSchema is the schema of the lowest 2xx response.
func responseSchema(spec *huma.OpenAPI, op *huma.Operation) (json.RawMessage, error) {
	var codes []string
	for c := range op.Responses {
		if strings.HasPrefix(c, "2") {
			codes = append(codes, c)
		}
	}
	if len(codes) == 0 {
		return json.RawMessage("null"), nil
	}
	sort.Strings(codes)
	return resolvedContent(spec, op.Responses[codes[0]].Content)
}

func resolvedContent(spec *huma.OpenAPI, content map[string]*huma.MediaType) (json.RawMessage, error) {
	if len(content) == 0 {
		return json.RawMessage("null"), nil
	}
	types := make([]string, 0, len(content))
	for t := range content {
		types = append(types, t)
	}
	sort.Strings(types)
	out := map[string]any{}
	for _, t := range types {
		raw, err := json.Marshal(content[t].Schema)
		if err != nil {
			return nil, err
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out[t] = derefSchema(spec, v, map[string]bool{})
	}
	return json.Marshal(out) // map keys are sorted, so the bytes are canonical
}

const schemaRefPrefix = "#/components/schemas/"

// derefSchema replaces {"$ref": "#/components/schemas/X"} by X's definition,
// recursively; a cycle keeps the reference name.
func derefSchema(spec *huma.OpenAPI, v any, seen map[string]bool) any {
	switch x := v.(type) {
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok && strings.HasPrefix(ref, schemaRefPrefix) {
			name := strings.TrimPrefix(ref, schemaRefPrefix)
			def := spec.Components.Schemas.Map()[name]
			if def == nil || seen[name] {
				return map[string]any{"$cycle": name}
			}
			raw, _ := json.Marshal(def)
			var dv any
			_ = json.Unmarshal(raw, &dv)
			seen[name] = true
			r := derefSchema(spec, dv, seen)
			delete(seen, name)
			return r
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			if k == "$schema" {
				continue
			}
			out[k] = derefSchema(spec, e, seen)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = derefSchema(spec, e, seen)
		}
		return out
	}
	return v
}
