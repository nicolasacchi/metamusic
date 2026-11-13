package ops

import (
	"encoding/json"
	"math"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// ParamError is a refused operator parameter: the render child maps it to the invalid-job exit, the service to
// INVALID_TRANSFORM {field, rule}. The service validates first, so this is a defence against a hand-edited job file.
type ParamError struct{ Field, Rule string }

func (e *ParamError) Error() string { return "ops: invalid " + e.Field + ": " + e.Rule }

// Rule names of ParamError.
const (
	RuleRange   = "range"
	RulePreset  = "preset_unknown"
	RuleInput   = "input"
	RuleChannel = "channels"
)

// hifiWarning is one coded warning of a hi-fi render report.
type hifiWarning struct {
	Code  string `json:"code"`
	Field string `json:"field,omitempty"`
}

// safetyDoc is the "safety" object of report.json (the shape the ingest bridge reads, child.go childReport).
type safetyDoc struct {
	GainDB  float64 `json:"gain_db"`
	Limiter struct {
		MaxReductionDB  float64 `json:"max_reduction_db"`
		EngagedFraction float64 `json:"engaged_fraction"`
	} `json:"limiter"`
	TargetLUFSRequested float64 `json:"target_lufs_requested"`
	TargetLUFSEffective float64 `json:"target_lufs_effective"`
	LUFSIn              float64 `json:"lufs_in"`
	LUFSOut             float64 `json:"lufs_out"`
	TruePeakDBTP        float64 `json:"true_peak_dbtp"`
	TruePeakIn          float64 `json:"true_peak_in_dbtp"`
}

func newSafetyDoc(r safety.FinishReport, truePeakIn float64) safetyDoc {
	d := safetyDoc{GainDB: r.GainDB, TargetLUFSRequested: r.TargetLUFSRequested, TargetLUFSEffective: r.TargetLUFSEffective,
		LUFSIn: r.InputLUFS, LUFSOut: r.OutputLUFS, TruePeakDBTP: r.OutputTruePeakDBTP, TruePeakIn: truePeakIn}
	d.Limiter.MaxReductionDB, d.Limiter.EngagedFraction = r.MaxReductionDB, r.EngagedFraction
	return d
}

// hifiReport is report.json minus the fields the child fills (pcm_sha256, frames, units): the safety object, the coded
// warnings, and under "hifi" the member of the kind (C223.4).
type hifiReport struct {
	Safety   safetyDoc      `json:"safety"`
	Warnings []hifiWarning  `json:"warnings"`
	Hifi     map[string]any `json:"hifi"`
}

func (r hifiReport) marshal() (json.RawMessage, error) {
	if r.Warnings == nil {
		r.Warnings = []hifiWarning{}
	}
	return json.Marshal(r)
}

// SafetyUnits is the work-unit price of safety.Finish on frames frames (C223.1 "+ safety"): the K-weighted loudness passes of
// the second pass and the limiter evaluations of the bisection (about 0.9 us per frame on the reference box, 1.2 ns per unit).
func SafetyUnits(frames int64) int64 { return 700 * frames }

// requireStereo checks that the job has exactly one stereo input of the job rate.
func requireStereo(in RunIn) (pcm.Buffer, error) {
	if len(in.Inputs) != 1 {
		return pcm.Buffer{}, &ParamError{Field: "inputs", Rule: RuleInput}
	}
	b := in.Inputs[0]
	if b.Rate <= 0 || len(b.Ch[0]) != len(b.Ch[1]) || len(b.Ch[0]) == 0 {
		return pcm.Buffer{}, &ParamError{Field: "inputs", Rule: RuleChannel}
	}
	return b, nil
}

// --- the all-pass of widen (C220.2) --------------------------------------------------------------------------------------

// allpassGain is the feedback gain of every Schroeder section: |H| = 1 exactly for any gain below 1.
const allpassGain = 0.5

// allpassDelays are the section delays in tenths of a millisecond: 3.1, 4.7, 7.3 and 11.1 ms.
var allpassDelays = [4]int{31, 47, 73, 111}

// AllpassDelaySamples is round(tenths * rate / 10000) in integer arithmetic, so the delay table is exact and identical on every
// host.
func AllpassDelaySamples(rate, tenthsMS int) int { return (tenthsMS*rate + 5000) / 10000 }

// allpassStream is the chain of four Schroeder all-pass sections as a stream: each section keeps the last d values of its w signal, so
// the blocks of a signal give exactly the samples of the single pass.
type allpassStream struct {
	d    [len(allpassDelays)]int
	w    [len(allpassDelays)][]float64 // ring of the last d values of w[n] = x[n] + g*w[n-d]
	seen int                           // samples consumed so far
}

func newAllpassStream(rate int) *allpassStream {
	a := &allpassStream{}
	for k, tenths := range allpassDelays {
		a.d[k] = max(AllpassDelaySamples(rate, tenths), 1)
		a.w[k] = make([]float64, a.d[k])
	}
	return a
}

// process runs the next block of the signal through the chain in place.
func (a *allpassStream) process(x []float64, m *dsp.Meter) error {
	for k, d := range a.d {
		if err := m.Add(int64(len(x)) * 4); err != nil {
			return err
		}
		ring := a.w[k]
		for i, v := range x {
			n := a.seen + i
			slot := n % d
			wn := v
			var wd float64
			if n >= d {
				wd = ring[slot] // w[n-d]
				wn += float64(allpassGain * wd)
			}
			ring[slot] = wn
			x[i] = wd - float64(allpassGain*wn)
		}
	}
	a.seen += len(x)
	return nil
}

// allpassChain runs x through the four cascaded Schroeder all-pass sections, H(z) = (-g + z^-d) / (1 - g z^-d) each.
func allpassChain(x []float64, rate int, m *dsp.Meter) ([]float64, error) {
	y := append([]float64(nil), x...)
	if err := newAllpassStream(rate).process(y, m); err != nil {
		return nil, err
	}
	return y, nil
}

// --- inter-channel correlation (C220.3) -----------------------------------------------------------------------------------

const (
	corrBlockSec  = 0.4
	corrHopSec    = 0.1
	corrGateDBFS  = -60.0
	corrSegPerBlk = 4 // corrBlockSec / corrHopSec
)

// sums5 are the running sums of one stretch of a channel pair: n, sum l, sum r, sum ll, sum rr, sum lr.
type sums5 struct{ n, l, r, ll, rr, lr float64 }

func (s *sums5) add(b sums5) {
	s.n += b.n
	s.l += b.l
	s.r += b.r
	s.ll += b.ll
	s.rr += b.rr
	s.lr += b.lr
}

// rho is the Pearson coefficient from the sums (0 when a channel is constant).
func (s sums5) rho() float64 {
	if s.n == 0 {
		return 0
	}
	vl := s.ll - float64(s.l*s.l)/s.n
	vr := s.rr - float64(s.r*s.r)/s.n
	cov := s.lr - float64(s.l*s.r)/s.n
	if vl <= 1e-30*s.n || vr <= 1e-30*s.n {
		return 0
	}
	return math.Max(-1, math.Min(1, cov/math.Sqrt(float64(vl*vr))))
}

// segLen is the hop in samples.
func segLen(rate int) int { return max(int(math.Floor(float64(rate)*corrHopSec+0.5)), 1) }

// corrStats is what the correlation guard measures: the whole-programme Pearson coefficient and the 10th percentile
// (nearest rank) of the 400 ms block values at a 100 ms hop over blocks louder than -60 dBFS RMS.
type corrStats struct{ Rho, P10 float64 }

// segmentSums5 are the sums of l and r per hop-length segment (the last one may be shorter).
func segmentSums5(l, r []float64, seg int, m *dsp.Meter) ([]sums5, error) {
	var out []sums5
	for start := 0; start < len(l); start += seg {
		end := min(start+seg, len(l))
		if err := m.Add(int64(end-start) * 8); err != nil {
			return nil, err
		}
		s := sums5{n: float64(end - start)}
		for i := start; i < end; i++ {
			a, b := l[i], r[i]
			s.l += a
			s.r += b
			s.ll += float64(a * a)
			s.rr += float64(b * b)
			s.lr += float64(a * b)
		}
		out = append(out, s)
	}
	return out, nil
}

// blocks groups segment sums into 400 ms blocks of four segments at a hop of one; a programme shorter than one block is a
// single block of everything.
func blocks(seg []sums5) []sums5 {
	if len(seg) < corrSegPerBlk {
		var s sums5
		for _, q := range seg {
			s.add(q)
		}
		return []sums5{s}
	}
	out := make([]sums5, 0, len(seg)-corrSegPerBlk+1)
	for i := 0; i+corrSegPerBlk <= len(seg); i++ {
		var s sums5
		for _, q := range seg[i : i+corrSegPerBlk] {
			s.add(q)
		}
		out = append(out, s)
	}
	return out
}

// gate marks the blocks of the INPUT above the gate; the same blocks are used for the output so in and out are compared on
// the same material.
func gate(in []sums5) []bool {
	ok := make([]bool, len(in))
	found := false
	for i, s := range in {
		ms := (s.ll + s.rr) / (2 * s.n)
		ok[i] = ms > 0 && float64(10*dsp.Log(ms)/math.Ln10) > corrGateDBFS
		found = found || ok[i]
	}
	if !found { // a whole programme under the gate still has a correlation
		for i := range ok {
			ok[i] = true
		}
	}
	return ok
}

// statsOf reduces block sums to corrStats over the gated blocks and the global sums.
func statsOf(global sums5, bl []sums5, keep []bool) corrStats {
	var vals []float64
	for i, s := range bl {
		if keep[i] {
			vals = append(vals, s.rho())
		}
	}
	st := corrStats{Rho: global.rho()}
	if len(vals) > 0 {
		sort.Float64s(vals)
		idx := int(math.Ceil(0.10*float64(len(vals)))) - 1
		st.P10 = vals[max(idx, 0)]
	}
	return st
}
