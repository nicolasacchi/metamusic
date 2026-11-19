package acceptance_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

// intbOp is one registered operation with its parameters, read from the registry's OpenAPI document, so a new operation
// or a new query parameter is swept without anyone listing it (U1-INT-B).
type intbOp struct {
	api.OpInfo
	PathParams []string // path parameter names, in order
	Query      []intbParam
	HasBody    bool
	Resp       *huma.Schema // the 2xx body schema
}

type intbParam struct {
	Name     string
	Type     string // string | integer | number | boolean | array
	Enum     []string
	Required bool
}

var (
	intbListener = auth.Principal{Kind: auth.KindHuman, ID: "carol", Name: "carol", User: "carol", Role: auth.RoleListener, Scopes: auth.ScopesFor(auth.RoleListener), Via: "proxy"}
	intbEditor   = auth.Principal{Kind: auth.KindHuman, ID: "bob", Name: "bob", User: "bob", Role: auth.RoleEditor, Scopes: auth.ScopesFor(auth.RoleEditor), Via: "proxy"}
	intbOwner    = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", User: "alice", Role: auth.RoleOwner, Scopes: auth.ScopesFor(auth.RoleOwner), Via: "proxy"}
	// a Listener's phone and Subsonic client: same role, device kind (D-U1-14)
	intbListenerDevice = auth.Principal{Kind: auth.KindDevice, ID: "dev-carol", Name: "phone", User: "carol", Role: auth.RoleListener, Scopes: auth.ScopesFor(auth.RoleListener), Via: "device"}
	intbEditorDevice   = auth.Principal{Kind: auth.KindDevice, ID: "dev-bob", Name: "phone", User: "bob", Role: auth.RoleEditor, Scopes: auth.ScopesFor(auth.RoleEditor), Via: "device"}
)

// intbOps lists every operation of the registry with its parameters and response schema.
func intbOps(tb testing.TB) []intbOp {
	tb.Helper()
	a := api.New(http.NewServeMux(), api.Deps{Version: "x"})
	spec := a.OpenAPI()
	var out []intbOp
	for _, info := range api.Operations(a) {
		item := spec.Paths[info.Path]
		var op *huma.Operation
		switch info.Method {
		case "GET":
			op = item.Get
		case "PUT":
			op = item.Put
		case "POST":
			op = item.Post
		case "DELETE":
			op = item.Delete
		case "PATCH":
			op = item.Patch
		}
		if op == nil {
			tb.Fatalf("operation %s has no %s item", info.ID, info.Method)
		}
		o := intbOp{OpInfo: info, HasBody: op.RequestBody != nil}
		for _, p := range op.Parameters {
			switch p.In {
			case "path":
				o.PathParams = append(o.PathParams, p.Name)
			case "query":
				ip := intbParam{Name: p.Name, Required: p.Required}
				if s := p.Schema; s != nil {
					if s.Ref != "" {
						s = spec.Components.Schemas.SchemaFromRef(s.Ref)
					}
					ip.Type = s.Type
					if s.Type == "array" && s.Items != nil {
						ip.Type = "array"
						s = s.Items
					}
					for _, e := range s.Enum {
						if str, ok := e.(string); ok {
							ip.Enum = append(ip.Enum, str)
						}
					}
				}
				o.Query = append(o.Query, ip)
			}
		}
		for _, code := range []string{"200", "201"} {
			if r := op.Responses[code]; r != nil {
				if mt := r.Content["application/json"]; mt != nil {
					o.Resp = mt.Schema
				}
			}
		}
		slices.SortFunc(o.Query, func(x, y intbParam) int { return strings.Compare(x.Name, y.Name) })
		out = append(out, o)
	}
	return out
}

// allowedFor lists the operations Permitted lets p call.
func allowedFor(ops []intbOp, p auth.Principal) []intbOp {
	var out []intbOp
	for _, o := range ops {
		if api.Permitted(p, o.Meta) == nil {
			out = append(out, o)
		}
	}
	return out
}
