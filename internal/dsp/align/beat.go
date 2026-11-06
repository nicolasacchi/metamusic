package align

import (
	"math"

	"github.com/nicolasacchi/metamusic/internal/analysis"
)

// Grid is a bar grid (C204.1): BPM beats a minute, BeatsPerBar beats to a bar, and the time in seconds of a downbeat.
type Grid struct {
	BPM            float64
	FirstDownbeatS float64
	BeatsPerBar    int
}

// BeatS is the length of a beat in seconds.
func (g Grid) BeatS() float64 { return 60 / g.BPM }

// BarS is the length of a bar in seconds, BeatsPerBar * 60 / BPM.
func (g Grid) BarS() float64 { return float64(g.BeatsPerBar) * 60 / g.BPM }

// BarStart is the time of the downbeat of bar n (0 is the bar of FirstDownbeatS; negative bars lie before it).
func (g Grid) BarStart(n int) float64 { return g.FirstDownbeatS + float64(n)*g.BarS() }

// NearestBar is the bar whose downbeat is nearest t (ties round up).
func (g Grid) NearestBar(t float64) int { return int(math.Floor((t-g.FirstDownbeatS)/g.BarS() + 0.5)) }

// Warning codes LocalGrid reports in LocalInfo.Warnings (C204.3, D-T1-9).
const (
	WarnBeatPhaseWeak       = "beat_phase_weak"
	WarnDownbeatAmbiguous   = "downbeat_ambiguous"
	WarnDownbeatOneDrop     = "downbeat_one_drop_suspected"
	WarnTempoRefineRejected = "tempo_refine_rejected"
)

// DefaultWindowBars is the analysis window of C204.3.
const DefaultWindowBars = 8

const (
	weakConfidence    = 1.5    // Confidence below this is beat_phase_weak
	noiseSigmas       = 2      // Confidence = best score / (mean + 2 sigma of the scores over the search grid)
	ambiguousDownbeat = 0.25   // DownbeatConfidence below this is downbeat_ambiguous
	oneDropConfidence = 0.6    // at least this, at position bpb/2, is downbeat_one_drop_suspected
	tempoSpan         = 0.03   // local tempo search: bpm0 * (1 +- 3 %) ...
	tempoStep         = 0.0005 // ... in 0.05 % steps
	maxRefine         = 0.02   // a refined tempo further than this from the spec's is rejected
	phaseSteps        = 4      // phase grid: a quarter of an envelope frame
	// analysis.OnsetEnvelope's geometry (window 1024, hop 128 at any rate; D-T1-20, pinned by TestOnset_PinnedForT1).
	onsetWindow = 1024
	onsetHop    = 128
	// envLagSamples is where, in samples after the start of its frame, an envelope value places the onset that raised it;
	// measured on single-sample clicks (TestBeatPhase_Clicks fails when it is wrong).
	envLagSamples = 864
	downbeatHz    = 150.0
	downbeatBlock = 0.005 // seconds per energy block of the downbeat heuristic
	downbeatPre   = 0.020 // the energy of a beat is read from beat-20 ms ...
	downbeatPost  = 0.040 // ... to beat+40 ms
)

// LocalInfo is what LocalGrid measured besides the grid.
type LocalInfo struct {
	LocalBPM           float64 // the refined tempo (the spec's when the refinement was rejected)
	BeatPhaseS         float64 // time in seconds of the beat nearest the window centre
	Confidence         float64 // S(phase*, tempo*) / (mean S + 2 sigma of S) over the search grid; < 1.5 is beat_phase_weak
	DownbeatConfidence float64 // (E_best - E_second) / E_best of the downbeat heuristic; < 0.25 is downbeat_ambiguous
	DownbeatPosition   int     // beat position read as the downbeat; 0 is the first beat at or after the audio start on the extended grid
	Warnings           []string
}

