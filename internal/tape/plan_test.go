package tape_test

import (
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/tape"
)

func ms(lufs, tp float64) tape.Measure { return tape.Measure{LUFS: lufs, TruePeakDBTP: tp, OK: true} }

var unmeasured = tape.Measure{}

func fp(v float64) *float64 { return &v }

type want struct {
	gain      int
	status    tape.GainStatus
	predicted *float64 // nil = none
}

// TestPlanGains_Table is G01-G17 of C128.2 with the exact integers stated there.
func TestPlanGains_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		in     []tape.Measure
		target *float64
		wantT  float64
		want   []want
	}{
		{"G01 one item at the target", []tape.Measure{ms(-14.0, -3)}, nil, -14.0, []want{{0, tape.GainMatched, fp(-14.0)}}},
		{"G02 median -13.0 clamps to -14.0", []tape.Measure{ms(-15.5, -2.0), ms(-13.0, -1.5), ms(-11.8, 0.6)}, nil, -14.0,
			[]want{{100, tape.GainPeakLimited, fp(-14.5)}, {-100, tape.GainMatched, fp(-14.0)}, {-220, tape.GainMatched, fp(-14.0)}}},
		{"G03 median -22.0, boost clamp and a cut", []tape.Measure{ms(-30, -12), ms(-14, -3)}, nil, -22.0,
			[]want{{600, tape.GainBoostClamped, fp(-24.0)}, {-800, tape.GainMatched, fp(-22.0)}}},
		{"G04 explicit target, limit exactly met", []tape.Measure{ms(-20, -5.0)}, fp(-16.0), -16.0, []want{{400, tape.GainMatched, fp(-16.0)}}},
		{"G05 silent item excluded from the median", []tape.Measure{ms(-70, -40), ms(-16, -3), ms(-12, -3)}, nil, -14.0,
			[]want{{0, tape.GainSilent, nil}, {200, tape.GainMatched, fp(-14.0)}, {-200, tape.GainMatched, fp(-14.0)}}},
		{"G06 unmeasured item excluded", []tape.Measure{unmeasured, ms(-16, -3)}, nil, -16.0, []want{{0, tape.GainUnmeasured, nil}, {0, tape.GainMatched, fp(-16.0)}}},
		{"G07 all unmeasured", []tape.Measure{unmeasured, unmeasured}, nil, -14.0, []want{{0, tape.GainUnmeasured, nil}, {0, tape.GainUnmeasured, nil}}},
		{"G08 cut clamp", []tape.Measure{ms(-1.0, -3)}, fp(-14.0), -14.0, []want{{-1200, tape.GainCutClamped, fp(-13.0)}}},
		{"G09 peak guard on a hot item", []tape.Measure{ms(-14.0, 0.6)}, fp(-14.0), -14.0, []want{{-160, tape.GainPeakLimited, fp(-15.6)}}},
		{"G10 median of two", []tape.Measure{ms(-16, -3), ms(-14, -3)}, nil, -15.0, []want{{100, tape.GainMatched, fp(-15.0)}, {-100, tape.GainMatched, fp(-15.0)}}},
		{"G11 sub-centi-dB noise rounds, not truncates", []tape.Measure{ms(-14.04, -3)}, fp(-14.0), -14.0, []want{{4, tape.GainMatched, fp(-14.0)}}},
		{"G13a median exactly at the lower clamp", []tape.Measure{ms(-23.0, -9)}, nil, -23.0, []want{{0, tape.GainMatched, fp(-23.0)}}},
		{"G13b median exactly at the upper clamp", []tape.Measure{ms(-14.0, -9)}, nil, -14.0, []want{{0, tape.GainMatched, fp(-14.0)}}},
		{"G13c median below the lower clamp", []tape.Measure{ms(-25.0, -9)}, nil, -23.0, []want{{200, tape.GainMatched, fp(-23.0)}}},
		{"G13d median above the upper clamp", []tape.Measure{ms(-9.0, -9)}, nil, -14.0, []want{{-500, tape.GainMatched, fp(-14.0)}}},
		{"G14 peak-limited below the cut floor: the cut clamp wins", []tape.Measure{ms(-14.0, 12.0)}, fp(-14.0), -14.0, []want{{-1200, tape.GainCutClamped, fp(-26.0)}}},
		{"G14b true peak +11 puts the limit exactly on the floor", []tape.Measure{ms(-14.0, 11.0)}, fp(-14.0), -14.0, []want{{-1200, tape.GainCutClamped, fp(-26.0)}}},
		{"G15 the float form of the guard gives 89, the integer 90", []tape.Measure{ms(-16.0, -1.9)}, fp(-14.0), -14.0, []want{{90, tape.GainPeakLimited, fp(-15.1)}}},
		{"G16 limit 120", []tape.Measure{ms(-16.5, -2.2)}, fp(-14.0), -14.0, []want{{120, tape.GainPeakLimited, fp(-15.3)}}},
		{"G17 limit 0", []tape.Measure{ms(-14.04, -1.0)}, fp(-14.0), -14.0, []want{{0, tape.GainPeakLimited, fp(-14.04)}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			gotT, plans := tape.PlanGains(c.in, c.target)
			if gotT != c.wantT {
				t.Errorf("target %v, want %v", gotT, c.wantT)
			}
			if len(plans) != len(c.want) {
				t.Fatalf("%d plans, want %d", len(plans), len(c.want))
			}
			for i, w := range c.want {
				p := plans[i]
				if p.GainCDB != w.gain || p.Status != w.status {
					t.Errorf("item %d: gain %d %s, want %d %s", i, p.GainCDB, p.Status, w.gain, w.status)
				}
				switch {
				case w.predicted == nil && p.PredictedLUFS != nil:
					t.Errorf("item %d: predicted %v, want none", i, *p.PredictedLUFS)
				case w.predicted != nil && p.PredictedLUFS == nil:
					t.Errorf("item %d: no prediction, want %v", i, *w.predicted)
				case w.predicted != nil && math.Abs(*p.PredictedLUFS-*w.predicted) > 1e-9:
					t.Errorf("item %d: predicted %v, want %v", i, *p.PredictedLUFS, *w.predicted)
				}
			}
		})
	}
}

