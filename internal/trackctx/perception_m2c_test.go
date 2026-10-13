package trackctx_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// lossyBlindSpot is the C60 wording; analysis.BlindSpots is its one source.
const lossyBlindSpot = "analysed from a lossy MP3 decode: bandwidth, clipping and true peak are not comparable with FLAC takes"

func blindSpotsOf(t *testing.T, id string) []string {
	t.Helper()
	e := newEnvM2c(t)
	b, err := e.b.Build(context.Background(), agent, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, b)
	p, _ := rawMap(t, raw)["perception"].(map[string]any)
	var out []string
	for _, v := range p["blind_spots"].([]any) {
		out = append(out, v.(string))
	}
	return out
}

// TestTrackctx_PerceptionLossy: a lossy track's perception.blind_spots carry the lossy text once, right
// after the six fixed entries; a lossless reference track does not.
func TestTrackctx_PerceptionLossy(t *testing.T) {
	t.Parallel()
	if _, ok := reflect.TypeOf(analysis.BlindSpotFacts{}).FieldByName("Lossy"); !ok {
		t.Skip("pending M2c-B2: analysis.BlindSpotFacts.Lossy and the Ears wiring are not merged yet")
	}
	fixed := analysis.BlindSpots(analysis.BlindSpotFacts{})[:6]
	got := blindSpotsOf(t, lt.TrackLossy)
	n := 0
	for _, s := range got {
		if s == lossyBlindSpot {
			n++
		}
	}
	if n != 1 || len(got) < 7 || got[6] != lossyBlindSpot {
		t.Errorf("lossy blind spots %q", got)
	}
	if diff := cmp.Diff(fixed, got[:6]); diff != "" {
		t.Errorf("fixed entries (-want +got):\n%s", diff)
	}
	for _, s := range blindSpotsOf(t, lt.TrackRefFlac) {
		if strings.Contains(s, "lossy") {
			t.Errorf("a lossless reference carries %q", s)
		}
	}
}
