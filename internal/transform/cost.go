package transform

import (
	"encoding/json"
	"math"
	"math/bits"
)

// The cost model of C209. Every number here is integer arithmetic (CPU budgets are integer milliseconds), so the estimate is the
// same on every host.
//
// PROVISIONAL(T1-X0 step 5): the constants below are the plan's starting values. X0 recalibrates NSPerUnit and the per-second
// shares on the build host and replaces them; until then an estimate is an order of magnitude, not a promise.

// CostConstants are the calibration constants of C209.2. Tests inject their own so no test depends on hardware.
type CostConstants struct {
	// NSPerUnitMilli is NSPerUnit in thousandths of a nanosecond (1.2 ns per work unit = 1200).
	NSPerUnitMilli int64
	// DecodeMSPerS, EncodeMSPerS, IngestMSPerS, ToolMSPerS are milliseconds of CPU per second of audio.
	DecodeMSPerS, EncodeMSPerS, IngestMSPerS, ToolMSPerS int64
	// SafetyUnitsPerFrame is the work of the loudness stage: LUFS plus limiter, about 30 units per frame.
	SafetyUnitsPerFrame int64
}

// DefaultCosts is PROVISIONAL(T1-X0 step 5): the starting values of C209.2.
var DefaultCosts = CostConstants{NSPerUnitMilli: 1200, DecodeMSPerS: 3, EncodeMSPerS: 10, IngestMSPerS: 30, ToolMSPerS: 40, SafetyUnitsPerFrame: 30}

// The phase-vocoder geometry the unit model assumes. PROVISIONAL(T1-X0 step 5 / lane D2): D2 may choose another window; the
// model then moves with it. A frame advances by pvWindow/pvHopDiv samples.
const (
	pvWindow = 4096
	pvHopDiv = 4
	// ensembleVoices is the fixed choir preset of C202.3.
	ensembleVoices = 5
	// maxUnits keeps 13 x units x NSPerUnitMilli inside int64.
	maxUnits = int64(1) << 50
)

// CPUPlan is the answer of the CPU model for one transform.
type CPUPlan struct {
	Units      int64
	ExpectedMS int64
	MaxMS      int64 // the budget (C209.3)
	MemMB      int64
	OutBytes   int64
}

