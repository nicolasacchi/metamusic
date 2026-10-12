package mcpgen

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

type itemIn struct {
	ID    string `path:"id" doc:"Item id"`
	Limit int    `query:"limit" required:"false" doc:"Max rows"`
	Key   string `header:"Idempotency-Key" required:"false" doc:"Dedup key"`
}
type itemOut struct {
	Body struct {
		ID string `json:"id"`
	}
}

type noteIn struct {
	ID   string `path:"id"`
	Body struct {
		Text string   `json:"text" doc:"Note text"`
		Tags []string `json:"tags,omitempty"`
	}
}
type collideIn struct {
	ID   string `path:"id"`
	Body struct {
		ID string `json:"id"`
	}
}
type Node struct {
	Name     string `json:"name"`
	Children []Node `json:"children,omitempty"`
}
type treeIn struct{ Body Node }
type treeOut struct{ Body Node }

func testAPI(t *testing.T) (huma.API, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("t", "0.0.1")
	cfg.OpenAPIPath, cfg.DocsPath, cfg.SchemasPath, cfg.CreateHooks = "/api/openapi", "", "", nil
	a := humago.New(mux, cfg)
	m := func(mcp string) api.Meta {
		return api.Meta{Scope: auth.ScopeRead, Cost: "free", MCP: mcp, UI: "none:test"}
	}
	op := func(id, method, path string) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Summary: "S " + id, Description: "D " + id}
	}
	api.Register(a, op("get_item", "GET", "/api/items/{id}"), m("tool"),
		func(ctx context.Context, in *itemIn) (*itemOut, error) {
			out := &itemOut{}
			out.Body.ID = in.ID + "|" + in.Key
			return out, nil
		})
	api.Register(a, op("add_note", "POST", "/api/items/{id}/notes"), m("tool"),
		func(ctx context.Context, in *noteIn) (*itemOut, error) {
			out := &itemOut{}
			out.Body.ID = in.ID + ":" + in.Body.Text + ":" + strings.Join(in.Body.Tags, ",")
			return out, nil
		})
	api.Register(a, op("collide", "POST", "/api/collide/{id}"), m("tool"),
		func(ctx context.Context, in *collideIn) (*itemOut, error) {
			out := &itemOut{}
			out.Body.ID = in.ID + "/" + in.Body.ID
			return out, nil
		})
	api.Register(a, op("tree", "POST", "/api/tree"), m("tool"),
		func(ctx context.Context, in *treeIn) (*treeOut, error) { return &treeOut{Body: in.Body}, nil })
	api.Register(a, op("generic_op", "GET", "/api/generic/{id}"), m("generic"),
		func(ctx context.Context, in *itemIn) (*itemOut, error) {
			out := &itemOut{}
			out.Body.ID = "g" + in.ID
			return out, nil
		})
	api.Register(a, op("hidden_op", "GET", "/api/hidden"), m("exclude:internal"),
		func(ctx context.Context, _ *struct{}) (*itemOut, error) { return &itemOut{}, nil })
	return a, mux
}

func specs(t *testing.T) map[string]*toolSpec {
	t.Helper()
	a, _ := testAPI(t)
	raw, _ := json.Marshal(a.OpenAPI())
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]*toolSpec{}
	for _, op := range api.Operations(a) {
		o := asMap(asMap(asMap(doc["paths"])[op.Path])[strings.ToLower(op.Method)])
		s, err := buildSpec(op.ID, op.Method, op.Path, o, asMap(asMap(doc["components"])["schemas"]))
		if err != nil {
			t.Fatal(err)
		}
		out[op.ID] = s
	}
	return out
}

func propNames(s *toolSpec) []string {
	var n []string
	for k := range asMap(s.Input["properties"]) {
		n = append(n, k)
	}
	return n
}