// TestPlanGains_PredictedIsLUFSPlusGain is G12: for every non-silent measured item predicted = LUFS + gain/100.
func TestPlanGains_PredictedIsLUFSPlusGain(t *testing.T) {
	t.Parallel()
	in := []tape.Measure{ms(-17.3, -2.1), ms(-12.25, -0.4), ms(-14.04, -1.0), ms(-21.9, -6), ms(-70, -50), unmeasured}
	_, plans := tape.PlanGains(in, nil)
	for i, p := range plans {
		switch in[i] {
		case unmeasured, in[4]:
			if p.PredictedLUFS != nil || p.GainCDB != 0 {
				t.Errorf("item %d: %+v", i, p)
			}
			continue
		}
		cl := int(math.Round(in[i].LUFS * 100))
		if want := float64(cl+p.GainCDB) / 100; p.PredictedLUFS == nil || *p.PredictedLUFS != want {
			t.Errorf("item %d: predicted %v, want %v", i, p.PredictedLUFS, want)
		}
	}
}

// TestPlanGains_TargetRounding: the caller's target is rounded to 0.1 LU first (half away from zero), so float noise cannot
// multiply tape keys; an explicit target is not clamped (the export validates its range).
func TestPlanGains_TargetRounding(t *testing.T) {
	t.Parallel()
	one := []tape.Measure{ms(-20, -9)}
	for _, c := range []struct{ in, want float64 }{
		{-16.04, -16.0}, {-16.05, -16.1}, {-16.0499999, -16.0}, {-15.95, -16.0}, {-9.0, -9.0}, {-30.0, -30.0}, {-14.0000001, -14.0}, {-12.349, -12.3}, {-12.35, -12.4},
	} {
		gotT, plans := tape.PlanGains(one, fp(c.in))
		if gotT != c.want {
			t.Errorf("target %v -> %v, want %v", c.in, gotT, c.want)
		}
		wantGain := int(math.Round(c.want*100)) + 2000
		if wantGain > 600 {
			wantGain = 600
		}
		if plans[0].GainCDB != wantGain {
			t.Errorf("target %v: gain %d, want %d", c.in, plans[0].GainCDB, wantGain)
		}
	}
}

