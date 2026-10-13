package duels

import (
	"encoding/json"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library"
)

// TestBrief_RecipeKnownAndLossy: a revealed slot's brief carries the two flags from the TrackRef (C53); an
// unrevealed slot has no brief at all, so the flags cannot leak slot identity.
func TestBrief_RecipeKnownAndLossy(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name        string
		ref         library.TrackRef
		known, loss bool
	}{
		{"known lossless", library.TrackRef{ID: "a", RecipeKnown: true}, true, false},
		{"reference flac", library.TrackRef{ID: "b"}, false, false},
		{"lossy reference", library.TrackRef{ID: "c", Lossy: true}, false, true},
	} {
		b := brief(c.ref)
		if b.RecipeKnown != c.known || b.Lossy != c.loss {
			t.Errorf("%s: %+v", c.name, b)
		}
		raw, _ := json.Marshal(b)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["recipe_known"] != c.known || m["lossy"] != c.loss {
			t.Errorf("%s json: %s", c.name, raw)
		}
	}
	var s Slot
	raw, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["track"] != nil {
		t.Errorf("an unrevealed slot has a null track: %s", raw)
	}
}
