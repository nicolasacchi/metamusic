package analysis_test

import (
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
)

const lossyBlindSpot = "analysed from a lossy MP3 decode: bandwidth, clipping and true peak are not comparable with FLAC takes"

func TestBlindSpots_LossySource(t *testing.T) {
	t.Parallel()
	base := analysis.BlindSpotFacts{TierA: true, HasSemantic: false, Timed: false}
	without := analysis.BlindSpots(base)
	if slices.Contains(without, lossyBlindSpot) {
		t.Fatal("lossy entry present for a non-lossy source")
	}
	base.Lossy = true
	with := analysis.BlindSpots(base)
	i := slices.Index(with, lossyBlindSpot)
	if i != 6 {
		t.Fatalf("lossy entry at %d, want 6 (after the six fixed entries): %q", i, with)
	}
	if len(with) != len(without)+1 {
		t.Fatalf("len %d vs %d", len(with), len(without))
	}
	if !slices.Equal(with[7:], without[6:]) {
		t.Fatalf("conditional entries moved: %q vs %q", with, without)
	}
}
