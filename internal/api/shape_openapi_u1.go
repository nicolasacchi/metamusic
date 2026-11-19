package api

import (
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/view"
)

// relaxResponseSchemas makes the keys the shaping rules can remove optional in the response schemas (D-U1-12: absent, never
// zeroed), so the OpenAPI document, the generated SPA types and the rules cannot drift: a key a role may not receive is
// `required` for no response schema that names it. The Go types stay as they were, so the Owner side's body is unchanged.
// A component that any request body or parameter also uses is left alone, so request validation is never loosened.
func relaxResponseSchemas(a huma.API) {
	oapi := a.OpenAPI()
	reg := oapi.Components.Schemas
	inRequest := map[string]bool{}
	for _, item := range oapi.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Put, item.Post, item.Delete, item.Patch} {
			if op == nil {
				continue
			}
			if op.RequestBody != nil {
				for _, mt := range op.RequestBody.Content {
					markComponents(reg, mt.Schema, inRequest, map[*huma.Schema]bool{})
				}
			}
			for _, p := range op.Parameters {
				markComponents(reg, p.Schema, inRequest, map[*huma.Schema]bool{})
			}
		}
	}
	for _, item := range oapi.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Put, item.Post, item.Delete, item.Patch} {
			m, ok := OpMeta(op)
			if !ok || m.OwnerOnly { // an owner-only operation is never shaped for a role, so its schema stays as typed
				continue
			}
			rules := rulesFor(op.OperationID, m)
			var paths []string
			for _, l := range [][]string{rules.Cost, rules.Muse, rules.Private, rules.Editor} {
				paths = append(paths, l...)
			}
			for status, resp := range op.Responses {
				if len(status) == 0 || status[0] != '2' || resp == nil {
					continue
				}
				for _, mt := range resp.Content {
					for _, p := range paths {
						relaxPath(reg, inRequest, mt.Schema, strings.Split(p, "."), 0)
					}
					if rules.MoneyKeys {
						relaxWhere(reg, inRequest, mt.Schema, view.MoneyKey, map[*huma.Schema]bool{})
					}
				}
			}
		}
	}
}

// deref resolves a $ref to its component and name; a plain schema is returned as is with an empty name.
func refTarget(reg huma.Registry, s *huma.Schema) (*huma.Schema, string) {
	if s == nil || s.Ref == "" {
		return s, ""
	}
	return reg.SchemaFromRef(s.Ref), strings.TrimPrefix(s.Ref, "#/components/schemas/")
}

// children lists the sub-schemas a walk follows: items, the options of a union and the properties.
func children(s *huma.Schema) (opts []*huma.Schema) {
	opts = append(opts, s.AnyOf...)
	opts = append(opts, s.OneOf...)
	opts = append(opts, s.AllOf...)
	if s.Items != nil {
		opts = append(opts, s.Items)
	}
	return opts
}

func markComponents(reg huma.Registry, s *huma.Schema, seen map[string]bool, visited map[*huma.Schema]bool) {
	r, name := refTarget(reg, s)
	if r == nil || visited[r] {
		return
	}
	visited[r] = true
	if name != "" {
		seen[name] = true
	}
	for _, c := range children(r) {
		markComponents(reg, c, seen, visited)
	}
	for _, c := range r.Properties {
		markComponents(reg, c, seen, visited)
	}
}

// dropRequired removes name from the required list of s unless s is a component a request also uses.
func dropRequired(s *huma.Schema, comp string, inRequest map[string]bool, name string) {
	if comp != "" && inRequest[comp] {
		return
	}
	s.Required = slices.DeleteFunc(s.Required, func(r string) bool { return r == name })
}

// relaxPath follows a rule path through s (arrays and unions transparently, "**" at any depth) and drops the last key from
// the required list of the object that holds it.
func relaxPath(reg huma.Registry, inRequest map[string]bool, s *huma.Schema, segs []string, depth int) {
	if len(segs) == 0 {
		return
	}
	head, rest := segs[0], segs[1:]
	r, comp := refTarget(reg, s)
	if r == nil || depth > 16 {
		return
	}
	for _, c := range children(r) {
		relaxPath(reg, inRequest, c, segs, depth+1)
	}
	switch {
	case head == "**":
		relaxPath(reg, inRequest, s, rest, depth+1)
		for _, c := range r.Properties {
			relaxPath(reg, inRequest, c, segs, depth+1)
		}
	case len(rest) == 0:
		if _, ok := r.Properties[head]; ok {
			dropRequired(r, comp, inRequest, head)
		}
	default:
		if c, ok := r.Properties[head]; ok {
			relaxPath(reg, inRequest, c, rest, depth+1)
		}
	}
}

// relaxWhere drops every property whose name satisfies pred from the required lists under s.
func relaxWhere(reg huma.Registry, inRequest map[string]bool, s *huma.Schema, pred func(string) bool, visited map[*huma.Schema]bool) {
	r, comp := refTarget(reg, s)
	if r == nil || visited[r] {
		return
	}
	visited[r] = true
	for _, c := range children(r) {
		relaxWhere(reg, inRequest, c, pred, visited)
	}
	for name, c := range r.Properties {
		if pred(name) {
			dropRequired(r, comp, inRequest, name)
		}
		relaxWhere(reg, inRequest, c, pred, visited)
	}
}
