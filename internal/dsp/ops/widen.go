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
	out, sink := collect(b.Rate, b.Frames())
	info, err := widenTo(context.Background(), b, p, fixed, m, sink)
	if err != nil {
		return pcm.Buffer{}, info, err
	}
	return *out, info, nil
}

// widenSignals are the three signals the widener mixes, computed block by block from the channels: the mid M = (L+R)/2, the side
// HS = highpass((L-R)/2) and the all-pass copy of the mid HA = highpass(allpass(M)), the high-pass being the LR4 of bass_mono_hz.
// Every filter keeps its state between blocks, so a render is the same in one block or many.
type widenSignals struct {
	p                 WidenParams
	ap                *allpassStream
	hpSide, hpAp      *filt.Stream
	mid, side, apCopy []float64
}

func newWidenSignals(rate int, p WidenParams, block int) *widenSignals {
	w := &widenSignals{p: p, mid: make([]float64, block), side: make([]float64, block), apCopy: make([]float64, block)}
	if p.Decorrelate > 0 {
		w.ap = newAllpassStream(rate)
	}
	if p.BassMonoHz > 0 {
		w.hpSide = filt.NewStream(filt.LR4HighPass(rate, float64(p.BassMonoHz)))
		if p.Decorrelate > 0 {
			w.hpAp = filt.NewStream(filt.LR4HighPass(rate, float64(p.BassMonoHz)))
		}
	}
	return w
}

