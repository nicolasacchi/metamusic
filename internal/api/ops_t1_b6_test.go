package api_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
)

// TestSurface_T1B6 pins the schema shapes the B6 readers fill (C211.3, C211.4): search_tracks parameters, the transform
// block of TrackSummary, TrackDetail and LineageNode, also_from, and the top-level transform of the context bundle.
func TestSurface_T1B6(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	b, err := api.OpenAPIJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name   string `json:"name"`
				Schema struct {
					Items struct {
						Enum []string `json:"enum"`
					} `json:"items"`
				} `json:"schema"`
			} `json:"parameters"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	params := map[string][]string{}
	for _, p := range spec.Paths["/api/tracks"]["get"].Parameters {
		params[p.Name] = p.Schema.Items.Enum
	}
	if _, ok := params["lyrics_of"]; !ok {
		t.Error("search_tracks has no lyrics_of parameter")
	}
	if got := params["origin"]; !slices.Equal(got, []string{"import", "job", "transform"}) {
		t.Errorf("origin enum = %v", got)
	}
	has := func(schema, prop string) {
		t.Helper()
		if _, ok := spec.Components.Schemas[schema].Properties[prop]; !ok {
			t.Errorf("%s has no %s", schema, prop)
		}
	}
	required := func(schema, prop string) {
		t.Helper()
		if !slices.Contains(spec.Components.Schemas[schema].Required, prop) {
			t.Errorf("%s.%s is not required", schema, prop)
		}
	}
	for _, s := range []string{"TrackSummary", "TrackDetail", "LineageNode"} {
		has(s, "transform")
		required(s, "transform")
	}
	has("LineageNode", "also_from")
	required("LineageNode", "also_from")
	has("ContextBundle", "transform")
	if slices.Contains(spec.Components.Schemas["ContextBundle"].Required, "transform") {
		t.Error("ContextBundle.transform must be optional")
	}
	for _, p := range []string{"kind", "transform_id", "spec_hash"} {
		has("TransformRef", p)
	}
	for _, p := range []string{"track_id", "role"} {
		has("AlsoFrom", p)
	}
}
