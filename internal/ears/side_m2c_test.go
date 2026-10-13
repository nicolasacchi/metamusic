package ears

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library"
)

func TestSide_RecipeKnownAndLossy(t *testing.T) {
	t.Parallel()
	known := side(&state{d: library.TrackDetail{ID: "a", RecipeKnown: true}, tier: "A"})
	lossy := side(&state{d: library.TrackDetail{ID: "b", Lossy: true}, tier: "0"})
	if !known.RecipeKnown || known.Lossy {
		t.Errorf("known: %+v", known)
	}
	if lossy.RecipeKnown || !lossy.Lossy {
		t.Errorf("lossy: %+v", lossy)
	}
}