func TestPlanGains_Degenerate(t *testing.T) {
	t.Parallel()
	if gotT, plans := tape.PlanGains(nil, nil); gotT != -14.0 || len(plans) != 0 {
		t.Errorf("no items: %v %v", gotT, plans)
	}
	// non-finite and absurd measurements are unmeasured, never a panic or an overflowing conversion
	for _, m := range []tape.Measure{ms(math.NaN(), -1), ms(math.Inf(-1), -1), ms(-14, math.NaN()), ms(-14, math.Inf(1)), ms(1e300, 0)} {
		_, plans := tape.PlanGains([]tape.Measure{m}, nil)
		if plans[0].Status != tape.GainUnmeasured || plans[0].GainCDB != 0 || plans[0].PredictedLUFS != nil {
			t.Errorf("%+v: %+v", m, plans[0])
		}
	}
	// a non-finite caller target falls back to the automatic one
	for _, bad := range []float64{math.NaN(), math.Inf(1), -1e12} {
		one := []tape.Measure{ms(-18, -6)}
		if gotT, plans := tape.PlanGains(one, fp(bad)); gotT != -18.0 || plans[0].GainCDB != 0 {
			t.Errorf("target %v: %v %+v", bad, gotT, plans[0])
		}
	}
	// the input is not modified and the result does not alias it
	in := []tape.Measure{ms(-15, -3), ms(-12, -3)}
	cp := append([]tape.Measure(nil), in...)
	tape.PlanGains(in, nil)
	if in[0] != cp[0] || in[1] != cp[1] {
		t.Error("PlanGains modified its input")
	}
}

// TestAutoTarget_Table: odd and even medians, the clamp edges, silent and unmeasured excluded, none => -14.0, and the
// round-half-away-from-zero of the last decimal.
func TestAutoTarget_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		in   []tape.Measure
		want float64
	}{
		{"odd count", []tape.Measure{ms(-20, -3), ms(-18, -3), ms(-17, -3)}, -18.0},
		{"even count averages the middle two", []tape.Measure{ms(-20, -3), ms(-18, -3), ms(-17, -3), ms(-16, -3)}, -17.5},
		{"unsorted input", []tape.Measure{ms(-16, -3), ms(-20, -3), ms(-17, -3)}, -17.0},
		{"even count, half rounds away from zero", []tape.Measure{ms(-17.0, -3), ms(-17.1, -3)}, -17.1},
		{"even count, half of a tenth", []tape.Measure{ms(-17.04, -3), ms(-17.05, -3)}, -17.0},
		{"one measurement of two decimals", []tape.Measure{ms(-15.05, -3)}, -15.1},
		{"below the floor clamps to -23.0", []tape.Measure{ms(-30, -3), ms(-28, -3)}, -23.0},
		{"above the ceiling clamps to -14.0", []tape.Measure{ms(-8, -3), ms(-10, -3)}, -14.0},
		{"exactly the floor", []tape.Measure{ms(-23.0, -3)}, -23.0},
		{"exactly the ceiling", []tape.Measure{ms(-14.0, -3)}, -14.0},
		{"silent items are excluded", []tape.Measure{ms(-70, -3), ms(-60, -3), ms(-18, -3)}, -18.0},
		{"just above silent counts", []tape.Measure{ms(-59.99, -3), ms(-59.99, -3)}, -23.0},
		{"unmeasured items are excluded", []tape.Measure{unmeasured, ms(-19, -3), unmeasured}, -19.0},
		{"nothing measured", []tape.Measure{unmeasured, ms(-80, -3)}, -14.0},
		{"empty", nil, -14.0},
	} {
		if got := tape.AutoTarget(c.in); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParams_Constants(t *testing.T) {
	t.Parallel()
	if tape.MaxItems != 60 || tape.MaxDurationS != 7200 || tape.Rate != 48000 || tape.Bits != 24 ||
		tape.TargetMinLUFS != -23.0 || tape.TargetMaxLUFS != -14.0 || tape.SilentLUFS != -60.0 ||
		tape.MaxCutCDB != -1200 || tape.MaxBoostCDB != 600 || tape.PeakCeilingDBTP != -1.0 ||
		tape.RetentionDays != 14 || tape.MaxBytes != 2<<30 || tape.Chunk != 1<<20 ||
		tape.MaxLivePerRecord != 3 || tape.MaxReadyBytes != 8<<30 {
		t.Error("a C118 constant changed")
	}
	if tape.ItemTimeout.Seconds() != 120 || tape.LeaseTTL.Seconds() != 60 {
		t.Error("a C118 timing changed")
	}
}
