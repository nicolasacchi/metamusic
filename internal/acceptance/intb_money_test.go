package acceptance_test

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
)

// moneyish reports whether a property name is a money field: the key shapes of D-U1-12 and the plain "cost" family.
func moneyish(k string) bool {
	return isMoneyKey(k) || k == "cost" || k == "cost_basis" || strings.HasPrefix(k, "cost_")
}

// moneyProps walks a response schema and returns the path of every money property that the schema marks as required (a
// property the shaping removes must be optional, so a required money field is one no rule covers).
func moneyProps(spec *huma.OpenAPI, s *huma.Schema, path string, seen map[string]bool, out *[]string) {
	if s == nil {
		return
	}
	if s.Ref != "" {
		if seen[s.Ref] {
			return
		}
		seen[s.Ref] = true
		defer delete(seen, s.Ref)
		s = spec.Components.Schemas.SchemaFromRef(s.Ref)
		if s == nil {
			return
		}
	}
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
	}
	for name, p := range s.Properties {
		if moneyish(name) && req[name] {
			*out = append(*out, path+"."+name)
		}
		moneyProps(spec, p, path+"."+name, seen, out)
	}
	moneyProps(spec, s.Items, path+"[]", seen, out)
	for _, group := range [][]*huma.Schema{s.AllOf, s.AnyOf, s.OneOf} {
		for _, c := range group {
			moneyProps(spec, c, path, seen, out)
		}
	}
	if ap, ok := s.AdditionalProperties.(*huma.Schema); ok {
		moneyProps(spec, ap, path+"{}", seen, out)
	}
}

// moneyAllowedPath is the written reason a required "money" property of a schema a non-Owner may reach is not a leak.
func moneyAllowedPath(op, path string) string {
	switch {
	case strings.HasPrefix(path, ".sequence.") || op == "suggest_sequence":
		return "the sequence cost is a unitless transition score of the arrangement, not money"
	case strings.HasSuffix(op, "_transform") || strings.HasSuffix(op, "_transforms"):
		return "D-U1-18: transforms never spend (CPU quota only); their cost block is a constant zero, and a Listener holds no transform operation"
	}
	return ""
}

// TestIntBMoneyFieldScan (C314.2, plan 3): walk the response schema of every operation a Listener or an Editor may call and
// require every money property to be optional, i.e. covered by a shaping rule; the sweeps then prove the rule removes it.
func TestIntBMoneyFieldScan(t *testing.T) {
	t.Parallel()
	a := api.New(http.NewServeMux(), api.Deps{Version: "x"})
	spec := a.OpenAPI()
	ops := intbOps(t)
	seenOwnerMoney := false
	for _, o := range ops { // the scan finds money fields at all: the Owner-only schemas carry them
		var found []string
		if api.Permitted(intbListener, o.Meta) != nil && api.Permitted(intbOwner, o.Meta) == nil {
			moneyProps(spec, o.Resp, "", map[string]bool{}, &found)
			seenOwnerMoney = seenOwnerMoney || len(found) > 0
		}
	}
	if !seenOwnerMoney {
		t.Fatal("the scan finds no money field in any Owner-only schema: it proves nothing")
	}
	for _, role := range []struct {
		name string
		op   func(intbOp) bool
	}{
		{"listener", func(o intbOp) bool {
			return api.Permitted(intbListener, o.Meta) == nil && api.Permitted(intbListenerDevice, o.Meta) == nil
		}},
		{"editor", func(o intbOp) bool { return api.Permitted(intbEditor, o.Meta) == nil }},
	} {
		for _, o := range ops {
			if !role.op(o) {
				continue
			}
			if role.name == "editor" && editorMoneyOps[o.ID] != "" {
				continue // C310.5: the schema carries the Editor's own estimate and batch cost
			}
			var found []string
			moneyProps(spec, o.Resp, "", map[string]bool{}, &found)
			sort.Strings(found)
			for _, p := range found {
				if moneyAllowedPath(o.ID, p) == "" {
					t.Errorf("%s may call %s and its response schema marks %s as required: no shaping rule covers it", role.name, o.ID, p)
				}
			}
		}
	}
}
