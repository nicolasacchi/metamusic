package features_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// TestFeatures_DspMeasuredOnly: a transform Take takes no declared, plan, style or facet input (D-T1-24): bare, it has no
// values and no predicted energy; measured, it carries measured bases only, whatever its parent declares.
func TestFeatures_DspMeasuredOnly(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	decl := 92
	parent := w.track(w.recipe("reggae, 92 bpm, chill", ""), trackOpts{Declared: &decl, AgeMin: 3})
	bare := librarytest.InsertTransformTake(t, w.st, librarytest.FixedNow, librarytest.TransformTake{N: 1, Kind: "glacier", Parent: parent})
	meas := librarytest.InsertTransformTake(t, w.st, librarytest.FixedNow, librarytest.TransformTake{N: 2, Kind: "widen", Parent: parent})
	w.tempo(meas, 100)
	w.key(meas, 9, "minor")
	w.energy(meas, 0.4)
	w.settle()
	ctx := context.Background()
	r, ok, err := w.svc.Take(ctx, bare)
	if err != nil || !ok {
		t.Fatalf("bare: ok=%v err=%v", ok, err)
	}
	if r.BPMx100 != nil || r.Key != nil || r.Energyx10000 != nil || r.BPMRequestedx100 != nil {
		t.Errorf("a bare transform Take has values: %+v", r)
	}
	r, _, _ = w.svc.Take(ctx, meas)
	if r.BPMBasis != "measured" || r.KeyBasis != "measured" || r.EnergyBasis != "measured" || r.BPMRequestedx100 != nil {
		t.Errorf("measured transform Take: %+v", r)
	}
	if pr, _, _ := w.svc.Take(ctx, parent); pr.BPMBasis != "declared" {
		t.Errorf("the parent is still declared: %+v", pr)
	}
	w.verifyEmpty()
}
