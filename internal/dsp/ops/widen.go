package ops

import (
	"context"
	"encoding/json"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/filt"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// KindWiden is the registry name of the stereo widener (C220).
const KindWiden = "widen"

// The widen parameter table (C220.1); the service validates against the same constants.
const (
	WidenWidthMin, WidenWidthMax, WidenWidthDefault = 1.0, 2.0, 1.3
	WidenDecorMin, WidenDecorMax, WidenDecorDefault = 0.0, 0.5, 0.10
	WidenBassMin, WidenBassMax, WidenBassDefault    = 60, 250, 120
)

// The fixed correlation policy of C220.3.
const (
	WidenGlobalRhoFloor = 0.30
	WidenP10RhoFloor    = 0.05
	widenBisections     = 8
	// WidenMonoFoldQuieterLU is the monofold_delta_lu below which the render warns.
	WidenMonoFoldQuieterLU = -1.5
)

// Warning codes the widen render can produce (the constants of C223.3).
const (
	WarnWidenAlreadyWide   = "widen_already_wide"
	WarnWidenOutOfPhase    = "widen_out_of_phase_input"
	WarnWidenMonoSource    = "widen_mono_source"
	WarnWidenStrengthLow   = "widen_strength_reduced"
	WarnWidenMonoFoldQuiet = "widen_monofold_quieter"
)

const monoSourceTolerance = 1e-12

func init() { Register(KindWiden, runWiden, widenUnits) }

// WidenParams are the resolved parameters of the operator.
type WidenParams struct {
	Width, Decorrelate float64
	BassMonoHz         int // 0 = off
}

// ParseWiden reads the resolved params object of job.json, filling an absent or null member with its default, and checks the
// ranges of C220.1.
func ParseWiden(raw json.RawMessage) (WidenParams, error) {
	var in struct {
		Width       *float64 `json:"width"`
		Decorrelate *float64 `json:"decorrelate"`
		BassMonoHz  *int     `json:"bass_mono_hz"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &in); err != nil {
			return WidenParams{}, &ParamError{Field: "params", Rule: "type"}
		}
	}
	p := WidenParams{Width: WidenWidthDefault, Decorrelate: WidenDecorDefault, BassMonoHz: WidenBassDefault}
	if in.Width != nil {
		p.Width = *in.Width
	}
	if in.Decorrelate != nil {
		p.Decorrelate = *in.Decorrelate
	}
	if in.BassMonoHz != nil {
		p.BassMonoHz = *in.BassMonoHz
	}
	return p, p.check()
}

func (p WidenParams) check() error {
	switch {
	case !(p.Width >= WidenWidthMin && p.Width <= WidenWidthMax):
		return &ParamError{Field: "width", Rule: RuleRange}
	case !(p.Decorrelate >= WidenDecorMin && p.Decorrelate <= WidenDecorMax):
		return &ParamError{Field: "decorrelate", Rule: RuleRange}
	case p.BassMonoHz != 0 && (p.BassMonoHz < WidenBassMin || p.BassMonoHz > WidenBassMax):
		return &ParamError{Field: "bass_mono_hz", Rule: RuleRange}
	}
	return nil
}

// WidenInfo is what the correlation guard recorded.
type WidenInfo struct {
	StrengthEffective    float64
	RhoIn, RhoP10In      float64
	RhoOut, RhoP10Out    float64
	Warnings             []string
	PassThrough, Neutral bool
}

// wsums are the per-segment sums of the five signals of widen: M, the (high-passed) side HS, the (high-passed) all-pass copy of
// the mid HA, and their products.
type wsums struct{ n, m, s, a, mm, ms, ma, ss, sa, aa float64 }

// WidenUnits is the work-unit model of C223.1 for frames frames: M/S, four all-pass sections, two LR4, the correlation passes
// and the final mix, plus the safety stage.
func WidenUnits(frames int64) int64 { return 100*frames + SafetyUnits(frames) }

func widenUnits(_, out int64, _ int, _ json.RawMessage) int64 { return WidenUnits(out) }

// Widen widens b without changing its mono fold-down and returns the buffer before the safety stage. Length is unchanged.
func Widen(b pcm.Buffer, p WidenParams, m *dsp.Meter) (pcm.Buffer, WidenInfo, error) {
	return widen(b, p, nil, m)
}

// widen is Widen with an optional fixed strength: when fixed is set the correlation guard is bypassed and the render runs at
// that strength (the tests use it to examine the filters on signals the guard would refuse, such as a hard-panned sine).
func widen(b pcm.Buffer, p WidenParams, fixed *float64, m *dsp.Meter) (pcm.Buffer, WidenInfo, error) {
	var info WidenInfo
	if err := p.check(); err != nil {
		return pcm.Buffer{}, info, err
	}
	n := b.Frames()
	l, r := b.Ch[0], b.Ch[1]
	seg := segLen(b.Rate)
	inSeg, err := segmentSums5(l, r, seg, m)
	if err != nil {
		return pcm.Buffer{}, info, err
	}
	var inAll sums5
	for _, s := range inSeg {
		inAll.add(s)
	}
	inBlocks := blocks(inSeg)
	keep := gate(inBlocks)
	in := statsOf(inAll, inBlocks, keep)
	info.RhoIn, info.RhoP10In, info.RhoOut, info.RhoP10Out, info.StrengthEffective = in.Rho, in.P10, in.Rho, in.P10, 0
	pass := func() (pcm.Buffer, WidenInfo, error) {
		info.PassThrough = true
		return b.Clone(), info, nil
	}
	if p.Width == 1 && p.Decorrelate == 0 && p.BassMonoHz == 0 {
		info.Neutral, info.StrengthEffective = true, 1
		return b.Clone(), info, nil
	}
	mono := true
	for i := range n {
		if math.Abs(l[i]-r[i]) > monoSourceTolerance {
			mono = false
			break
		}
	}
	if fixed == nil && mono && p.Decorrelate == 0 {
		info.Warnings = append(info.Warnings, WarnWidenMonoSource)
		return pass()
	}
	if fixed == nil && in.Rho < WidenGlobalRhoFloor {
		if in.Rho < 0 {
			info.Warnings = append(info.Warnings, WarnWidenOutOfPhase)
		} else {
			info.Warnings = append(info.Warnings, WarnWidenAlreadyWide)
		}
		return pass()
	}

	if err := m.Add(int64(n) * 4); err != nil {
		return pcm.Buffer{}, info, err
	}
	mid, side := make([]float64, n), make([]float64, n)
	for i := range n {
		mid[i] = (l[i] + r[i]) / 2
		side[i] = (l[i] - r[i]) / 2
	}
	var ap []float64
	if p.Decorrelate > 0 {
		if ap, err = allpassChain(mid, b.Rate, m); err != nil {
			return pcm.Buffer{}, info, err
		}
	}
	if p.BassMonoHz > 0 {
		hp := filt.LR4HighPass(b.Rate, float64(p.BassMonoHz))
		if side, err = hp.Process(side, m); err != nil {
			return pcm.Buffer{}, info, err
		}
		if ap != nil {
			if ap, err = hp.Process(ap, m); err != nil {
				return pcm.Buffer{}, info, err
			}
		}
	}
	if ap == nil {
		ap = make([]float64, n)
	}

	// per-segment sums of the five signals: the stats of any strength follow from them in closed form
	if err := m.Add(int64(n) * 14); err != nil {
		return pcm.Buffer{}, info, err
	}
	var segs []wsums
	for start := 0; start < n; start += seg {
		end := min(start+seg, n)
		w := wsums{n: float64(end - start)}
		for i := start; i < end; i++ {
			a, s, c := mid[i], side[i], ap[i]
			w.m += a
			w.s += s
			w.a += c
			w.mm += float64(a * a)
			w.ms += float64(a * s)
			w.ma += float64(a * c)
			w.ss += float64(s * s)
			w.sa += float64(s * c)
			w.aa += float64(c * c)
		}
		segs = append(segs, w)
	}
	coef := func(s float64) (a, c float64) {
		return 1 + float64(s*(p.Width-1)), float64(s * p.Decorrelate)
	}
	// the output pair for strength s is L' = M + S2, R' = M - S2 with S2 = a*HS + c*HA
	outSums := func(w wsums, a, c float64) sums5 {
		sm := float64(a*w.ms) + float64(c*w.ma)
		s2 := float64(a*a)*w.ss + float64(2*a*c)*w.sa + float64(c*c)*w.aa
		s1 := float64(a*w.s) + float64(c*w.a)
		return sums5{n: w.n, l: w.m + s1, r: w.m - s1, ll: w.mm + 2*sm + s2, rr: w.mm - 2*sm + s2, lr: w.mm - s2}
	}
	eval := func(s float64) corrStats {
		a, c := coef(s)
		sg := make([]sums5, len(segs))
		var all sums5
		for i, w := range segs {
			sg[i] = outSums(w, a, c)
			all.add(sg[i])
		}
		return statsOf(all, blocks(sg), keep)
	}
	feasible := func(st corrStats) bool {
		return st.Rho >= math.Min(WidenGlobalRhoFloor, in.Rho) && st.P10 >= math.Min(WidenP10RhoFloor, in.P10)
	}
	s := 1.0
	if fixed != nil {
		s = *fixed
	}
	out := eval(s)
	if fixed == nil && !feasible(out) {
		lo, hi := 0.0, 1.0
		for range widenBisections {
			mid := (lo + hi) / 2
			if feasible(eval(mid)) {
				lo = mid
			} else {
				hi = mid
			}
		}
		s = lo
		out = eval(s)
		info.Warnings = append(info.Warnings, WarnWidenStrengthLow)
		if s == 0 {
			info.StrengthEffective = 0
			return pass()
		}
	}
	info.StrengthEffective, info.RhoOut, info.RhoP10Out = s, out.Rho, out.P10

	if err := m.Add(int64(n) * 6); err != nil {
		return pcm.Buffer{}, info, err
	}
	a, c := coef(s)
	res := pcm.Buffer{Rate: b.Rate, Ch: [2][]float64{make([]float64, n), make([]float64, n)}}
	for i := range n {
		s2 := float64(a*side[i]) + float64(c*ap[i])
		res.Ch[0][i] = mid[i] + s2
		res.Ch[1][i] = mid[i] - s2
	}
	return res, info, nil
}

// monoLUFS is the integrated loudness of the mono fold-down (L+R)/2 played on both channels.
func monoLUFS(b pcm.Buffer) (float64, bool) {
	n := b.Frames()
	mono := make([]float64, n)
	for i := range n {
		mono[i] = (b.Ch[0][i] + b.Ch[1][i]) / 2
	}
	return safety.LUFS(pcm.Buffer{Rate: b.Rate, Ch: [2][]float64{mono, mono}})
}

func runWiden(ctx context.Context, in RunIn) (RunOut, error) {
	b, err := requireStereo(in)
	if err != nil {
		return RunOut{}, err
	}
	p, err := ParseWiden(in.Params)
	if err != nil {
		return RunOut{}, err
	}
	m := in.Meter
	if m != nil && m.Ctx == nil {
		m.Ctx = ctx
	}
	if err := m.Add(SafetyUnits(int64(b.Frames()))); err != nil {
		return RunOut{}, err
	}
	parentMono, parentOK := monoLUFS(b)
	out, info, err := Widen(b, p, m)
	if err != nil {
		return RunOut{}, err
	}
	rep, err := safety.Finish(&out, safety.FinishIn{TargetLUFS: in.TargetLUFS, RefLUFS: in.RefLUFS})
	if err != nil {
		return RunOut{}, err
	}
	warnings := info.Warnings
	delta := 0.0
	if mo, ok := monoLUFS(out); ok && parentOK {
		delta = mo - parentMono
		if delta < WidenMonoFoldQuieterLU {
			warnings = append(warnings, WarnWidenMonoFoldQuiet)
		}
	}
	hr := hifiReport{Safety: newSafetyDoc(rep, safety.TruePeakDBTP(b)), Hifi: map[string]any{"widen": map[string]any{
		"strength_effective": info.StrengthEffective, "rho_in": info.RhoIn, "rho_out": info.RhoOut,
		"rho_p10_out": info.RhoP10Out, "monofold_delta_lu": delta,
	}}}
	for _, w := range warnings {
		hr.Warnings = append(hr.Warnings, hifiWarning{Code: w})
	}
	raw, err := hr.marshal()
	return RunOut{Buffer: out, Report: raw}, err
}
