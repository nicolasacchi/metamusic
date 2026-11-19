package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

// resolves reports whether a rule path names a property somewhere under s (arrays and unions transparently, "**" at any
// depth), the same walk relaxPath and view.Shape follow.
func resolves(reg huma.Registry, s *huma.Schema, segs []string, depth int) bool {
	r, _ := refTarget(reg, s)
	if r == nil || len(segs) == 0 || depth > 16 {
		return false
	}
	for _, c := range children(r) {
		if resolves(reg, c, segs, depth+1) {
			return true
		}
	}
	head, rest := segs[0], segs[1:]
	if head == "**" {
		if resolves(reg, s, rest, depth+1) {
			return true
		}
		for _, c := range r.Properties {
			if resolves(reg, c, segs, depth+1) {
				return true
			}
		}
		return false
	}
	c, ok := r.Properties[head]
	if !ok {
		return open(r) // a free-form object (a map) may carry the key at runtime; the schema cannot say
	}
	return len(rest) == 0 || resolves(reg, c, rest, depth+1)
}

// open reports whether s is an object that admits keys it does not list (additionalProperties neither absent nor false).
func open(s *huma.Schema) bool {
	if s.AdditionalProperties == nil {
		return false
	}
	b, isBool := s.AdditionalProperties.(bool)
	return !isBool || b
}

// TestShapeRulesResolve pins the shaping rules to the response schemas: every operation shapeRules names is registered,
// and every path of its rules names a property of one of its 2xx response bodies. A renamed field or operation would
// otherwise silently stop being removed for an Editor or a Listener (C308.3).
func TestShapeRulesResolve(t *testing.T) {
	t.Parallel()
	a := New(http.NewServeMux(), Deps{Version: "x"})
	reg := a.OpenAPI().Components.Schemas
	ops := map[string]*huma.Operation{}
	for _, item := range a.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Get, item.Put, item.Post, item.Delete, item.Patch} {
			if op != nil {
				ops[op.OperationID] = op
			}
		}
	}
	for id, rules := range shapeRules {
		op, ok := ops[id]
		if !ok {
			t.Errorf("shapeRules names %q, which is not a registered operation", id)
			continue
		}
		var paths []string
		for _, l := range [][]string{rules.Cost, rules.Muse, rules.Private, rules.Editor, rules.Empty} {
			paths = append(paths, l...)
		}
		for _, p := range paths {
			found := false
			for status, resp := range op.Responses {
				if len(status) == 0 || status[0] != '2' || resp == nil {
					continue
				}
				for _, mt := range resp.Content {
					if resolves(reg, mt.Schema, strings.Split(p, "."), 0) {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("%s: rule path %q names no property of the response", id, p)
			}
		}
	}
}

// TestResolvesWalk covers the walk itself: a nested hit, a miss, "**", an array, a union and a cycle.
func TestResolvesWalk(t *testing.T) {
	t.Parallel()
	reg := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	leaf := &huma.Schema{Properties: map[string]*huma.Schema{"seed": {}}}
	loop := &huma.Schema{Properties: map[string]*huma.Schema{}}
	loop.Properties["self"] = loop
	root := &huma.Schema{Properties: map[string]*huma.Schema{
		"items": {Items: leaf},
		"u":     {AnyOf: []*huma.Schema{{}, leaf}},
		"loop":  loop,
		"free":  {Type: "object", AdditionalProperties: true},
		"shut":  {Type: "object", AdditionalProperties: false},
		"mapof": {Type: "object", AdditionalProperties: &huma.Schema{}},
	}}
	for p, want := range map[string]bool{
		"items.seed": true, "u.seed": true, "**.seed": true, "items.nope": false, "nope": false, "loop.self.x": false, "": false,
		"free.cost": true, "shut.cost": false, "mapof.cost": true,
	} {
		segs := strings.Split(p, ".")
		if p == "" {
			segs = nil
		}
		if got := resolves(reg, root, segs, 0); got != want {
			t.Errorf("resolves(%q) = %v, want %v", p, got, want)
		}
	}
}
