package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

type nothing struct{}
type outT struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

func h(context.Context, *nothing) (*outT, error) { return &outT{}, nil }

func newAPI() huma.API {
	return humago.New(http.NewServeMux(), huma.DefaultConfig("t", "1"))
}

func goodOp() huma.Operation {
	return huma.Operation{OperationID: "do_thing", Method: "GET", Path: "/api/thing", Summary: "s", Description: "d"}
}

func goodMeta() api.Meta {
	return api.Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/"}
}

func TestRegisterPanics(t *testing.T) {
	tests := []struct {
		name string
		op   func(*huma.Operation)
		meta func(*api.Meta)
	}{
		{"bad id upper", func(o *huma.Operation) { o.OperationID = "DoThing" }, nil},
		{"bad id empty", func(o *huma.Operation) { o.OperationID = "" }, nil},
		{"bad id dash", func(o *huma.Operation) { o.OperationID = "do-thing" }, nil},
		{"no summary", func(o *huma.Operation) { o.Summary = "" }, nil},
		{"no description", func(o *huma.Operation) { o.Description = "" }, nil},
		{"empty scope", nil, func(m *api.Meta) { m.Scope = "" }},
		{"unknown scope", nil, func(m *api.Meta) { m.Scope = "root" }},
		{"bad cost", nil, func(m *api.Meta) { m.Cost = "pricey" }},
		{"empty cost", nil, func(m *api.Meta) { m.Cost = "" }},
		{"bad mcp", nil, func(m *api.Meta) { m.MCP = "maybe" }},
		{"exclude no reason", nil, func(m *api.Meta) { m.MCP = "exclude:" }},
		{"empty ui", nil, func(m *api.Meta) { m.UI = "" }},
		{"bad ui", nil, func(m *api.Meta) { m.UI = "settings" }},
		{"none no reason", nil, func(m *api.Meta) { m.UI = "none:" }},
		{"human only wrong path", nil, func(m *api.Meta) { m.HumanOnly = true }},
		{"human path not human only", func(o *huma.Operation) { o.Path = "/human/api/x" }, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op, meta := goodOp(), goodMeta()
			if tc.op != nil {
				tc.op(&op)
			}
			if tc.meta != nil {
				tc.meta(&meta)
			}
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			api.Register(newAPI(), op, meta, h)
		})
	}
}

func TestRegisterOK(t *testing.T) {
	a := newAPI()
	api.Register(a, goodOp(), goodMeta(), h)
	op := a.OpenAPI().Paths["/api/thing"].Get
	m, ok := api.OpMeta(op)
	if !ok || m.Scope != auth.ScopeRead || m.MCP != "tool" {
		t.Fatalf("meta = %+v %v", m, ok)
	}
	hop := goodOp()
	hop.OperationID, hop.Path = "human_thing", "/human/api/thing"
	hm := goodMeta()
	hm.HumanOnly, hm.UI = true, "none:test"
	api.Register(a, hop, hm, h)
}

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func TestEveryOpHasValidMeta(t *testing.T) {
	a := api.New(http.NewServeMux(), api.Deps{Version: "x"})
	ops := api.Operations(a)
	if len(ops) < 3 {
		t.Fatalf("got %d ops", len(ops))
	}
	for _, o := range ops {
		if !idRe.MatchString(o.ID) {
			t.Errorf("bad id %q", o.ID)
		}
		if o.Meta.Cost != "free" || o.Meta.MCP != "tool" || o.Meta.Scope == "" || o.Meta.UI == "" {
			t.Errorf("%s: meta %+v", o.ID, o.Meta)
		}
		if o.Meta.HumanOnly != strings.HasPrefix(o.Path, "/human/api/") {
			t.Errorf("%s: human_only mismatch", o.ID)
		}
	}
	b, err := api.OpenAPIJSON(a)
	if err != nil || !strings.Contains(string(b), `"x-mm"`) || !strings.Contains(string(b), `"human_only"`) {
		t.Fatalf("x-mm missing in spec: %v", err)
	}
}

func serveAs(a huma.API, mux *http.ServeMux, p auth.Principal, method, path string) *httptest.ResponseRecorder {
	_ = a
	r := httptest.NewRequest(method, path, nil).WithContext(auth.WithPrincipal(context.Background(), p))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestWrapperEnforcement(t *testing.T) {
	mux := http.NewServeMux()
	a := humago.New(mux, huma.DefaultConfig("t", "1"))
	api.Register(a, goodOp(), goodMeta(), h)
	hop := goodOp()
	hop.OperationID, hop.Path = "human_thing", "/human/api/thing"
	hm := goodMeta()
	hm.HumanOnly, hm.UI = true, "none:test"
	api.Register(a, hop, hm, h)

	agentRead := auth.Principal{Kind: auth.KindAgent, Scopes: []auth.Scope{auth.ScopeRead}}
	agentW := auth.Principal{Kind: auth.KindAgent, Scopes: []auth.Scope{auth.ScopeWrite}}
	human := auth.Principal{Kind: auth.KindHuman, Scopes: []auth.Scope{auth.ScopeRead}}
	cases := []struct {
		name string
		p    auth.Principal
		path string
		want int
		code string
	}{
		{"anon", auth.Principal{Kind: auth.KindAnonymous}, "/api/thing", 401, "UNAUTHENTICATED"},
		{"no scope", agentW, "/api/thing", 403, "FORBIDDEN_SCOPE"},
		{"ok agent", agentRead, "/api/thing", 200, ""},
		{"agent on human op", agentRead, "/human/api/thing", 403, "HUMAN_ONLY"},
		{"human on human op", human, "/human/api/thing", 200, ""},
	}
	for _, tc := range cases {
		w := serveAs(a, mux, tc.p, "GET", tc.path)
		if w.Code != tc.want {
			t.Errorf("%s: status %d body %s", tc.name, w.Code, w.Body)
		}
		if tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s: body %s", tc.name, w.Body)
		}
	}
}
