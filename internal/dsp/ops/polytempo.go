package ops

import (
	"context"
	"encoding/json"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/pv"
)

// KindPolytempo is two Takes at different tempos converging on a shared downbeat (C205.2).
const KindPolytempo = "polytempo"

func init() { Register(KindPolytempo, runPolytempo, polytempoUnits) }

// Pan modes of polytempo (C202.3).
const (
	PanSplit  = "split"
	PanCentre = "centre"
)

// polySideDB is the level of each side of the mix (C205.2).
const polySideDB = -3.0

// polySplit is the pan of the split mix: A left-weighted, B right-weighted (C205.2).
const polySplit = 0.35

// PolytempoParams are the resolved parameters of the operator.
type PolytempoParams struct {
	BeatsPerBar int
	ConvergeBar int
	UnisonBars  int
	Pan         string
	N           int // phase-vocoder frame; 0 means 4096
}

func (p PolytempoParams) frame() int {
	if p.N > 0 {
		return p.N
	}
	return pvWindow
}

// ParsePolytempo reads the resolved params and the alignment of job.json and checks what the operator cannot render.
func ParsePolytempo(raw, alignment json.RawMessage) (PolytempoParams, [2]inputAlign, error) {
	in := struct {
		BeatsPerBar *int    `json:"beats_per_bar"`
		ConvergeBar *int    `json:"converge_bar"`
		UnisonBars  *int    `json:"unison_bars"`
		Pan         *string `json:"pan"`
	}{}
	if err := parseParams(raw, &in); err != nil {
		return PolytempoParams{}, [2]inputAlign{}, err
	}
	p := PolytempoParams{BeatsPerBar: 4, ConvergeBar: 4, UnisonBars: 8, Pan: PanSplit}
	if in.BeatsPerBar != nil {
		p.BeatsPerBar = *in.BeatsPerBar
	}
	if in.ConvergeBar != nil {
		p.ConvergeBar = *in.ConvergeBar
	}
	if in.UnisonBars != nil {
		p.UnisonBars = *in.UnisonBars
	}
	if in.Pan != nil {
		p.Pan = *in.Pan
	}
	switch {
	case p.BeatsPerBar < 2 || p.BeatsPerBar > 7:
		return p, [2]inputAlign{}, &ParamError{Field: "beats_per_bar", Rule: RuleRange}
	case p.ConvergeBar < 4:
		return p, [2]inputAlign{}, &ParamError{Field: "converge_bar", Rule: RuleRange}
	case p.UnisonBars < 0 || p.UnisonBars > 64:
		return p, [2]inputAlign{}, &ParamError{Field: "unison_bars", Rule: RuleRange}
	case p.Pan != PanSplit && p.Pan != PanCentre:
		return p, [2]inputAlign{}, &ParamError{Field: "pan", Rule: RuleRange}
	}
	al, err := parseAlignment(alignment, p.BeatsPerBar)
	return p, al, err
}

// logMean is L = (ta - tb) / ln(ta / tb), the mean tempo of a ramp that is log-linear in time, and ta when the tempos are equal.
func logMean(ta, tb float64) float64 {
	if math.Abs(dsp.Log(ta/tb)) < 1e-12 {
		return ta
	}
	return (ta - tb) / dsp.Log(ta/tb)
}

// PolytempoPlan is the timeline of C205.2 for the local tempos and the meeting.
type PolytempoPlan struct {
	EntryS     float64 // o: B's entry in the output
	M          int     // the number of bars of B before the downbeat that meets A
	MeetBeats  float64 // beats of B played before the meeting
	LBPM       float64 // the log-mean tempo of the ramp
	RampS      float64 // Tc - o
	EndS       float64 // Tc + unison_bars bars of A
	BSourceEnd float64 // the seconds of B consumed by the end
}

