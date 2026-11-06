package ops

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/align"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/pv"
)

// KindMorph is the beat- and key-aligned transition of two Takes (C205.1).
const KindMorph = "morph"

func init() { Register(KindMorph, runMorph, morphUnits) }

// Rule names the pair operators add to ParamError.
const (
	RuleATooShort        = "a_too_short"
	RuleBTooShort        = "b_too_short"
	RuleConvergeTooEarly = "converge_too_early"
	RuleDownbeat         = "downbeat_unresolved"
	RuleTempoRatio       = "tempo_ratio"
)

const (
	// gridWindowBars is the width of the local beat-phase window (C204.3).
	gridWindowBars = 8
	// gridUnitsPerSample prices align.LocalGrid per sample of its window (about 0.25 us per sample on the reference box, 1.2 ns
	// per unit).
	gridUnitsPerSample = 220
	// joinS is the equal-power crossfade at each join of the morph (C205.1.4).
	joinS = 0.010
	// RatioMax bounds the tempo ratio of a morph: the phase vocoder follows stretch ratios in [0.25, 8], and the ramp stretches A
	// by T0/T1 and B by T1/T0.
	RatioMax = 4.0
	// mixUnitsPerSample prices the per-sample ramp gains of the transition.
	mixUnitsPerSample = 40
)

// unitsCopySample is the price of copying one verbatim sample into the output.
const unitsCopySample = 1

// inputAlign is one entry of the frozen alignment object of the spec (C202.2): the numbers the pair operators need.
type inputAlign struct {
	BPM          float64  `json:"bpm"`
	DownbeatS    *float64 `json:"downbeat_s"`
	DownbeatAuto bool     `json:"downbeat_auto"`
	PlanBarS     *float64 `json:"plan_bar_s"`
	BeatsPerBar  int      `json:"beats_per_bar"`
}

// source is the downbeat_source of the report: explicit, plan or auto.
func (a inputAlign) source() string {
	switch {
	case a.DownbeatAuto:
		return "auto"
	case a.DownbeatS != nil:
		return "explicit"
	default:
		return "plan"
	}
}

// d0 is the first downbeat the spec states: the explicit one, else 0 (a plan grid starts at the first bar of the audio; the
// auto heuristic has no value before the render measures it).
func (a inputAlign) d0() float64 {
	if a.DownbeatS != nil {
		return *a.DownbeatS
	}
	return 0
}

// parseAlignment reads the alignment object and checks the two pair inputs.
func parseAlignment(raw json.RawMessage, bpb int) ([2]inputAlign, error) {
	var doc struct {
		Inputs []inputAlign `json:"inputs"`
	}
	var out [2]inputAlign
	if err := parseParams(raw, &doc); err != nil {
		return out, &ParamError{Field: "alignment", Rule: "type"}
	}
	if len(doc.Inputs) != 2 {
		return out, &ParamError{Field: "alignment", Rule: RuleInput}
	}
	names := [2]string{"bpm_a", "bpm_b"}
	downs := [2]string{"downbeat_a_s", "downbeat_b_s"}
	for i, a := range doc.Inputs {
		if !(a.BPM >= 40 && a.BPM <= 240) {
			return out, &ParamError{Field: names[i], Rule: RuleRange}
		}
		if a.DownbeatS == nil && !a.DownbeatAuto && a.PlanBarS == nil {
			return out, &ParamError{Field: downs[i], Rule: RuleDownbeat}
		}
		if a.DownbeatS != nil && !(*a.DownbeatS >= 0) {
			return out, &ParamError{Field: downs[i], Rule: RuleRange}
		}
		a.BeatsPerBar = bpb
		out[i] = a
	}
	return out, nil
}

// at is a_at / b_at of the spec after resolution: a bar of the input's grid, a number of bars before its end (auto downbeat), or
// a section start in seconds.
type at struct {
	Bar         *int     `json:"bar"`
	FromEndBars *int     `json:"from_end_bars"`
	StartS      *float64 `json:"start_s"`
}

