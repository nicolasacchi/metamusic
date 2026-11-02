package nextactions_test

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
)

type openAPI struct {
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas map[string]map[string]any `json:"schemas"`
	} `json:"components"`
}

type opSpec struct {
	Parameters  []map[string]any `json:"parameters"`
	RequestBody struct {
		Content map[string]struct {
			Schema map[string]any `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
	OperationID string         `json:"operationId"`
	XMM         map[string]any `json:"x-mm"`
}

func loadSpec(t *testing.T) (openAPI, map[string]opSpec) {
	t.Helper()
	raw, err := os.ReadFile("../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPI
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ops := map[string]opSpec{}
	for _, item := range doc.Paths {
		for method, rawOp := range item {
			if method == "parameters" {
				continue
			}
			var op opSpec
			if err := json.Unmarshal(rawOp, &op); err != nil {
				t.Fatal(err)
			}
			ops[op.OperationID] = op
		}
	}
	return doc, ops
}

// check validates v against a JSON schema of the generated spec: $ref, type, enum, required, properties with
// additionalProperties false, items and minItems, the keywords the L3 hints touch.
func (d openAPI) check(schema map[string]any, v any, at string) []string {
	if ref, ok := schema["$ref"].(string); ok {
		return d.check(d.Components.Schemas[strings.TrimPrefix(ref, "#/components/schemas/")], v, at)
	}
	var errs []string
	if enum, ok := schema["enum"].([]any); ok && !slices.Contains(enum, v) {
		errs = append(errs, fmt.Sprintf("%s: %v not in %v", at, v, enum))
	}
	types := []string{}
	switch tv := schema["type"].(type) {
	case string:
		types = []string{tv}
	case []any:
		for _, x := range tv {
			types = append(types, x.(string))
		}
	}
	ok := len(types) == 0
	for _, ty := range types {
		switch ty {
		case "string":
			_, isT := v.(string)
			ok = ok || isT
		case "integer", "number":
			switch v.(type) {
			case int, int64, float64:
				ok = true
			}
		case "array":
			_, isT := v.([]any)
			ok = ok || isT
		case "object":
			_, isT := v.(map[string]any)
			ok = ok || isT
		case "null":
			ok = ok || v == nil
		case "boolean":
			_, isT := v.(bool)
			ok = ok || isT
		}
	}
	if !ok {
		return append(errs, fmt.Sprintf("%s: %T is not %v", at, v, types))
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		for _, req := range anyStrings(schema["required"]) {
			if _, has := x[req]; !has {
				errs = append(errs, fmt.Sprintf("%s: missing required %s", at, req))
			}
		}
		for k, val := range x {
			p, known := props[k].(map[string]any)
			if !known {
				if schema["additionalProperties"] == false {
					errs = append(errs, fmt.Sprintf("%s: unknown field %s", at, k))
				}
				continue
			}
			errs = append(errs, d.check(p, val, at+"."+k)...)
		}
	case []any:
		if n, ok := schema["minItems"].(float64); ok && float64(len(x)) < n {
			errs = append(errs, fmt.Sprintf("%s: %d items, minimum %v", at, len(x), n))
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, e := range x {
				errs = append(errs, d.check(items, e, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	}
	return errs
}

func anyStrings(v any) []string {
	var out []string
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			out = append(out, x.(string))
		}
	}
	return out
}

// validateHint checks one emitted hint against the generated spec: the operation exists, costs nothing, needs a scope the viewer
// holds, every argument is a parameter or a body field, the required ones are present and the values satisfy types and enums.
func validateHint(t *testing.T, doc openAPI, ops map[string]opSpec, viewer auth.Principal, h nextactions.Hint) {
	t.Helper()
	op, ok := ops[h.Op]
	if !ok {
		t.Errorf("hint op %s is not in the registry", h.Op)
		return
	}
	if op.XMM["cost"] != "free" {
		t.Errorf("hint op %s costs %v: hints are free operations", h.Op, op.XMM["cost"])
	}
	if sc, _ := op.XMM["scope"].(string); !viewer.Has(auth.Scope(sc)) {
		t.Errorf("hint op %s needs scope %q, which the viewer does not hold", h.Op, sc)
	}
	if op.XMM["human_only"] == true {
		t.Errorf("hint op %s is human only", h.Op)
	}
	params := map[string]map[string]any{}
	for _, p := range op.Parameters {
		params[p["name"].(string)] = p
	}
	body := map[string]any{}
	for k, v := range h.Args {
		if _, isParam := params[k]; !isParam {
			body[k] = v
		}
	}
	for name, p := range params {
		// a required header (Idempotency-Key) is the caller's to send, not a hint argument
		if _, given := h.Args[name]; !given && p["required"] == true && p["in"] != "header" {
			t.Errorf("hint %s: required parameter %s is missing", h.Op, name)
		}
		if v, given := h.Args[name]; given {
			for _, e := range doc.check(p["schema"].(map[string]any), v, name) {
				t.Errorf("hint %s: %s", h.Op, e)
			}
		}
	}
	if js, has := op.RequestBody.Content["application/json"]; has {
		for _, e := range doc.check(js.Schema, body, "body") {
			t.Errorf("hint %s: %s", h.Op, e)
		}
	} else if len(body) > 0 {
		t.Errorf("hint %s: arguments %v are neither parameters nor a body", h.Op, body)
	}
}

// emittedHints runs a rig where every NA1 rule that hints fires and returns the hints of an agent with the given scopes.
func emittedHints(t *testing.T, scopes ...auth.Scope) (nextactions.Page, auth.Principal) {
	t.Helper()
	r := l3Rig(t)
	p := auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: scopes}
	return list(t, r, p, nextactions.Filter{Limit: 50}), p
}

// TestNextActions_HintsAreFree and TestNextActions_HintsValidateAgainstOpenAPI: every hint the rules emit names a registry
// operation with cost free, its arguments satisfy the generated input schema (required fields, types, enums), and every
// operation the viewer's scope forbids is absent.
func TestNextActions_HintsAreFree(t *testing.T) {
	t.Parallel()
	doc, ops := loadSpec(t)
	for _, scopes := range [][]auth.Scope{{auth.ScopeRead}, {auth.ScopeRead, auth.ScopeGenerate}, {auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate}} {
		pg, p := emittedHints(t, scopes...)
		n := 0
		for _, a := range pg.Items {
			if a.Hint != nil {
				n++
				validateHint(t, doc, ops, p, *a.Hint)
			}
		}
		if n < 8 {
			t.Errorf("scopes %v: only %d hints emitted", scopes, n)
		}
		ops := map[string]bool{}
		for _, a := range pg.Items {
			if a.Hint != nil {
				ops[a.Hint.Op] = true
			}
		}
		if len(scopes) == 3 && !(ops["create_duel"] && ops["list_songs"] && ops["get_record"]) {
			t.Errorf("the NA2 hints are not all exercised: %v", ops)
		}
	}
}

func TestNextActions_HintsValidateAgainstOpenAPI(t *testing.T) {
	t.Parallel()
	doc, _ := loadSpec(t)
	// the validator itself must reject what it should: a missing required field, a wrong enum, an unknown field
	body := ops(t)["estimate_generation"].RequestBody.Content["application/json"].Schema
	for name, bad := range map[string]any{
		"no items":   map[string]any{},
		"bad mode":   map[string]any{"items": []any{map[string]any{"mode": "bogus"}}},
		"no mode":    map[string]any{"items": []any{map[string]any{"draft_ids": []any{"d"}}}},
		"unknown":    map[string]any{"items": []any{map[string]any{"mode": "from_draft", "draft_ids": []any{"d"}}}, "extra": 1},
		"empty list": map[string]any{"items": []any{}},
	} {
		if len(doc.check(body, bad, "body")) == 0 {
			t.Errorf("the validator accepts %s", name)
		}
	}
	good := map[string]any{"items": []any{map[string]any{"mode": "from_draft", "draft_ids": []any{"d"}}}}
	if errs := doc.check(body, good, "body"); len(errs) != 0 {
		t.Errorf("the validator rejects a valid body: %v", errs)
	}
}

func ops(t *testing.T) map[string]opSpec {
	_, o := loadSpec(t)
	return o
}

// TestNextActions_ScopeFiltersHints: muse_unrendered hints the free estimate only for a viewer who may generate, and get_muse
// otherwise; no hint names an operation beyond the viewer's scope.
func TestNextActions_ScopeFiltersHints(t *testing.T) {
	t.Parallel()
	r := l3Rig(t)
	m3 := museByTitle(t, r, lt.L3Muse3Title)
	d1 := str(t, r, `SELECT draft_id FROM songs WHERE title = 'Synthetic Song d1'`)
	for name, tc := range map[string]struct {
		scopes []auth.Scope
		op     string
	}{"read only": {[]auth.Scope{auth.ScopeRead}, "get_muse"}, "may generate": {[]auth.Scope{auth.ScopeRead, auth.ScopeGenerate}, "estimate_generation"}} {
		p := auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: tc.scopes}
		as := byCode(list(t, r, p, nextactions.Filter{SubjectKind: "muse", SubjectID: m3}), "muse_unrendered")
		if len(as) != 1 || as[0].Hint == nil || as[0].Hint.Op != tc.op {
			t.Fatalf("%s: %+v", name, as)
		}
		if tc.op == "estimate_generation" {
			want := map[string]any{"items": []any{map[string]any{"mode": "from_draft", "draft_ids": []any{d1}}}}
			if fmt.Sprint(as[0].Hint.Args) != fmt.Sprint(want) {
				t.Errorf("args %v, want %v", as[0].Hint.Args, want)
			}
		} else if as[0].Hint.Args["id"] != m3 {
			t.Errorf("args %v", as[0].Hint.Args)
		}
	}
}