// ceilDiv is ceil(a/b) for non-negative a and positive b.
func ceilDiv(a, b int64) int64 {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// framesMS is the length of n frames at rate in whole milliseconds, rounded up.
func framesMS(n int64, rate int) int64 {
	if rate <= 0 {
		return 0
	}
	return ceilDiv(n*1000, int64(rate))
}

// ExpectedMS is expected_cpu_ms of C209.2: ceil(1.3 x units x NSPerUnit / 1e6 + decode + encode + ingest + tool), in integer
// arithmetic. inMS and outMS are the summed input length and the output length in milliseconds; tool is for score_render only.
func (c CostConstants) ExpectedMS(units, inMS, outMS int64, tool bool) int64 {
	units = min(max(units, 0), maxUnits)
	compute := ceilDiv(13*units*c.NSPerUnitMilli, 10*1000*1_000_000)
	ms := compute + ceilDiv(c.DecodeMSPerS*inMS, 1000) + ceilDiv(c.EncodeMSPerS*outMS, 1000) + ceilDiv(c.IngestMSPerS*outMS, 1000)
	if tool {
		ms += ceilDiv(c.ToolMSPerS*outMS, 1000)
	}
	return ms
}

// BudgetMS is max_cpu_ms of C209.3: min(MaxCPUS x 1000, 2 x expected + 5000).
func BudgetMS(expectedMS int64, maxCPUS int) int64 {
	return min(int64(maxCPUS)*1000, 2*expectedMS+5000)
}

// MemoryMB is mem_mb_est of C209.4: ceil((sum in_frames + out_frames x (1 + k)) x 16 B / 2^20 + 64), planar float64 stereo.
func MemoryMB(inFrames, outFrames int64, k float64) int64 {
	// k is one of {0,1,3}; the product stays exact
	frames := inFrames + outFrames*(1+int64(math.Round(k)))
	return ceilDiv(frames*16, 1<<20) + 64
}

// OutBytesEst is out_bytes_est of C209.4: 0.65 x 6 x out_frames.
func OutBytesEst(outFrames int64) int64 { return ceilDiv(outFrames*39, 10) }

func log2Ceil(n int64) int64 {
	if n <= 1 {
		return 0
	}
	return int64(bits.Len64(uint64(n - 1)))
}

func nextPow2(n int64) int64 {
	if n <= 1 {
		return 1
	}
	return int64(1) << bits.Len64(uint64(n-1))
}

// fftUnits is FFT(n) = 5 n log2 n.
func fftUnits(n int64) int64 { return 5 * n * log2Ceil(n) }

// pvFrames is the number of phase-vocoder frames over n output samples.
func pvFrames(n int64) int64 { return ceilDiv(n, pvWindow/pvHopDiv) + 1 }

func safetyUnits(outFrames int64) int64 { return DefaultCosts.SafetyUnitsPerFrame * outFrames }

// pvUnits is one processed signal: frames x channels x (3 FFT(N) + 40 N) (the morph/polytempo per-signal cost).
func pvSignalUnits(frames int64, ch int) int64 {
	return pvFrames(frames) * int64(ch) * (3*fftUnits(pvWindow) + 40*pvWindow)
}

// unitsMorph: the three processed signals of the transition (A stretch, B stretch, B shift) cost PV units; the verbatim head
// and tail are copies. UnitsIn.OutFrames is the whole output; the transition length comes from the resolved parameters.
func unitsMorph(in UnitsIn) int64 {
	m := paramsMap(in.Resolved)
	trans := rampFrames(m, in.Rate)
	return 3*pvSignalUnits(trans, in.Channels) + safetyUnits(in.OutFrames)
}

// unitsPolytempo: B is stretched over the whole overlap; A is a copy; plus a mix of 10 units per frame and channel.
func unitsPolytempo(in UnitsIn) int64 {
	return pvSignalUnits(in.OutFrames, in.Channels) + 10*in.OutFrames*int64(in.Channels) + safetyUnits(in.OutFrames)
}

// unitsGlacier: F x ch x (2 FFT(N) + 20 N) with N the window and F = outLen/(N/2) + 1.
func unitsGlacier(in UnitsIn) int64 {
	m := paramsMap(in.Resolved)
	n := nextPow2(int64(math.Round(paramFloat(m, "window_s", 0.5) * float64(in.Rate))))
	f := ceilDiv(in.OutFrames, n/2) + 1
	return f*int64(in.Channels)*(2*fftUnits(n)+20*n) + safetyUnits(in.OutFrames)
}

// unitsShepard: F x ch x ((1 + layers) FFT(N) + layers x 40 N).
func unitsShepard(in UnitsIn) int64 {
	layers := int64(paramInt(paramsMap(in.Resolved), "layers", 5))
	return pvFrames(in.OutFrames)*int64(in.Channels)*((1+layers)*fftUnits(pvWindow)+layers*40*pvWindow) + safetyUnits(in.OutFrames)
}

// ensembleTailFrames is the room tail the estimate assumes: the tail cut at -70 dB, bounded by tail_s.
func ensembleTailFrames(m map[string]json.RawMessage, rate int) int64 {
	tail := paramFloat(m, "tail_s", 20)
	rt60 := map[string]float64{"cathedral": 9, "cistern": 18, "hangar": 5, "canyon": 12}[paramString(m, "space")]
	if rt60 > 0 {
		tail = min(tail, rt60*70/60) // -70 dB is reached after 70/60 of the rt60
	}
	return int64(math.Ceil(tail * float64(rate)))
}

// unitsEnsemble: F x ch x ((1+V) FFT(N) + V 40 N + 25 N), plus the IR (7 FFT(2^k) + 20 IRlen) and the convolution per channel
// (modelled as two FFTs of the padded length per channel).
func unitsEnsemble(in UnitsIn) int64 {
	m := paramsMap(in.Resolved)
	v := int64(ensembleVoices)
	f := pvFrames(in.InFrames)
	voices := f * int64(in.Channels) * ((1+v)*fftUnits(pvWindow) + v*40*pvWindow + 25*pvWindow)
	irLen := ensembleTailFrames(m, in.Rate)
	ir := 7*fftUnits(nextPow2(irLen)) + 20*irLen
	conv := int64(in.Channels) * 2 * fftUnits(nextPow2(in.InFrames+irLen))
	return voices + ir + conv + safetyUnits(in.OutFrames)
}

// rampFrames is the length in frames of the morph transition: ramp.Total() of C204.6 for the resolved bars, tempos and
// beats per bar.
func rampFrames(m map[string]json.RawMessage, rate int) int64 {
	bars := paramInt(m, "bars", 8)
	bpb := paramInt(m, "beats_per_bar", 4)
	t0 := paramFloat(m, "bpm_a", 0)
	t1 := paramFloat(m, "bpm_b", 0)
	if t0 <= 0 || t1 <= 0 {
		return 0
	}
	return int64(math.Ceil(rampTotal(t0, t1, float64(bars*bpb)) * float64(rate)))
}

// rampTotal is Total() of the tempo ramp T(beta) = T0 r^(beta/B), r = T1/T0, over B beats: 60 B (1 - 1/r) / (T0 ln r), or
// 60 B / T0 when r = 1 (C204.6). It is a copy of the closed form because the alignment package is not part of this lane.
func rampTotal(t0, t1, beats float64) float64 {
	r := t1 / t0
	if math.Abs(math.Log(r)) < 1e-12 {
		return 60 * beats / t0
	}
	return 60 * beats * (1 - 1/r) / (t0 * math.Log(r))
}

// planCPU turns the unit model of a kind into the CPU plan of one transform.
func planCPU(c CostConstants, spec KindSpec, u UnitsIn, inMS int64, maxCPUS int) CPUPlan {
	units := spec.Units(u)
	outMS := framesMS(u.OutFrames, u.Rate)
	exp := c.ExpectedMS(units, inMS, outMS, spec.Kind == KindScoreRender)
	return CPUPlan{
		Units: units, ExpectedMS: exp, MaxMS: BudgetMS(exp, maxCPUS),
		MemMB: MemoryMB(u.InFrames, u.OutFrames, spec.MemFactor), OutBytes: OutBytesEst(u.OutFrames),
	}
}