// nominal is the position in seconds the transition is expected at, from the spec's tempo and downbeat: the render then
// measures the grid around it.
func (a at) nominal(al inputAlign, barS, dur float64) (float64, bool) {
	switch {
	case a.Bar != nil:
		return al.d0() + float64(*a.Bar)*barS, true
	case a.FromEndBars != nil:
		return dur - float64(*a.FromEndBars)*barS, true
	case a.StartS != nil:
		return *a.StartS, true
	}
	return 0, false
}

// MorphParams are the resolved parameters of the operator.
type MorphParams struct {
	Bars        int
	BeatsPerBar int
	AAt, BAt    at
	SemitonesB  float64
	N           int // phase-vocoder frame; 0 means 4096 (tests use a smaller one at 16 kHz)
}

func (p MorphParams) frame() int {
	if p.N > 0 {
		return p.N
	}
	return pvWindow
}

// ParseMorph reads the resolved params object of job.json and the alignment object and checks what the operator cannot render.
func ParseMorph(raw, alignment json.RawMessage) (MorphParams, [2]inputAlign, error) {
	in := struct {
		Bars        *int     `json:"bars"`
		BeatsPerBar *int     `json:"beats_per_bar"`
		AAt         *at      `json:"a_at"`
		BAt         *at      `json:"b_at"`
		SemitonesB  *float64 `json:"semitones_b"`
	}{}
	if err := parseParams(raw, &in); err != nil {
		return MorphParams{}, [2]inputAlign{}, err
	}
	p := MorphParams{Bars: 8, BeatsPerBar: 4}
	if in.Bars != nil {
		p.Bars = *in.Bars
	}
	if in.BeatsPerBar != nil {
		p.BeatsPerBar = *in.BeatsPerBar
	}
	if in.SemitonesB != nil {
		p.SemitonesB = *in.SemitonesB
	}
	if in.AAt != nil {
		p.AAt = *in.AAt
	}
	if in.BAt != nil {
		p.BAt = *in.BAt
	} else {
		zero := 0
		p.BAt = at{Bar: &zero}
	}
	switch {
	case p.Bars < 1 || p.Bars > 32:
		return p, [2]inputAlign{}, &ParamError{Field: "bars", Rule: RuleRange}
	case p.BeatsPerBar < 2 || p.BeatsPerBar > 7:
		return p, [2]inputAlign{}, &ParamError{Field: "beats_per_bar", Rule: RuleRange}
	case !(math.Abs(p.SemitonesB) <= 12):
		return p, [2]inputAlign{}, &ParamError{Field: "semitones_b", Rule: RuleRange}
	}
	if _, ok := p.AAt.nominal(inputAlign{}, 1, 1); !ok {
		return p, [2]inputAlign{}, &ParamError{Field: "a_at", Rule: RuleInput}
	}
	al, err := parseAlignment(alignment, p.BeatsPerBar)
	return p, al, err
}

// --- the measured grid ---------------------------------------------------------------------------------------------------

// PhaseInfo is the beat_phase entry of one input in report.json (C210.5).
type PhaseInfo struct {
	LocalBPM           float64  `json:"local_bpm"`
	FirstDownbeatS     float64  `json:"first_downbeat_s"`
	Confidence         float64  `json:"confidence"`
	DownbeatSource     string   `json:"downbeat_source"`
	DownbeatConfidence *float64 `json:"downbeat_confidence"`
	DownbeatAmbiguous  bool     `json:"downbeat_ambiguous"`
}

// anchor is the result of measuring the grid of one input around a nominal position.
type anchor struct {
	T        float64 // the bar start, in seconds, the operator uses
	BPM      float64 // the tempo it uses (the local one unless the measurement was weak)
	Info     PhaseInfo
	Warnings []Warning
}

