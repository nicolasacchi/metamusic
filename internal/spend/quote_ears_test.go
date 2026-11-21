package spend

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/money"
)

// earsParams are the C339.2 calibration: load 40 s, 150 and 40 milli per audio second.
const earsLoadS, earsRTFMilli, earsTrRTFMilli = 40, 150, 40

func earsIn(audio int, full, transcribe bool) TakeIn {
	return TakeIn{AudioS: audio, Full: full, EarsS: EarsExpectedS(audio, earsLoadS, earsRTFMilli, earsTrRTFMilli, transcribe)}
}

func TestEarsExpectedS_Budget(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		audio      int
		transcribe bool
		ears, bud  int
	}{
		{216, false, 73, 110}, {216, true, 82, 123}, {0, false, 40, 60}, {1, false, 41, 62},
	} {
		got := EarsExpectedS(c.audio, earsLoadS, earsRTFMilli, earsTrRTFMilli, c.transcribe)
		if got != c.ears || EarsBudgetS(got) != c.bud {
			t.Errorf("audio %d transcribe %v: ears_s %d budget %d, want %d %d", c.audio, c.transcribe, got, EarsBudgetS(got), c.ears, c.bud)
		}
	}
}

// TestQuoteTakes_TierB pins the C339.2 goldens: Tier B adds its budget to the timeout and its expected seconds to the expectation,
// so the reservation (take = timeout + kill slack) covers the worst case including the listening check.
func TestQuoteTakes_TierB(t *testing.T) {
	t.Parallel()
	plain := QuoteTakes(goldenParams, []TakeIn{{AudioS: 216}}).Takes[0]
	if plain.ExpectedS != 167 || plain.TimeoutS != 332 || plain.TakeMicro != 66509 || plain.MaxMicro != 104843 || plain.ExpectedMicro != 32009 || plain.EarsBudgetS != 0 {
		t.Fatalf("plain take drifted: %+v", plain)
	}
	q := QuoteTakes(goldenParams, []TakeIn{earsIn(216, false, false)})
	tc := q.Takes[0]
	for name, p := range map[string][2]int64{
		"expected_s": {int64(tc.ExpectedS), 240}, "timeout_s": {int64(tc.TimeoutS), 442}, "take": {int64(tc.TakeMicro), 87592},
		"max": {int64(tc.MaxMicro), 125926}, "expected": {int64(tc.ExpectedMicro), 46000}, "budget": {int64(tc.EarsBudgetS), 110},
		"gen_s": {int64(tc.GenS), 157}, "gen_budget": {int64(tc.GenBudgetS), 262},
	} {
		if p[0] != p[1] {
			t.Errorf("%s = %d, want %d", name, p[0], p[1])
		}
	}
	if tc.GenBudgetS != plain.GenBudgetS {
		t.Errorf("render window %d != /1 window %d", tc.GenBudgetS, plain.GenBudgetS)
	}
	if tc.TakeMicro <= plain.TakeMicro {
		t.Errorf("the reservation %d does not cover the listening check (plain %d)", tc.TakeMicro, plain.TakeMicro)
	}
	if want := money.ForSeconds(int64(tc.TimeoutS)+int64(goldenParams.KillSlackS), goldenParams.PricePPH); tc.TakeMicro != want {
		t.Errorf("take = %d, want the timeout's worth %d", tc.TakeMicro, want)
	}

	full := QuoteTakes(goldenParams, []TakeIn{earsIn(216, true, true)}).Takes[0]
	if full.GenS != 205 || full.ExpectedS != 297 || full.TimeoutS != 531 || full.MaxMicro != 142984 || full.EarsBudgetS != 123 {
		t.Errorf("cot=full with transcription: %+v", full)
	}

	three := QuoteTakes(goldenParams, []TakeIn{earsIn(216, false, false), earsIn(216, false, false), earsIn(216, false, false)})
	for name, p := range map[string][2]int64{
		"max": {int64(three.MaxMicro), 377778}, "retry": {int64(three.RetryAllowanceMicro), 251852},
		"ceiling": {int64(three.CeilingMicro), 629630}, "expected": {int64(three.ExpectedMicro), 176334},
	} {
		if p[0] != p[1] {
			t.Errorf("three takes %s = %d, want %d", name, p[0], p[1])
		}
	}
	noEars := QuoteTakes(goldenParams, []TakeIn{{AudioS: 216}, {AudioS: 216}, {AudioS: 216}})
	if noEars.CeilingMicro != 524215 {
		t.Errorf("three plain takes ceiling = %d, want 524215", noEars.CeilingMicro)
	}
}

func TestTierBFits(t *testing.T) {
	t.Parallel()
	p := goldenParams
	if !TierBFits(p, earsIn(216, false, false)) {
		t.Error("216 s melody take should fit 900 s")
	}
	p.MaxRuntimeS = 400
	if TierBFits(p, earsIn(216, false, false)) {
		t.Error("216 s take fits 400 s: 442 > 400")
	}
	if TierBFits(goldenParams, earsIn(600, true, true)) {
		t.Error("600 s cot=full with transcription must not fit 900 s")
	}
	// a /1 timeout clamped by the cap never fits, even if the sum would
	p = goldenParams
	p.MaxRuntimeS = 332 // exactly the /1 timeout: not clamped, but no room for the budget
	if TierBFits(p, earsIn(216, false, false)) {
		t.Error("no room for the budget")
	}
	p.RTFMilli = 2000 // the /1 timeout would be 1000+ and is clamped to 900
	if TierBFits(p, TakeIn{AudioS: 360, EarsS: 1}) {
		t.Error("a clamped /1 timeout must not fit")
	}
	big := QuoteTakes(goldenParams, []TakeIn{earsIn(600, true, true)}).Takes[0]
	if big.TimeoutS != 900+231 {
		t.Errorf("600 s cot=full timeout = %d, want 1131 (clamped /1 + budget)", big.TimeoutS)
	}
}

// TestDeadlines_TierBKeepsRenderWindow: whenever a take fits, the render window (timeout - finish margin - upload budget - ears
// budget) equals the /1 window of the same take, for every audio length.
func TestDeadlines_TierBKeepsRenderWindow(t *testing.T) {
	t.Parallel()
	for audio := 10; audio <= 600; audio += 7 {
		for _, full := range []bool{false, true} {
			for _, tr := range []bool{false, true} {
				in := earsIn(audio, full, tr)
				if !TierBFits(goldenParams, in) {
					continue
				}
				got := QuoteTakes(goldenParams, []TakeIn{in}).Takes[0]
				base := QuoteTakes(goldenParams, []TakeIn{{AudioS: audio, Full: full}}).Takes[0]
				if w := got.TimeoutS - WorkerFinishMarginS - UploadBudgetS - got.EarsBudgetS; w != base.GenBudgetS || got.GenBudgetS != base.GenBudgetS {
					t.Errorf("audio %d full %v tr %v: window %d / %d, /1 window %d", audio, full, tr, w, got.GenBudgetS, base.GenBudgetS)
				}
				if got.TimeoutS != base.TimeoutS+got.EarsBudgetS || got.ExpectedS != base.ExpectedS+in.EarsS {
					t.Errorf("audio %d: timeout %d expected %d", audio, got.TimeoutS, got.ExpectedS)
				}
			}
		}
	}
}
