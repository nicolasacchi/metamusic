package trackctx_test

import (
	"context"
	"encoding/json"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// TestContext_Transform: a transform Take's context carries the top-level transform block (kind, inputs with roles,
// alignment sources, safety summary); every other Take's context has no such key.
func TestContext_Transform(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	info := lt.FixtureT1(t, e.st, lt.FixedNow)
	a, b := info.Tx["A"], info.Tx["B"]
	m := lt.InsertTransformTake(t, e.st, lt.FixedNow, lt.TransformTake{N: 1, Kind: "morph", Parent: a, ParentRole: "a", Also: []lt.TransformInput{{Role: "b", Track: b}}})
	bundle, err := e.b.Build(context.Background(), human, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, bundle)
	var doc struct {
		Provenance map[string]any `json:"provenance"`
		Transform  struct {
			Kind        string `json:"kind"`
			TransformID string `json:"transform_id"`
			SpecHash    string `json:"spec_hash"`
			Inputs      []struct {
				Ord     int    `json:"ord"`
				Role    string `json:"role"`
				TrackID string `json:"track_id"`
			} `json:"inputs"`
			Alignment []any          `json:"alignment"`
			Params    map[string]any `json:"params"`
			Safety    map[string]any `json:"safety"`
		} `json:"transform"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	tr := doc.Transform
	if tr.Kind != "morph" || tr.TransformID != m || len(tr.SpecHash) != 64 || tr.Alignment == nil || tr.Params == nil || tr.Safety == nil {
		t.Errorf("transform block = %+v", tr)
	}
	if len(tr.Inputs) != 2 || tr.Inputs[0].Role != "a" || tr.Inputs[0].TrackID != a || tr.Inputs[1].Role != "b" || tr.Inputs[1].TrackID != b {
		t.Errorf("inputs = %+v", tr.Inputs)
	}
	if doc.Provenance["origin"] != "transform" {
		t.Errorf("provenance.origin = %v", doc.Provenance["origin"])
	}
	// a plain Take has no transform key at all
	plain, err := e.b.Build(context.Background(), human, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys(t, marshal(t, plain)) {
		if k == "transform" {
			t.Error("a plain Take's context has a transform key")
		}
	}
}