// monoF32 is the float32 mono mix the onset front end is fed (C204.3).
func monoF32(b pcm.Buffer) []float32 {
	out := make([]float32, b.Frames())
	for i := range out {
		out[i] = float32(0.5 * (b.Ch[0][i] + b.Ch[1][i]))
	}
	return out
}

// LocalGridUnits is the work-unit price of align.LocalGrid over a window of the given number of samples.
func LocalGridUnits(windowSamples int64) int64 { return gridUnitsPerSample * windowSamples }

// gridWindow is the number of samples of the 8-bar window around a position, as align.LocalGrid clamps it.
func gridWindow(bpm float64, bpb, rate, n int) int {
	w := roundHalfUp(float64(gridWindowBars*bpb) * 60 / bpm * float64(rate))
	return min(max(w, 1024), n)
}

// anchorAt measures the grid of x around the nominal bar start centre. An explicit or plan downbeat is a statement about which
// beat starts a bar, so the measurement only moves it to the nearest measured beat and refines the tempo; with the auto opt-in
// the heuristic's downbeat is the one used. A weak measurement (confidence under 1.5) keeps the spec's numbers.
func anchorAt(mono []float32, rate int, al inputAlign, centre float64, field string, m *dsp.Meter) (anchor, error) {
	if err := m.Add(LocalGridUnits(int64(gridWindow(al.BPM, al.BeatsPerBar, rate, len(mono))))); err != nil {
		return anchor{}, err
	}
	centre = math.Max(centre, 0)
	g, info := align.LocalGrid(mono, rate, al.BPM, al.BeatsPerBar, centre, gridWindowBars)
	auto := al.DownbeatAuto
	a := anchor{T: centre, BPM: al.BPM}
	a.Info = PhaseInfo{LocalBPM: info.LocalBPM, Confidence: info.Confidence, DownbeatSource: al.source()}
	weak := false
	for _, w := range info.Warnings {
		switch w {
		case align.WarnBeatPhaseWeak:
			weak = true
			a.Warnings = append(a.Warnings, Warning{Code: w, Field: field})
		case align.WarnTempoRefineRejected:
			a.Warnings = append(a.Warnings, Warning{Code: w, Field: field})
		default: // the downbeat heuristic's warnings concern only an input that asked for it
			if auto {
				a.Warnings = append(a.Warnings, Warning{Code: w, Field: field})
			}
		}
	}
	switch {
	case auto:
		a.T, a.BPM = g.FirstDownbeatS, g.BPM
		dc := info.DownbeatConfidence
		a.Info.DownbeatConfidence = &dc
		a.Info.DownbeatAmbiguous = dc < 0.25
	case weak:
		// keep the spec's tempo and the stated downbeat
	default:
		a.T, a.BPM = info.BeatPhaseS, g.BPM
	}
	a.Info.FirstDownbeatS = a.T
	return a, nil
}

// --- the morph --------------------------------------------------------------------------------------------------------------

// MorphInfo is what Morph measured besides the audio.
type MorphInfo struct {
	Phase      [2]PhaseInfo
	Warnings   []Warning
	TA, TB     float64 // the transition start in A and the entry in B, seconds
	T0, T1     float64 // the local tempos the ramp runs between
	TotalS     float64 // ramp.Total()
	TransFrame int
	OutFrames  int
}

// seg copies x[from, to) with zeros outside x.
func seg(x []float64, from, to int) []float64 {
	out := make([]float64, max(to-from, 0))
	lo, hi := max(from, 0), min(to, len(x))
	if lo < hi {
		copy(out[lo-from:], x[lo:hi])
	}
	return out
}

// pvErr maps a phase-vocoder parameter refusal (a ratio the vocoder cannot follow) to the invalid-job error.
func pvErr(err error, field string) error {
	if errors.Is(err, pv.ErrParam) {
		return &ParamError{Field: field, Rule: RuleTempoRatio}
	}
	return err
}

