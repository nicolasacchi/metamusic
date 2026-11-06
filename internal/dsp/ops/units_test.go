package ops

import (
	"context"
	"encoding/json"
	"testing"
)

// d5UnitCase is one fixed job of the units table: a 30 s input at 48 kHz (two for the pair kinds), the output length the
// service would estimate, and the resolved params.
type d5UnitCase struct {
	kind    string
	in, out int64
	params  string
	want    int64
}

const sec30 = 30 * 48000

var d5UnitCases = []d5UnitCase{
	{kind: KindGlacier, in: sec30, out: 20 * sec30 / 3, params: `{"factor":20,"start_s":0,"length_s":10,"window_s":0.5,"fade_in_s":4,"fade_out_s":4}`, want: 13259837440},
	{kind: KindShepard, in: sec30, out: 120 * 48000, params: `{"direction":"up","loop_s":8,"layers":5,"period_s":24,"duration_s":120}`, want: 25778020352},
	{kind: KindEnsemble, in: sec30, out: sec30 + 11*48000, params: `{"space":"cathedral","wet_db":-6,"dry_db":-12,"tail_s":20}`, want: 16170596448},
	{kind: KindMaster, in: sec30, out: sec30, params: `{}`, want: 1195200000},
	{kind: KindWiden, in: sec30, out: sec30, params: `{}`, want: 1152000000},
	{kind: KindMorph, in: 2 * sec30, out: 40 * 48000, params: `{"bars":8,"beats_per_bar":4,"bpm_a":92,"bpm_b":128,"semitones_b":-3}`, want: 5931310600},
	{kind: KindPolytempo, in: 2 * sec30, out: 60 * 48000, params: `{"beats_per_bar":4,"bpm_a":90,"bpm_b":120,"converge_bar":12,"unison_bars":8}`, want: 7575521280},
}

// TestUnits_Table pins the work-unit model of one fixed job per kind to committed literals. The meter's budget and the cost model
// share these numbers, so a change is a deliberate recalibration, never a side effect.
func TestUnits_Table(t *testing.T) {
	t.Parallel()
	for _, kind := range Kinds() {
		found := false
		for _, c := range d5UnitCases {
			found = found || c.kind == kind
		}
		if !found {
			t.Errorf("kind %s has no row in the units table", kind)
		}
	}
	for _, c := range d5UnitCases {
		got, ok := Units(c.kind, c.in, c.out, 48000, json.RawMessage(c.params))
		if !ok || got != c.want {
			t.Errorf("%s: %d units (registered %v), want %d", c.kind, got, ok, c.want)
		}
		if !SelfFinishing(c.kind) {
			if bare, _ := d5Registered(c); got-bare != SafetyUnits(c.out) {
				t.Errorf("%s: safety share %d, want %d", c.kind, got-bare, SafetyUnits(c.out))
			}
		}
	}
	if _, ok := Units("nope", 1, 1, 48000, nil); ok {
		t.Error("Units answered for an unregistered kind")
	}
}

func d5Registered(c d5UnitCase) (int64, bool) {
	_, fn, ok := Lookup(c.kind)
	return fn(c.in, c.out, 48000, json.RawMessage(c.params)), ok
}

// TestUnits_MeterWithinModel: on the dispatcher fixtures the meter's total stays within the model (never an under-promise by
// more than 5 %, never wildly above): the model is what the CPU budget is built from.
func TestUnits_MeterWithinModel(t *testing.T) {
	d4SkipHeavy(t)
	for _, kind := range Kinds() {
		j := d5Job(t, kind)
		_, rep, err := Run(context.Background(), j)
		if err != nil {
			t.Fatal(err)
		}
		var in int64
		for _, x := range j.Inputs {
			in += int64(x.Buf.Frames())
		}
		model, _ := Units(kind, in, rep.Frames, d5Rate, j.Params)
		if r := float64(rep.Units) / float64(model); r > 1.05 || r < 0.3 {
			t.Errorf("%s: meter %d units, model %d (ratio %.2f)", kind, rep.Units, model, r)
		}
		t.Logf("%s: meter %d units, model %d (ratio %.2f)", kind, rep.Units, model, float64(rep.Units)/float64(model))
	}
}

// TestOwnFades: glacier and shepard bring their own fades (a shepard output loops, so a safety fade would notch the seam).
func TestOwnFades(t *testing.T) {
	for _, k := range []string{KindGlacier, KindShepard} {
		if !ownFades[k] {
			t.Errorf("%s is not in ownFades", k)
		}
	}
}
