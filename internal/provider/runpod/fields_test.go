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

// TestClient_ManagementFieldsInSnapshot resolves every operation, query parameter and field of the client's
// table (C77.2) against the committed snapshot with the same resolver the live probe uses, so a renamed
// field in a future spec fails a test instead of silently being ignored.
func TestClient_ManagementFieldsInSnapshot(t *testing.T) {
	t.Parallel()
	if len(runpod.ManagementFields) < 40 {
		t.Fatalf("field table has only %d entries", len(runpod.ManagementFields))
	}
	r, err := runpod.CompareSchema(runpod.Snapshot(), runpod.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.Failures {
		t.Error(f)
	}
}

// TestSnapshot_PinnedFacts checks the facts of C77.2 the plan relies on: the snapshot is the real public
// document (not a reconstruction) and carries them.
func TestSnapshot_PinnedFacts(t *testing.T) {
	t.Parallel()
	var d map[string]any
	if err := json.Unmarshal(runpod.Snapshot(), &d); err != nil {
		t.Fatal(err)
	}
	info, _ := d["info"].(map[string]any)
	if desc, _ := info["description"].(string); strings.Contains(desc, "RECONSTRUCTED") {
		t.Fatal("the snapshot is a reconstructed subset, not the real document")
	}
	if len(d["paths"].(map[string]any)) < 20 {
		t.Fatalf("the snapshot has only %d paths: not the full document", len(d["paths"].(map[string]any)))
	}
	r, err := runpod.CompareSchema(runpod.Snapshot(), runpod.Snapshot())
	if err != nil || !r.OK {
		t.Fatalf("pinned facts: %+v %v", r, err)
	}
	if strings.Join(r.FlashbootValues, ",") != "OFF,FLASHBOOT,PRIORITY_FLASHBOOT" {
		t.Fatalf("flashboot = %v", r.FlashbootValues)
	}
}
