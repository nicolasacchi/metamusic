package runpod_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
)

type doc map[string]any

func loadSnapshot(t testing.TB) doc {
	t.Helper()
	raw, err := os.ReadFile("testdata/openapi.snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var d doc
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d doc) at(path ...string) any {
	var cur any = map[string]any(d)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// deref follows a $ref within the document.
func (d doc) deref(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	if ref, ok := m["$ref"].(string); ok {
		m, _ = d.at(strings.Split(strings.TrimPrefix(ref, "#/"), "/")...).(map[string]any)
	}
	return m
}

// property resolves one property name in a schema, looking through $ref, oneOf and anyOf unions.
func (d doc) property(schema map[string]any, name string) map[string]any {
	schema = d.deref(schema)
	if schema == nil {
		return nil
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		if p, ok := props[name]; ok {
			return d.deref(p)
		}
	}
	for _, k := range []string{"oneOf", "anyOf", "allOf"} {
		if alts, ok := schema[k].([]any); ok {
			for _, a := range alts {
				if p := d.property(d.deref(a), name); p != nil {
					return p
				}
			}
		}
	}
	return nil
}

func (d doc) resolve(schema, path string) bool {
	cur := d.deref(d.at("components", "schemas", schema))
	for _, seg := range strings.Split(path, ".") {
		if cur == nil {
			return false
		}
		cur = d.property(cur, seg)
	}
	return cur != nil
}

func TestClient_ManagementFieldsInSnapshot(t *testing.T) {
	t.Parallel()
	d := loadSnapshot(t)
	for _, op := range runpod.ManagementOperations {
		m, p, _ := strings.Cut(op, " ")
		if d.at("paths", p, strings.ToLower(m)) == nil {
			t.Errorf("operation %s missing from the snapshot", op)
		}
	}
	if len(runpod.ManagementFields) < 40 {
		t.Fatalf("field table has only %d entries", len(runpod.ManagementFields))
	}
	for _, f := range runpod.ManagementFields {
		if f.Schema == "" {
			m, p, _ := strings.Cut(f.Operation, " ")
			found := false
			params, _ := d.at("paths", p, strings.ToLower(m), "parameters").([]any)
			for _, x := range params {
				if pm, _ := x.(map[string]any); pm != nil && pm["name"] == f.Path {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: query parameter %s not in the snapshot", f.Operation, f.Path)
			}
			continue
		}
		if !d.resolve(f.Schema, f.Path) {
			t.Errorf("%s: %s.%s not in the snapshot", f.Operation, f.Schema, f.Path)
		}
	}
}

// TestSnapshot_PinnedFacts checks the facts of C77.2 the plan relies on.
func TestSnapshot_PinnedFacts(t *testing.T) {
	t.Parallel()
	d := loadSnapshot(t)
	create := d.deref(d.at("components", "schemas", "CreateEndpointRequest"))
	req, _ := create["required"].([]any)
	if len(req) != 3 {
		t.Fatalf("required = %v", req)
	}
	if qd := d.property(d.property(create, "scaling"), "queueDelay"); qd == nil || qd["minimum"] != 0.5 {
		t.Fatalf("queueDelay = %v", qd)
	}
	if tm := d.property(create, "timeout"); !strings.Contains(strings.ToLower(tm["description"].(string)), "milliseconds") {
		t.Fatalf("timeout = %v", tm)
	}
	if fb := d.property(create, "flashboot"); len(fb["enum"].([]any)) != 3 {
		t.Fatalf("flashboot = %v", fb)
	}
	if mx := d.property(d.property(create, "workers"), "max"); mx["default"] != float64(3) {
		t.Fatalf("workers.max = %v", mx)
	}
}
