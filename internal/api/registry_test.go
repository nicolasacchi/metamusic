package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		{"path outside api", func(o *huma.Operation) { o.Path = "/outside" }, nil},
		{"human only with tool", func(o *huma.Operation) { o.Path = "/human/api/x" }, func(m *api.Meta) { m.HumanOnly = true; m.UI = "none:x" }},
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
	hm.HumanOnly, hm.UI, hm.MCP = true, "none:test", "exclude:human-only"
	api.Register(a, hop, hm, h)
}

func TestEveryOpHasValidMeta(t *testing.T) {
	a := api.New(http.NewServeMux(), api.Deps{Version: "x"})
	if len(api.Operations(a)) < 3 {
		t.Fatal("too few ops")
	}
	b, err := api.OpenAPIJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Summary     string `json:"summary"`
			Description string `json:"description"`
			XMM         *struct {
				Scope     auth.Scope `json:"scope"`
				Cost      string     `json:"cost"`
				MCP       string     `json:"mcp"`
				UI        string     `json:"ui"`
				HumanOnly bool       `json:"human_only"`
			} `json:"x-mm"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	n := 0
	for path, item := range spec.Paths {
		for method, op := range item {
			n++
			if op.XMM == nil {
				t.Errorf("%s %s: x-mm missing", method, path)
				continue
			}
			m := api.Meta{Scope: op.XMM.Scope, Cost: op.XMM.Cost, MCP: op.XMM.MCP, UI: op.XMM.UI, HumanOnly: op.XMM.HumanOnly}
			hop := huma.Operation{OperationID: op.OperationID, Path: path, Summary: op.Summary, Description: op.Description}
			if err := api.ValidateMeta(hop, m); err != nil {
				t.Errorf("%s %s: %v", method, path, err)
			}
		}
	}
	if n < 3 {
		t.Fatalf("only %d operations in spec", n)
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
	hm.HumanOnly, hm.UI, hm.MCP = true, "none:test", "exclude:human-only"
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

type reqIn struct {
	Q    string `query:"q" required:"true"`
	Body struct {
		N int `json:"n" minimum:"1"`
	}
}

// Authorization must happen before input is read, parsed or validated.
func TestAuthBeforeInput(t *testing.T) {
	mux := http.NewServeMux()
	a := humago.New(mux, huma.DefaultConfig("t", "1"))
	api.Register(a, huma.Operation{OperationID: "purge", Method: "GET", Path: "/human/api/purge", Summary: "s", Description: "d"},
		api.Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", UI: "none:t", HumanOnly: true},
		func(context.Context, *struct {
			Q string `query:"q" required:"true"`
		}) (*outT, error) {
			return &outT{}, nil
		})
	api.Register(a, huma.Operation{OperationID: "wr", Method: "POST", Path: "/api/wr", Summary: "s", Description: "d"},
		api.Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "none:t"},
		func(context.Context, *reqIn) (*outT, error) { return &outT{}, nil })
	call := func(p auth.Principal, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(auth.WithPrincipal(context.Background(), p))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	admin := auth.Principal{Kind: auth.KindAgent, Scopes: []auth.Scope{auth.ScopeAdmin}}
	ro := auth.Principal{Kind: auth.KindAgent, Scopes: []auth.Scope{auth.ScopeRead}}
	anon := auth.Principal{Kind: auth.KindAnonymous}
	big := `{"n":1,"pad":"` + strings.Repeat("x", 2<<20) + `"}`
	for _, tc := range []struct {
		name         string
		p            auth.Principal
		method, path string
		body         string
		status       int
		code         string
	}{
		{"agent human-only no params", admin, "GET", "/human/api/purge", "", 403, "HUMAN_ONLY"},
		{"ro invalid body", ro, "POST", "/api/wr", `{"n":0}`, 403, "FORBIDDEN_SCOPE"},
		{"ro malformed body", ro, "POST", "/api/wr", `{`, 403, "FORBIDDEN_SCOPE"},
		{"ro oversized body", ro, "POST", "/api/wr", big, 403, "FORBIDDEN_SCOPE"},
		{"anon invalid input", anon, "POST", "/api/wr", `{"n":0}`, 401, "UNAUTHENTICATED"},
	} {
		w := call(tc.p, tc.method, tc.path, tc.body)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) ||
			w.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: %d %q %.200s", tc.name, w.Code, w.Header().Get("Content-Type"), w.Body)
		}
	}
	// authorized caller still gets validation errors
	wr := auth.Principal{Kind: auth.KindAgent, Scopes: []auth.Scope{auth.ScopeWrite}}
	if w := call(wr, "POST", "/api/wr", `{"n":0}`); w.Code != 422 {
		t.Errorf("authorized invalid: %d", w.Code)
	}
}