// next computes the signals of the next frames l, r. m is charged the work of the render's first pass; the second pass, which
// recomputes the same signals, passes nil (its extra work is not metered: the units of a render are those of the in-memory path).
func (w *widenSignals) next(l, r []float64, m *dsp.Meter) (mid, side, ap []float64, err error) {
	k := len(l)
	mid, side, ap = w.mid[:k], w.side[:k], w.apCopy[:k]
	if err := m.Add(int64(k) * 4); err != nil {
		return nil, nil, nil, err
	}
	for i := range k {
		mid[i] = (l[i] + r[i]) / 2
		side[i] = (l[i] - r[i]) / 2
	}
	if w.ap != nil {
		copy(ap, mid)
		if err := w.ap.process(ap, m); err != nil {
			return nil, nil, nil, err
		}
	} else {
		clear(ap)
	}
	if w.hpSide != nil {
		if err := w.hpSide.Process(side, m); err != nil {
			return nil, nil, nil, err
		}
		if w.hpAp != nil {
			if err := w.hpAp.Process(ap, m); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	return mid, side, ap, nil
}

// widenTo is widen over blocks: it hands the render to sink in order (the slices are only valid during the call). The input is read
// three times, none of them holding more than a block of anything: the segment sums of the correlation guard, the sums of the
// filtered signals, and the mix at the strength the guard chose.
func widenTo(ctx context.Context, b pcm.Buffer, p WidenParams, fixed *float64, m *dsp.Meter, sink func(l, r []float64) error) (WidenInfo, error) {
	var info WidenInfo
	if err := p.check(); err != nil {
		return info, err
	}
	n := b.Frames()
	l, r := b.Ch[0], b.Ch[1]
	seg := segLen(b.Rate)
	inSeg, err := segmentSums5(l, r, seg, m)
	if err != nil {
		return info, err
	}
	var inAll sums5
	for _, s := range inSeg {
		inAll.add(s)
	}
	inBlocks := blocks(inSeg)
	keep := gate(inBlocks)
	in := statsOf(inAll, inBlocks, keep)
	info.RhoIn, info.RhoP10In, info.RhoOut, info.RhoP10Out, info.StrengthEffective = in.Rho, in.P10, in.Rho, in.P10, 0
	block := max(hifiBlock, 1)
	bl, br := make([]float64, min(block, n)), make([]float64, min(block, n))
	// pass hands the input itself to sink: the neutral and the guard's pass-through results
	pass := func() (WidenInfo, error) {
		for start := 0; start < n; start += block {
			if err := ctx.Err(); err != nil {
				return info, err
			}
			end := min(start+block, n)
			copy(bl, l[start:end])
			copy(br, r[start:end])
			if err := sink(bl[:end-start], br[:end-start]); err != nil {
				return info, err
			}
		}
		return info, nil
	}
	if p.Width == 1 && p.Decorrelate == 0 && p.BassMonoHz == 0 {
		info.Neutral, info.StrengthEffective = true, 1
		return pass()
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
		info.PassThrough = true
		return pass()
	}
	if fixed == nil && in.Rho < WidenGlobalRhoFloor {
		if in.Rho < 0 {
			info.Warnings = append(info.Warnings, WarnWidenOutOfPhase)
		} else {
			info.Warnings = append(info.Warnings, WarnWidenAlreadyWide)
		}
		info.PassThrough = true
		return pass()
	}

	// first pass: per-segment sums of the five signals, from which the stats of any strength follow in closed form
	sig := newWidenSignals(b.Rate, p, min(block, n))
	var segs []wsums
	var cur wsums
	gi := 0
	for start := 0; start < n; start += block {
		if err := ctx.Err(); err != nil {
			return info, err
		}
		end := min(start+block, n)
		mid, side, ap, err := sig.next(l[start:end], r[start:end], m)
		if err != nil {
			return info, err
		}
		if err := m.Add(int64(end-start) * 14); err != nil {
			return info, err
		}
		for i := 0; i < end-start; {
			// the frames left in the current segment
			stop := min(end-start, i+(seg-gi%seg))
			for ; i < stop; i++ {
				a, s, c := mid[i], side[i], ap[i]
				cur.n++
				cur.m += a
				cur.s += s
				cur.a += c
				cur.mm += float64(a * a)
				cur.ms += float64(a * s)
				cur.ma += float64(a * c)
				cur.ss += float64(s * s)
				cur.sa += float64(s * c)
				cur.aa += float64(c * c)
				gi++
			}
			if gi%seg == 0 {
				segs = append(segs, cur)
				cur = wsums{}
			}
		}
	}
	if cur.n > 0 {
		segs = append(segs, cur)
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
			info.PassThrough = true
			return pass()
		}
	}
	info.StrengthEffective, info.RhoOut, info.RhoP10Out = s, out.Rho, out.P10

	// second pass: the same signals again, mixed at the strength s
	if err := m.Add(int64(n) * 6); err != nil {
		return info, err
	}
	a, c := coef(s)
	sig = newWidenSignals(b.Rate, p, min(block, n))
	for start := 0; start < n; start += block {
		if err := ctx.Err(); err != nil {
			return info, err
		}
		end := min(start+block, n)
		mid, side, ap, err := sig.next(l[start:end], r[start:end], nil)
		if err != nil {
			return info, err
		}
		for i := range mid {
			s2 := float64(a*side[i]) + float64(c*ap[i])
			bl[i] = mid[i] + s2
			br[i] = mid[i] - s2
		}
		if err := sink(bl[:end-start], br[:end-start]); err != nil {
			return info, err
		}
	}
	return info, nil
}

// monoLUFS is the integrated loudness of the mono fold-down (L+R)/2 played on both channels.
func monoLUFS(b pcm.Buffer) (float64, bool) {
	k := safety.NewLUFSMeter(b.Rate, b.Frames())
	monoBlocks(b, func(mono []float64) { k.Add(mono, mono) })
	return k.LUFS()
}

// monoBlocks hands the fold-down (L+R)/2 of b to fn a block at a time.
func monoBlocks(b pcm.Buffer, fn func(mono []float64)) {
	n := b.Frames()
	mono := make([]float64, min(hifiBlock, n))
	for start := 0; start < n; start += hifiBlock {
		end := min(start+hifiBlock, n)
		for i := start; i < end; i++ {
			mono[i-start] = (b.Ch[0][i] + b.Ch[1][i]) / 2
		}
		fn(mono[:end-start])
	}
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
	outMono, outOK := monoLUFS(out)
	raw, err := widenReport(info, rep, safety.TruePeakDBTP(b), parentMono, parentOK, outMono, outOK)
	return RunOut{Buffer: out, Report: raw}, err
}

// streamWiden is runWiden with the render and the finish streamed (see streamMaster): the guard's measurement is a first pass over
// the input, the render writes a scratch spool, and the finish and the mono fold-down of the result are measured on the blocks as
// they are delivered. Same samples, same report.
func streamWiden(ctx context.Context, in RunIn, sc safety.Scratch, sink func(l, r []float64) error) (int, int, json.RawMessage, error) {
	b, err := requireStereo(in)
	if err != nil {
		return 0, 0, nil, err
	}
	p, err := ParseWiden(in.Params)
	if err != nil {
		return 0, 0, nil, err
	}
	m := in.Meter
	if m != nil && m.Ctx == nil {
		m.Ctx = ctx
	}
	if err := m.Add(SafetyUnits(int64(b.Frames()))); err != nil {
		return 0, 0, nil, err
	}
	parentMono, parentOK := monoLUFS(b)
	var info WidenInfo
	outMeter := safety.NewLUFSMeter(b.Rate, b.Frames())
	mono := make([]float64, 0, safety.StreamBlock)
	rep, err := renderAndFinish(ctx, sc, b.Rate, safety.FinishIn{TargetLUFS: in.TargetLUFS, RefLUFS: in.RefLUFS}, sink,
		func(w func(l, r []float64) error) (int, error) {
			var err error
			info, err = widenTo(ctx, b, p, nil, m, w)
			return b.Frames(), err
		}, func(l, r []float64) {
			mono = mono[:0]
			for i := range l {
				mono = append(mono, (l[i]+r[i])/2)
			}
			outMeter.Add(mono, mono)
		})
	if err != nil {
		return 0, 0, nil, err
	}
	tpIn, err := safety.StreamTruePeakDBTP(ctx, safety.BufferSource(b))
	if err != nil {
		return 0, 0, nil, err
	}
	outMono, outOK := outMeter.LUFS()
	raw, err := widenReport(info, rep, tpIn, parentMono, parentOK, outMono, outOK)
	return b.Rate, b.Frames(), raw, err
}

// widenReport is the operator's report: the safety object, the guard's numbers and the warnings of C220.
func widenReport(info WidenInfo, rep safety.FinishReport, tpIn, parentMono float64, parentOK bool, outMono float64, outOK bool) (json.RawMessage, error) {
	warnings := info.Warnings
	delta := 0.0
	if outOK && parentOK {
		delta = outMono - parentMono
		if delta < WidenMonoFoldQuieterLU {
			warnings = append(warnings, WarnWidenMonoFoldQuiet)
		}
	}
	hr := hifiReport{Safety: newSafetyDoc(rep, tpIn), Hifi: map[string]any{"widen": map[string]any{
		"strength_effective": info.StrengthEffective, "rho_in": info.RhoIn, "rho_out": info.RhoOut,
		"rho_p10_out": info.RhoP10Out, "monofold_delta_lu": delta,
	}}}
	for _, w := range warnings {
		hr.Warnings = append(hr.Warnings, hifiWarning{Code: w})
	}
	return hr.marshal()
}
