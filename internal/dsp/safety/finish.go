package safety

import (
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

const (
	silentPeakDBFS = -70.0
	dcThreshold    = 1e-4
	fadeSec        = 0.010
	bisections     = 8
	ceilingSlackDB = 0.05
	// DefaultTargetLUFS stands in for a zero FinishIn.TargetLUFS (D-T1-14).
	DefaultTargetLUFS = -16.0
)

// Finish runs the D-T1-14 pipeline on b in place: finite check, silence check, DC removal, 10 ms fades, then the second
// pass (gain to the target, the limiter on a copy, the shortfall measured; while it exceeds MaxLimitingLU the target is lowered
// by bisection, at most 8 evaluations), the final limiter on the real buffer and the true-peak assertion against the
// ceiling. Only ErrNonFinite and ErrSilent fail a render (the ceiling assertion is a bug alarm: one re-limit, then ErrCeiling).
func Finish(b *pcm.Buffer, in FinishIn) (FinishReport, error) {
	rep := FinishReport{TargetLUFSRequested: in.TargetLUFS, RefLUFS: in.RefLUFS, CeilingDBTP: in.Ceiling()}
	target := in.TargetLUFS
	if target == 0 {
		target = DefaultTargetLUFS
		rep.TargetLUFSRequested = target
	}
	if len(b.Ch[0]) != len(b.Ch[1]) {
		return rep, ErrSilent
	}
	var peak float64
	for c := range 2 {
		for _, v := range b.Ch[c] {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return rep, ErrNonFinite
			}
			peak = math.Max(peak, math.Abs(v))
		}
	}
	if peak == 0 || float64(2*db10(peak)) < silentPeakDBFS {
		return rep, ErrSilent
	}
	removeDC(b, &rep)
	if !in.OwnFades {
		fade(b)
	}
	l0, ok := LUFS(*b)
	if !ok {
		return rep, ErrSilent
	}
	rep.InputLUFS = l0
	ceil := rep.CeilingDBTP
	det := framePeaks(*b)
	gainFor := func(t float64) float64 { return dsp.Pow(10, (t-l0)/20) }
	trial := pcm.Buffer{Rate: b.Rate, Ch: [2][]float64{make([]float64, b.Frames()), make([]float64, b.Frames())}}
	shortfall := func(t float64) float64 {
		limitInto(&trial, *b, det, gainFor(t), ceil)
		l, ok := LUFS(trial)
		if !ok {
			return math.Inf(1)
		}
		return t - l
	}
	eff := target
	sf0 := shortfall(target)
	if sf0 > MaxLimitingLU {
		rep.Limited = true
		// lo is feasible: gain that puts the true peak exactly on the ceiling engages no limiter
		hi := target
		lo := math.Min(l0+ceil-float64(2*db10(maxOf(det))), hi-0.01)
		guess := target - (sf0 - MaxLimitingLU)
		if guess > lo && guess < hi {
			if shortfall(guess) <= MaxLimitingLU {
				lo = guess
			} else {
				hi = guess
			}
		}
		for range bisections - 1 {
			mid := (lo + hi) / 2
			if shortfall(mid) <= MaxLimitingLU {
				lo = mid
			} else {
				hi = mid
			}
		}
		eff = lo
	}
	rep.TargetLUFSEffective = eff
	rep.GainDB = eff - l0
	info := limitInto(b, *b, det, gainFor(eff), ceil)
	rep.MaxReductionDB, rep.EngagedFraction = info.MaxReductionDB, info.EngagedFraction
	tp := TruePeakDBTP(*b)
	if tp > ceil+ceilingSlackDB {
		limitInto(b, *b, framePeaks(*b), 1, ceil-(tp-ceil)-0.02)
		tp = TruePeakDBTP(*b)
		if tp > ceil+ceilingSlackDB {
			return rep, ErrCeiling
		}
	}
	rep.OutputTruePeakDBTP = tp
	if l, ok := LUFS(*b); ok {
		rep.OutputLUFS = l
		rep.ShortfallLU = eff - l
	} else {
		return rep, ErrSilent
	}
	return rep, nil
}

// removeDC subtracts each channel's mean when it exceeds 1e-4 (D-T1-14 step 2).
func removeDC(b *pcm.Buffer, rep *FinishReport) {
	for c := range 2 {
		var s float64
		for _, v := range b.Ch[c] {
			s += v
		}
		mean := s / float64(len(b.Ch[c]))
		if math.Abs(mean) > dcThreshold {
			rep.DCRemoved = true
			for i := range b.Ch[c] {
				b.Ch[c][i] -= mean
			}
		}
	}
}

// fade applies 10 ms raised-cosine fades at both ends (shorter on a buffer under 20 ms).
func fade(b *pcm.Buffer) {
	n := b.Frames()
	f := min(int(math.Floor(float64(b.Rate)*fadeSec+0.5)), n/2)
	for i := range f {
		g := 0.5 - float64(0.5*math.Cos(math.Pi*float64(i)/float64(f)))
		for c := range 2 {
			b.Ch[c][i] = float64(b.Ch[c][i] * g)
			b.Ch[c][n-1-i] = float64(b.Ch[c][n-1-i] * g)
		}
	}
}