// Morph renders the transition of C205.1: A verbatim up to tA, a tempo ramp of exactly ramp.Total() seconds in which A is
// stretched from T0 to T1 and B from T0 to its own tempo (shifted into A's key and gliding to its own pitch) under an
// equal-power crossfade, then B verbatim to its end. The output has exactly tA + ramp.Total() + the tail of B samples.
func Morph(ctx context.Context, a, b pcm.Buffer, p MorphParams, al [2]inputAlign, m *dsp.Meter) (pcm.Buffer, MorphInfo, error) {
	var info MorphInfo
	rate := a.Rate
	if rate <= 0 || b.Rate != rate || a.Frames() == 0 || b.Frames() == 0 {
		return pcm.Buffer{}, info, &ParamError{Field: "inputs", Rule: RuleChannel}
	}
	n := p.frame()
	hs := n / 4
	barA := float64(p.BeatsPerBar) * 60 / al[0].BPM
	barB := float64(p.BeatsPerBar) * 60 / al[1].BPM
	durA, durB := float64(a.Frames())/float64(rate), float64(b.Frames())/float64(rate)
	nomA, _ := p.AAt.nominal(al[0], barA, durA)
	nomB, ok := p.BAt.nominal(al[1], barB, durB)
	if !ok {
		nomB = al[1].d0()
	}
	ancA, err := anchorAt(monoF32(a), rate, al[0], nomA, "downbeat_a_s", m)
	if err != nil {
		return pcm.Buffer{}, info, err
	}
	ancB, err := anchorAt(monoF32(b), rate, al[1], nomB, "downbeat_b_s", m)
	if err != nil {
		return pcm.Buffer{}, info, err
	}
	info.Phase = [2]PhaseInfo{ancA.Info, ancB.Info}
	info.Warnings = append(append(info.Warnings, ancA.Warnings...), ancB.Warnings...)
	t0, t1 := ancA.BPM, ancB.BPM
	beats := float64(p.Bars * p.BeatsPerBar)
	ramp := align.Ramp{T0: t0, T1: t1, Beats: beats}
	total := ramp.Total()
	info.TA, info.TB, info.T0, info.T1, info.TotalS = ancA.T, ancB.T, t0, t1, total

	if r := t1 / t0; !(r >= 1.0/RatioMax && r <= RatioMax) {
		return pcm.Buffer{}, info, &ParamError{Field: "bpm_b", Rule: RuleTempoRatio}
	}
	tAs, tBs := max(roundHalfUp(ancA.T*float64(rate)), 0), max(roundHalfUp(ancB.T*float64(rate)), 0)
	transLen := roundHalfUp(total * float64(rate))
	srcA := roundHalfUp(60 * beats / t0 * float64(rate))
	srcB := roundHalfUp(60 * beats / t1 * float64(rate))
	tailStart := tBs + srcB
	if tAs+srcA > a.Frames() {
		return pcm.Buffer{}, info, &ParamError{Field: "a_at", Rule: RuleATooShort}
	}
	if tailStart > b.Frames() {
		return pcm.Buffer{}, info, &ParamError{Field: "b_at", Rule: RuleBTooShort}
	}
	outLen := tAs + transLen + (b.Frames() - tailStart)
	info.TransFrame, info.OutFrames = transLen, outLen

	// time of output frame m of a stretched segment with a lead-in of n samples, clamped to the ramp
	tau := func(frame int) float64 {
		return math.Min(math.Max(float64(frame*hs-n), 0)/float64(rate), total)
	}
	lnr := dsp.Log(t1 / t0)
	alphaA := func(frame int) float64 { return ramp.Alpha(tau(frame)) }
	alphaB := func(frame int) float64 {
		if frame*hs < n { // the lead-in is read at natural speed so the transition starts exactly n samples in
			return 1
		}
		return dsp.Exp(lnr * (1 - ramp.BeatAt(tau(frame))/beats))
	}
	shifted := math.Abs(p.SemitonesB) > 1e-9
	rho := func(frame int) float64 {
		u := math.Min(ramp.BeatAt(tau(frame))/beats, 1)
		return dsp.Exp(math.Ln2 * p.SemitonesB * (1 - u) / 12)
	}
	pvp := pv.Params{N: n, Hs: hs, Lock: true, Rate: rate, Meter: m}

	out := pcm.Buffer{Rate: rate}
	for c := 0; c < 2; c++ {
		if err := ctx.Err(); err != nil {
			return pcm.Buffer{}, info, err
		}
		stA, err := pv.StretchErr(seg(a.Ch[c], tAs-n, tAs+srcA+n), alphaA, pvp)
		if err != nil {
			return pcm.Buffer{}, info, pvErr(err, "bpm_b")
		}
		stB, err := pv.StretchErr(seg(b.Ch[c], tBs-n, tailStart+n), alphaB, pvp)
		if err != nil {
			return pcm.Buffer{}, info, pvErr(err, "bpm_b")
		}
		if shifted {
			if stB, err = pv.ShiftErr(stB, rho, pvp); err != nil {
				return pcm.Buffer{}, info, pvErr(err, "semitones_b")
			}
		}
		tr := make([]float64, transLen)
		trA, trB := seg(stA, n, n+transLen), seg(stB, n, n+transLen)
		err = meterBlocks(m, transLen, mixUnitsPerSample, func(lo, hi int) {
			for i := lo; i < hi; i++ {
				u := math.Min(ramp.BeatAt(float64(i)/float64(rate))/beats, 1)
				th := math.Pi / 2 * u
				tr[i] = float64(math.Cos(th)*trA[i]) + float64(math.Sin(th)*trB[i])
			}
		})
		if err != nil {
			return pcm.Buffer{}, info, err
		}
		// the joins: 10 ms equal-power crossfades against the verbatim signals
		xf := min(roundHalfUp(joinS*float64(rate)), transLen/2)
		for i := 0; i < xf; i++ {
			th := math.Pi / 2 * float64(i+1) / float64(xf+1)
			tr[i] = float64(math.Cos(th)*a.Ch[c][tAs+i]) + float64(math.Sin(th)*tr[i])
			k := transLen - xf + i
			tr[k] = float64(math.Cos(th)*tr[k]) + float64(math.Sin(th)*b.Ch[c][tailStart-xf+i])
		}
		o := make([]float64, 0, outLen)
		o = append(o, a.Ch[c][:tAs]...)
		o = append(o, tr...)
		o = append(o, b.Ch[c][tailStart:]...)
		if err := m.Add(int64(unitsCopySample * outLen)); err != nil {
			return pcm.Buffer{}, info, err
		}
		out.Ch[c] = o
	}
	return out, info, nil
}