// LocalGrid measures the beat phase and tempo of mono x around centreS (C204.3). The onset envelope of a window of
// windowBars bars (clamped inside the input) is scored against combs of beats over bpm0 * (1 +- 3 %) in 0.05 % steps and a
// quarter-frame phase grid; the beats are laid out from the window centre, so a tempo error does not move the grid where
// the transition is. Grid.FirstDownbeatS is the downbeat nearest centreS, picked by the energy heuristic (< 150 Hz onset
// energy by beat position); a caller with an explicit or planned downbeat discards it and uses LocalInfo.BeatPhaseS and
// the tempo only. A refined tempo more than 2 % from bpm0 is rejected and bpm0 used. The result is a deterministic function
// of its arguments. An input shorter than the onset window gives the unrefined grid at centreS with Confidence 0.
func LocalGrid(x []float32, rate int, bpm0 float64, bpb int, centreS float64, windowBars int) (Grid, LocalInfo) {
	bpb = max(bpb, 1)
	windowBars = max(windowBars, 1)
	g := Grid{BPM: bpm0, FirstDownbeatS: centreS, BeatsPerBar: bpb}
	info := LocalInfo{LocalBPM: bpm0, BeatPhaseS: centreS, Warnings: []string{WarnBeatPhaseWeak}}
	if bpm0 <= 0 || rate <= 0 || len(x) < onsetWindow {
		return g, info
	}
	// the window in samples, clamped inside the input
	n := int(float64(windowBars*bpb)*60/bpm0*float64(rate) + 0.5)
	n = min(max(n, onsetWindow), len(x))
	s0 := min(max(int(centreS*float64(rate)+0.5)-n/2, 0), len(x)-n)
	seg := x[s0 : s0+n]
	env := analysis.OnsetEnvelope(seg, rate)
	segT := float64(s0) / float64(rate)
	// the envelope position of a time, in frames
	frameAt := func(t float64) float64 { return ((t-segT)*float64(rate) - envLagSamples) / onsetHop }
	dphi := onsetHop / float64(rate) / phaseSteps

	best := searchComb(env, frameAt, centreS, bpm0, dphi)
	info.Warnings = info.Warnings[:0]
	info.Confidence = best.conf
	if best.conf < weakConfidence {
		info.Warnings = append(info.Warnings, WarnBeatPhaseWeak)
	}
	if math.Abs(best.bpm/bpm0-1) > maxRefine {
		info.Warnings = append(info.Warnings, WarnTempoRefineRejected)
		best.bpm = bpm0
		best.phase, _ = bestPhase(env, frameAt, centreS, bpm0, dphi)
	}
	period := 60 / best.bpm
	beat0 := centreS + best.phase // some beat of the grid
	info.LocalBPM, g.BPM = best.bpm, best.bpm
	info.BeatPhaseS = beat0 + math.Floor((centreS-beat0)/period+0.5)*period
	g.FirstDownbeatS = info.BeatPhaseS

	locateDownbeat(seg, rate, segT, bpb, beat0, period, centreS, &g, &info)
	return g, info
}

type combBest struct{ bpm, phase, conf float64 }