// PlanPolytempo places B. tc is the meeting in the output, ta and tb the tempos of A and B at the meeting, first the position
// of B's first downbeat in B's beats (first >= 0: audio before it counts), unison the bars after the meeting. B enters as early as it can: m is
// the largest integer >= 0 with an entry offset o >= 0, 0 <= o < 60 bpb / L. ok is false when no such m exists.
func PlanPolytempo(tc, ta, tb, first float64, bpb, unison int) (PolytempoPlan, bool) {
	l := logMean(ta, tb)
	room := tc*l/60 - first // beats available between B's first downbeat and the meeting
	if room < 0 {
		return PolytempoPlan{}, false
	}
	m := int(math.Floor(room / float64(bpb)))
	meet := first + float64(m*bpb)
	o := tc - 60*meet/l
	end := tc + float64(unison*bpb)*60/ta
	return PolytempoPlan{
		EntryS: o, M: m, MeetBeats: meet, LBPM: l, RampS: tc - o, EndS: end,
		BSourceEnd: (meet + float64(unison*bpb)) * 60 / tb,
	}, true
}

// PolytempoInfo is what Polytempo measured besides the audio.
type PolytempoInfo struct {
	Phase     [2]PhaseInfo
	Warnings  []Warning
	Plan      PolytempoPlan
	TcS       float64
	TA, TB    float64 // local tempos
	OutFrames int
}

// Polytempo renders C205.2: A unstretched from output 0, B entering as early as it can and ramping log-linearly in time from
// its tempo to A's so that a downbeat of each coincides at the meeting, then both in unison for unison_bars bars of A.
func Polytempo(ctx context.Context, a, b pcm.Buffer, p PolytempoParams, al [2]inputAlign, m *dsp.Meter) (pcm.Buffer, PolytempoInfo, error) {
	var info PolytempoInfo
	rate := a.Rate
	if rate <= 0 || b.Rate != rate || a.Frames() == 0 || b.Frames() == 0 {
		return pcm.Buffer{}, info, &ParamError{Field: "inputs", Rule: RuleChannel}
	}
	n := p.frame()
	hs := n / 4
	bpb := p.BeatsPerBar
	// A: the meeting, from the spec's grid, measured locally
	barA := float64(bpb) * 60 / al[0].BPM
	ancA, err := anchorAt(monoF32(a), rate, al[0], al[0].d0()+float64(p.ConvergeBar)*barA, "downbeat_a_s", m)
	if err != nil {
		return pcm.Buffer{}, info, err
	}
	tc, ta := ancA.T, ancA.BPM
	// B: a first estimate of the meeting downbeat from the spec's numbers, then measured around it
	tbSpec := al[1].BPM
	first0 := al[1].d0() * tbSpec / 60 // B's first downbeat in B's beats: audio before it (an intro) is part of B
	plan0, ok := PlanPolytempo(tc, ta, tbSpec, first0, bpb, p.UnisonBars)
	if !ok {
		return pcm.Buffer{}, info, &ParamError{Field: "converge_bar", Rule: RuleConvergeTooEarly}
	}
	sc0 := plan0.MeetBeats * 60 / tbSpec
	ancB, err := anchorAt(monoF32(b), rate, al[1], sc0, "downbeat_b_s", m)
	if err != nil {
		return pcm.Buffer{}, info, err
	}
	tb := ancB.BPM
	if r := tb / ta; !(r >= 1.0/RatioMax && r <= RatioMax) {
		return pcm.Buffer{}, info, &ParamError{Field: "bpm_b", Rule: RuleTempoRatio}
	}
	// the measured downbeat gives B's grid: the first downbeat is the measured one less the bars the first plan stepped over
	first := ancB.T*tb/60 - float64(plan0.M*bpb)
	if first < 0 {
		first += math.Ceil(-first/float64(bpb)) * float64(bpb)
	}
	plan, ok := PlanPolytempo(tc, ta, tb, first, bpb, p.UnisonBars)
	if !ok {
		return pcm.Buffer{}, info, &ParamError{Field: "converge_bar", Rule: RuleConvergeTooEarly}
	}
	info.Phase = [2]PhaseInfo{ancA.Info, ancB.Info}
	info.Warnings = append(append(info.Warnings, ancA.Warnings...), ancB.Warnings...)
	info.Plan, info.TcS, info.TA, info.TB = plan, tc, ta, tb

	outLen := roundHalfUp(plan.EndS * float64(rate))
	oS := roundHalfUp(plan.EntryS * float64(rate))
	srcEnd := roundHalfUp(plan.BSourceEnd * float64(rate))
	if outLen > a.Frames() {
		return pcm.Buffer{}, info, &ParamError{Field: "converge_bar", Rule: RuleATooShort}
	}
	if srcEnd > b.Frames() {
		return pcm.Buffer{}, info, &ParamError{Field: "converge_bar", Rule: RuleBTooShort}
	}
	info.OutFrames = outLen

	lnr := dsp.Log(tb / ta)
	ramp := plan.RampS
	alpha := func(frame int) float64 {
		t := math.Max(float64(frame*hs-n), 0) / float64(rate)
		frac := 1.0
		if ramp > 1e-9 {
			frac = math.Min(t/ramp, 1)
		}
		return dsp.Exp(lnr * frac)
	}
	pvp := pv.Params{N: n, Hs: hs, Lock: true, Rate: rate, Meter: m}
	pan := [2]float64{}
	if p.Pan == PanSplit {
		pan = [2]float64{-polySplit, polySplit}
	}
	side := dbToLin(polySideDB)
	var gains [2][2]float64 // gains[input][channel]
	for i := range gains {
		l, r := stackGains(pan[i])
		gains[i] = [2]float64{float64(side * l), float64(side * r)}
	}

	out := pcm.Buffer{Rate: rate}
	for c := 0; c < 2; c++ {
		if err := ctx.Err(); err != nil {
			return pcm.Buffer{}, info, err
		}
		stB, err := pv.StretchErr(seg(b.Ch[c], -n, srcEnd+n), alpha, pvp)
		if err != nil {
			return pcm.Buffer{}, info, pvErr(err, "bpm_b")
		}
		o := make([]float64, outLen)
		err = meterBlocks(m, outLen, 2*unitsCopySample+2, func(lo, hi int) {
			for i := lo; i < hi; i++ {
				v := float64(gains[0][c] * a.Ch[c][i])
				if j := i - oS; j >= 0 && n+j < len(stB) {
					v += float64(gains[1][c] * stB[n+j])
				}
				o[i] = v
			}
		})
		if err != nil {
			return pcm.Buffer{}, info, err
		}
		out.Ch[c] = o
	}
	return out, info, nil
}