// pairReport is the report of morph and polytempo: the beat-phase entries, the warnings and the operator's own object.
func pairReport(kind string, phase [2]PhaseInfo, warns []Warning, own map[string]any) (json.RawMessage, error) {
	bp := make([]json.RawMessage, 2)
	for i := range phase {
		raw, err := json.Marshal(phase[i])
		if err != nil {
			return nil, err
		}
		bp[i] = raw
	}
	if warns == nil {
		warns = []Warning{}
	}
	return json.Marshal(map[string]any{
		"beat_phase":  bp,
		"warnings":    warns,
		"blind_spots": PairBlindSpots,
		kind:          own,
	})
}

// PairBlindSpots are the limits of the local grid that morph and polytempo write to the report (C204.3).
var PairBlindSpots = []string{"tempo_drift", "rubato", "free_time_intro", "half_double_time", "swing", "tempo_change_in_window", "downbeat_heuristic"}

func runMorph(ctx context.Context, in RunIn) (RunOut, error) {
	p, al, err := ParseMorph(in.Params, in.Alignment)
	if err != nil {
		return RunOut{}, err
	}
	if len(in.Inputs) != 2 {
		return RunOut{}, &ParamError{Field: "inputs", Rule: RuleInput}
	}
	a, b := in.Inputs[0], in.Inputs[1]
	if a.Rate == 0 {
		a.Rate = in.Rate
	}
	if b.Rate == 0 {
		b.Rate = in.Rate
	}
	out, info, err := Morph(ctx, a, b, p, al, in.Meter)
	if err != nil {
		return RunOut{}, err
	}
	rep, err := pairReport(KindMorph, info.Phase, info.Warnings, map[string]any{
		"t_a_s": info.TA, "t_b_s": info.TB, "local_bpm_a": info.T0, "local_bpm_b": info.T1, "ramp_s": info.TotalS,
		"semitones_b": p.SemitonesB, "out_frames": info.OutFrames,
	})
	return RunOut{Buffer: out, Report: rep}, err
}