func TestSchemaParamsAndHeaders(t *testing.T) {
	s := specs(t)["get_item"]
	got := asMap(s.Input["properties"])
	for _, want := range []string{"id", "limit", "idempotency_key"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing %q in %v", want, propNames(s))
		}
	}
	if len(got) != 3 {
		t.Fatalf("props %v", propNames(s))
	}
	if diff := cmp.Diff([]string{"id"}, s.Input["required"]); diff != "" {
		t.Fatalf("required: %s", diff)
	}
	if s.Input["additionalProperties"] != false || s.Input["type"] != "object" {
		t.Fatalf("shape %v", s.Input)
	}
	if s.Output == nil || s.Output["type"] != "object" {
		t.Fatalf("output %v", s.Output)
	}
	var hdr param
	for _, p := range s.Params {
		if p.Arg == "idempotency_key" {
			hdr = p
		}
	}
	if hdr.Wire != "Idempotency-Key" || hdr.In != "header" {
		t.Fatalf("header mapping %+v", hdr)
	}
}

func TestSchemaFlatBody(t *testing.T) {
	s := specs(t)["add_note"]
	if s.Body != bodyFlat {
		t.Fatalf("body mode %v", s.Body)
	}
	got := asMap(s.Input["properties"])
	for _, want := range []string{"id", "text", "tags"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing %q in %v", want, propNames(s))
		}
	}
	if _, ok := got["body"]; ok {
		t.Fatal("flat body must not add a body property")
	}
	if diff := cmp.Diff([]string{"id", "text"}, s.Input["required"]); diff != "" {
		t.Fatalf("required: %s", diff)
	}
}

func TestSchemaCollisionNestsBody(t *testing.T) {
	s := specs(t)["collide"]
	if s.Body != bodyNested {
		t.Fatalf("body mode %v", s.Body)
	}
	got := asMap(s.Input["properties"])
	if len(got) != 2 || got["id"] == nil || got["body"] == nil {
		t.Fatalf("props %v", propNames(s))
	}
	b := asMap(got["body"])
	if asMap(b["properties"])["id"] == nil {
		t.Fatalf("nested body lost its id property: %v", b)
	}
}

func hasRefTo(v any, prefix string) bool {
	switch t := v.(type) {
	case map[string]any:
		if r, ok := t["$ref"].(string); ok && strings.HasPrefix(r, prefix) {
			return true
		}
		for _, x := range t {
			if hasRefTo(x, prefix) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if hasRefTo(x, prefix) {
				return true
			}
		}
	}
	return false
}

func TestSchemaRefsInlined(t *testing.T) {
	for id, s := range specs(t) {
		if hasRefTo(s.Input, refPrefix) || hasRefTo(s.Output, refPrefix) {
			t.Errorf("%s: component $ref left in schema", id)
		}
	}
	tree := specs(t)["tree"]
	if _, ok := tree.Input["$defs"]; !ok {
		t.Fatalf("recursive type must land in $defs: %v", tree.Input)
	}
	if !hasRefTo(tree.Input, "#/$defs/") {
		t.Fatalf("recursive ref should point into $defs: %v", tree.Input)
	}
}

func TestSchemaNoDuplicateOrUnknownArgs(t *testing.T) {
	for id, s := range specs(t) {
		seen := map[string]bool{}
		for _, p := range s.Params {
			if seen[p.Arg] {
				t.Errorf("%s: duplicate arg %q", id, p.Arg)
			}
			seen[p.Arg] = true
		}
		for _, n := range s.BodyProps {
			if seen[n] {
				t.Errorf("%s: body prop %q collides with a parameter", id, n)
			}
			seen[n] = true
		}
		props := asMap(s.Input["properties"])
		for n := range seen {
			if props[n] == nil {
				t.Errorf("%s: %q missing from schema", id, n)
			}
		}
	}
}

func TestBuildRequest(t *testing.T) {
	sp := specs(t)
	req, bad := buildRequest(context.Background(), sp["get_item"],
		json.RawMessage(`{"id":"a/b c","limit":3,"idempotency_key":"k1"}`))
	if bad != nil {
		t.Fatal(bad)
	}
	if req.URL.EscapedPath() != "/api/items/a%2Fb%20c" || req.URL.Query().Get("limit") != "3" || req.Header.Get("Idempotency-Key") != "k1" {
		t.Fatalf("req %s hdr %v", req.URL, req.Header)
	}
	if _, bad := buildRequest(context.Background(), sp["get_item"], json.RawMessage(`{"id":"x","nope":1}`)); bad == nil {
		t.Fatal("unknown argument must be refused")
	}
	if _, bad := buildRequest(context.Background(), sp["get_item"], json.RawMessage(`{}`)); bad == nil {
		t.Fatal("missing path parameter must be refused")
	}
}