func runPolytempo(ctx context.Context, in RunIn) (RunOut, error) {
	p, al, err := ParsePolytempo(in.Params, in.Alignment)
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
	out, info, err := Polytempo(ctx, a, b, p, al, in.Meter)
	if err != nil {
		return RunOut{}, err
	}
	rep, err := pairReport(KindPolytempo, info.Phase, info.Warnings, map[string]any{
		"tc_s": info.TcS, "entry_s": info.Plan.EntryS, "m": info.Plan.M, "l_bpm": info.Plan.LBPM,
		"local_bpm_a": info.TA, "local_bpm_b": info.TB, "pan": p.Pan, "out_frames": info.OutFrames,
	})
	return RunOut{Buffer: out, Report: rep}, err
}

// PolytempoUnits is the work-unit model of polytempo (safety excluded): two local grids, B stretched over the output (plus
// its context) on both channels, and the mix.
func PolytempoUnits(inFrames, outFrames int64, rate int, p PolytempoParams, bpmA float64) int64 {
	n := p.frame()
	u := pvStretchUnits(outFrames+2*int64(n), n)
	win := int64(gridWindow(max(bpmA, 40), p.BeatsPerBar, rate, int(min(max(inFrames/2, 1024), 1<<31-1))))
	u += 2 * LocalGridUnits(win)
	return u + 2*(2*unitsCopySample+2)*outFrames
}

func polytempoUnits(in, out int64, rate int, raw json.RawMessage) int64 {
	var q struct {
		BeatsPerBar int     `json:"beats_per_bar"`
		BPMA        float64 `json:"bpm_a"`
	}
	_ = parseParams(raw, &q)
	p := PolytempoParams{BeatsPerBar: q.BeatsPerBar}
	if p.BeatsPerBar == 0 {
		p.BeatsPerBar = 4
	}
	if q.BPMA == 0 {
		q.BPMA = 120
	}
	return PolytempoUnits(in, out, rate, p, q.BPMA)
}