// morphTransFrames is the transition length in frames for the tempos and bars of the spec.
func morphTransFrames(bpmA, bpmB float64, bars, bpb, rate int) int64 {
	if !(bpmA > 0 && bpmB > 0) {
		return 0
	}
	return int64(roundHalfUp(align.Ramp{T0: bpmA, T1: bpmB, Beats: float64(bars * bpb)}.Total() * float64(rate)))
}

// pvStretchUnits is the meter's price of stretching a segment whose output has the given length, on both channels.
func pvStretchUnits(outLen int64, n int) int64 {
	hs := int64(n / 4)
	return 2 * (outLen/hs + 2) * dsp.Units.PVFrame(n)
}

// pvShiftUnits is the meter's price of shifting a signal of the given length by one voice without formant, on both channels.
func pvShiftUnits(length int64, n int) int64 {
	hs := int64(n / 4)
	return 2 * (length/hs + 2) * (2*dsp.Units.FFT(n) + int64(30*n))
}

// MorphUnits is the work-unit model of morph (safety excluded): two local grids, the three processed signals of the
// transition (A stretch, B stretch, B shift when the shift is not zero) and the linear passes.
func MorphUnits(inFrames, outFrames int64, rate int, p MorphParams, bpmA, bpmB float64) int64 {
	n := int64(p.frame())
	trans := morphTransFrames(bpmA, bpmB, p.Bars, p.BeatsPerBar, rate)
	seg := trans + 2*n
	u := pvStretchUnits(seg, p.frame()) * 2
	if math.Abs(p.SemitonesB) > 1e-9 {
		u += pvShiftUnits(seg, p.frame())
	}
	win := int64(gridWindow(max(bpmA, 40), p.BeatsPerBar, rate, int(min(max(inFrames/2, 1024), 1<<31-1))))
	u += 2 * LocalGridUnits(win)
	u += 2*mixUnitsPerSample*trans + 2*unitsCopySample*outFrames
	return u
}

func morphUnits(in, out int64, rate int, raw json.RawMessage) int64 {
	var q struct {
		Bars        int      `json:"bars"`
		BeatsPerBar int      `json:"beats_per_bar"`
		BPMA        float64  `json:"bpm_a"`
		BPMB        float64  `json:"bpm_b"`
		SemitonesB  *float64 `json:"semitones_b"`
	}
	_ = parseParams(raw, &q)
	p := MorphParams{Bars: q.Bars, BeatsPerBar: q.BeatsPerBar}
	if p.Bars == 0 {
		p.Bars = 8
	}
	if p.BeatsPerBar == 0 {
		p.BeatsPerBar = 4
	}
	if q.SemitonesB != nil {
		p.SemitonesB = *q.SemitonesB
	}
	bpmA, bpmB := q.BPMA, q.BPMB
	if bpmA == 0 {
		bpmA = 120
	}
	if bpmB == 0 {
		bpmB = bpmA
	}
	return MorphUnits(in, out, rate, p, bpmA, bpmB)
}
