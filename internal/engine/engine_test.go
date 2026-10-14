package engine_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
)

type fake struct{ name string }

func (f fake) Name() string                                            { return f.name }
func (f fake) Caps() engine.Caps                                       { return engine.Caps{MaxSeconds: 60} }
func (f fake) Validate(json.RawMessage) []engine.Issue                 { return nil }
func (f fake) ExpectedAudioSeconds(json.RawMessage) (int, error)       { return 30, nil }
func (f fake) Artifacts(json.RawMessage) (required, optional []string) { return nil, nil }

func TestRegistry_Lookup(t *testing.T) {
	t.Parallel()
	r := engine.Registry{"a": fake{"a"}}
	if e, ok := r["a"]; !ok || e.Name() != "a" || e.Caps().MaxSeconds != 60 {
		t.Fatalf("lookup: %v %v", e, ok)
	}
	if _, ok := r["missing"]; ok {
		t.Fatal("unknown engine found")
	}
}

func TestIssue_Shape(t *testing.T) {
	t.Parallel()
	// the seven fields C70 lists, in order
	want := []string{"Code", "Field", "Severity", "Message", "Line", "Section", "Fatal"}
	typ := reflect.TypeFor[engine.Issue]()
	if typ.NumField() != len(want) {
		t.Fatalf("Issue has %d fields, want %d", typ.NumField(), len(want))
	}
	for i, n := range want {
		if typ.Field(i).Name != n {
			t.Errorf("field %d = %s, want %s", i, typ.Field(i).Name, n)
		}
	}
}