// combScore is the mean envelope value at the beats beat0 + k*period that fall inside the envelope (linear interpolation).
func combScore(env []float64, frameAt func(float64) float64, beat0, period float64) float64 {
	f0 := frameAt(beat0)
	df := frameAt(beat0+period) - f0
	last := float64(len(env) - 1)
	var sum float64
	var n int
	for k := math.Ceil(-f0 / df); ; k++ {
		f := f0 + k*df
		if f > last {
			break
		}
		i := int(f)
		v := env[i]
		if i+1 < len(env) {
			v += (env[i+1] - env[i]) * (f - float64(i))
		}
		sum += v
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// searchComb scores every (tempo, phase) of the search grid and returns the best with its confidence (best / mean score).
func searchComb(env []float64, frameAt func(float64) float64, centreS, bpm0, dphi float64) combBest {
	if len(env) < 2 {
		return combBest{bpm: bpm0}
	}
	const steps = 60 // tempoSpan / tempoStep
	var sum, sumSq float64
	var cnt int
	bestS := math.Inf(-1)
	best := combBest{bpm: bpm0}
	for i := -steps; i <= steps; i++ {
		bpm := bpm0 * (1 + float64(i)*tempoStep)
		period := 60 / bpm
		for j := 0; float64(j)*dphi < period; j++ {
			phi := float64(j) * dphi
			s := combScore(env, frameAt, centreS+phi, period)
			sum += s
			sumSq += float64(s * s)
			cnt++
			if s > bestS {
				bestS, best.bpm, best.phase = s, bpm, phi
			}
		}
	}
	mean := sum / float64(cnt)
	std := math.Sqrt(math.Max(sumSq/float64(cnt)-float64(mean*mean), 0))
	if ceil := mean + noiseSigmas*std; ceil > 0 {
		best.conf = bestS / ceil
	}
	return best
}

// bestPhase is the best phase of a fixed tempo.
func bestPhase(env []float64, frameAt func(float64) float64, centreS, bpm, dphi float64) (phase, score float64) {
	period := 60 / bpm
	score = math.Inf(-1)
	for j := 0; float64(j)*dphi < period; j++ {
		if s := combScore(env, frameAt, centreS+float64(j)*dphi, period); s > score {
			score, phase = s, float64(j)*dphi
		}
	}
	return phase, score
}

// locateDownbeat picks the downbeat among the beats of the local grid by the energy heuristic: the beat position (beats
// numbered from the first beat at or after the audio start, modulo bpb) whose beats carry the most onset energy below 150 Hz.
// It sets g.FirstDownbeatS to the downbeat nearest centreS and reports the confidence and the warnings.
func locateDownbeat(seg []float32, rate int, segT float64, bpb int, beat0, period, centreS float64, g *Grid, info *LocalInfo) {
	kRef := math.Ceil(-beat0 / period) // the beat index of position 0
	posOf := func(k float64) int { return ((int(k-kRef) % bpb) + bpb) % bpb }
	if bpb == 1 {
		info.DownbeatConfidence = 1
		info.DownbeatPosition = 0
		return
	}
	rise := lowBandRise(seg, rate)
	blk := float64(blockLen(rate))
	energy := make([]float64, bpb)
	count := make([]int, bpb)
	kLo := math.Ceil((segT - beat0) / period)
	kHi := math.Floor((segT+float64(len(seg))/float64(rate)-beat0)/period) - 1
	for k := kLo; k <= kHi; k++ {
		t := beat0 + k*period - segT
		a := int(math.Floor((t - downbeatPre) * float64(rate) / blk))
		b := int(math.Floor((t + downbeatPost) * float64(rate) / blk))
		if a < 0 || b >= len(rise) {
			continue
		}
		var e float64
		for _, r := range rise[a : b+1] {
			e += r
		}
		p := posOf(k)
		energy[p] += e
		count[p]++
	}
	var best, second float64 = -1, -1
	pos := 0
	for p := range energy {
		if count[p] > 0 {
			energy[p] /= float64(count[p])
		}
		switch e := energy[p]; {
		case e > best:
			second, best, pos = best, e, p
		case e > second:
			second = e
		}
	}
	if best > 0 {
		info.DownbeatConfidence = (best - max(second, 0)) / best
	}
	info.DownbeatPosition = pos
	if info.DownbeatConfidence < ambiguousDownbeat {
		info.Warnings = append(info.Warnings, WarnDownbeatAmbiguous)
	}
	if bpb%2 == 0 && pos == bpb/2 && info.DownbeatConfidence >= oneDropConfidence {
		info.Warnings = append(info.Warnings, WarnDownbeatOneDrop)
	}
	// the downbeat nearest the centre: the beat of position pos closest in time
	kc := math.Floor((centreS-beat0)/period + 0.5)
	bestK, bestD := kc, math.Inf(1)
	for o := -float64(bpb); o <= float64(bpb); o++ {
		if k := kc + o; posOf(k) == pos {
			if d := math.Abs(beat0 + k*period - centreS); d < bestD {
				bestK, bestD = k, d
			}
		}
	}
	g.FirstDownbeatS = beat0 + bestK*period
}

// lowBandRise is the half-wave-rectified rise of the energy below 150 Hz, per block of 5 ms: seg through two cascaded
// second-order Butterworth low-passes, squared and summed per block, differenced.
func lowBandRise(seg []float32, rate int) []float64 {
	w0 := 2 * math.Pi * downbeatHz / float64(rate)
	alpha := math.Sin(w0) / (2 * math.Sqrt2 / 2)
	cs := math.Cos(w0)
	a0 := 1 + alpha
	b0, b1, b2 := (1-cs)/2/a0, (1-cs)/a0, (1-cs)/2/a0
	a1, a2 := -2*cs/a0, (1-alpha)/a0
	y := make([]float64, len(seg))
	for i, v := range seg {
		y[i] = float64(v)
	}
	for range 2 {
		var x1, x2, y1, y2 float64
		for i, x0 := range y {
			o := float64(b0*x0) + float64(b1*x1) + float64(b2*x2) - float64(a1*y1) - float64(a2*y2)
			x2, x1, y2, y1 = x1, x0, y1, o
			y[i] = o
		}
	}
	blk := blockLen(rate)
	nb := len(y) / blk
	rise := make([]float64, nb)
	var prev float64
	for b := range nb {
		var e float64
		for _, v := range y[b*blk : (b+1)*blk] {
			e += float64(v * v)
		}
		if d := e - prev; d > 0 {
			rise[b] = d
		}
		prev = e
	}
	return rise
}

// blockLen is the energy block of the downbeat heuristic in samples.
func blockLen(rate int) int { return max(int(downbeatBlock*float64(rate)+0.5), 1) }
