package tape

import (
	"math"
	"sort"
)

// Measure is one item's integrated loudness and true peak, from Ears. OK false means no measurement exists.
type Measure struct {
	LUFS, TruePeakDBTP float64
	OK                 bool
}

// GainStatus says how an item's gain came about.
type GainStatus string

// The statuses of a planned gain.
const (
	GainMatched      GainStatus = "matched"
	GainCutClamped   GainStatus = "cut_clamped"
	GainBoostClamped GainStatus = "boost_clamped"
	GainPeakLimited  GainStatus = "peak_limited"
	GainSilent       GainStatus = "silent"
	GainUnmeasured   GainStatus = "unmeasured"
)

// GainPlan is the planned static gain of one item: GainCDB in centi-dB and the loudness it should have afterwards
// (nil for silent and unmeasured items).
type GainPlan struct {
	GainCDB       int
	Status        GainStatus
	PredictedLUFS *float64
}

// round2 converts a reading to integer centi-units, rounding half away from zero. ok is false for a value the plan treats
// as unmeasured: not finite, or beyond any believable reading.
func round2(x float64) (c int, ok bool) {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) > maxSane/centi {
		return 0, false
	}
	return int(math.Round(x * centi)), true
}

// reading is one measured item in centi-units.
type reading struct{ l, tp int }

func read(m Measure) (reading, bool) {
	if !m.OK {
		return reading{}, false
	}
	l, ok1 := round2(m.LUFS)
	tp, ok2 := round2(m.TruePeakDBTP)
	return reading{l, tp}, ok1 && ok2
}

func silent(r reading) bool { return r.l <= int(SilentLUFS*centi) }

// roundHalfAway divides n by d (d > 0) rounding half away from zero.
func roundHalfAway(n, d int) int {
	if n < 0 {
		return -((-n + d/2) / d)
	}
	return (n + d/2) / d
}

// autoTarget is AutoTarget in centi-units: a multiple of 10.
func autoTarget(rs []reading) int {
	var ls []int
	for _, r := range rs {
		if !silent(r) {
			ls = append(ls, r.l)
		}
	}
	if len(ls) == 0 {
		return int(TargetMaxLUFS * centi)
	}
	sort.Ints(ls)
	m2 := 2 * ls[len(ls)/2] // twice the median, so the mean of the two middle values stays an integer
	if len(ls)%2 == 0 {
		m2 = ls[len(ls)/2-1] + ls[len(ls)/2]
	}
	m2 = min(max(m2, 2*int(TargetMinLUFS*centi)), 2*int(TargetMaxLUFS*centi))
	return roundHalfAway(m2, 2*tenthStep) * tenthStep
}

// AutoTarget is the target loudness when the caller names none (D-L2-18): the median integrated LUFS of the measured,
// non-silent items, clamped to [TargetMinLUFS, TargetMaxLUFS] and rounded to 0.1 LU (half away from zero); no measurement
// at all gives TargetMaxLUFS. Integer arithmetic on centi-units from the first step.
func AutoTarget(ms []Measure) float64 {
	var rs []reading
	for _, m := range ms {
		if r, ok := read(m); ok {
			rs = append(rs, r)
		}
	}
	return float64(autoTarget(rs)) / centi
}

// PlanGains plans one static gain per item (D-L2-18) and returns the target it used. A caller's target is first rounded to
// 0.1 LU (half away from zero); otherwise the target is AutoTarget. Every reading is converted once to integer centi-units
// (round(x*100), half away from zero) and everything after is integer arithmetic:
//
//   - an item without a (finite) measurement is unmeasured: gain 0;
//   - an item at or below SilentLUFS is silent: gain 0, and it does not count toward the median;
//   - otherwise gain = target - loudness, clamped to [MaxCutCDB, MaxBoostCDB] (cut_clamped / boost_clamped);
//   - then the peak guard: gain <= PeakCeilingDBTP - truePeak (exact in integers; the float form floor((-1.0 - (-1.9))*100)
//     gives 89 where 90 is right). A guarded gain is peak_limited; when the guard pushes it to or past the cut floor the
//     cut clamp wins and the item is cut_clamped.
//
// PredictedLUFS is (loudness + gain)/100 for every item that was neither silent nor unmeasured.
func PlanGains(ms []Measure, target *float64) (float64, []GainPlan) {
	rs := make([]reading, len(ms))
	ok := make([]bool, len(ms))
	var measured []reading
	for i, m := range ms {
		rs[i], ok[i] = read(m)
		if ok[i] {
			measured = append(measured, rs[i])
		}
	}
	var tc int
	if target != nil {
		// a non-finite or absurd caller target falls back to the automatic one (the export validates the range itself)
		if t10, fine := roundTenths(*target); fine {
			tc = t10 * tenthStep
		} else {
			tc = autoTarget(measured)
		}
	} else {
		tc = autoTarget(measured)
	}
	plans := make([]GainPlan, len(ms))
	for i, r := range rs {
		switch {
		case !ok[i]:
			plans[i] = GainPlan{Status: GainUnmeasured}
			continue
		case silent(r):
			plans[i] = GainPlan{Status: GainSilent}
			continue
		}
		gain, status := tc-r.l, GainMatched
		switch {
		case gain < int(MaxCutCDB):
			gain, status = int(MaxCutCDB), GainCutClamped
		case gain > int(MaxBoostCDB):
			gain, status = int(MaxBoostCDB), GainBoostClamped
		}
		if limit := int(PeakCeilingDBTP*centi) - r.tp; gain > limit {
			gain, status = limit, GainPeakLimited
			if gain <= int(MaxCutCDB) {
				gain, status = int(MaxCutCDB), GainCutClamped
			}
		}
		pred := float64(r.l+gain) / centi
		plans[i] = GainPlan{GainCDB: gain, Status: status, PredictedLUFS: &pred}
	}
	return float64(tc) / centi, plans
}

// roundTenths rounds a target in LU to whole tenths, half away from zero.
func roundTenths(x float64) (int, bool) {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) > maxSane/centi {
		return 0, false
	}
	return int(math.Round(x * tenthStep)), true
}
