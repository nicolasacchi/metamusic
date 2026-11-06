package transform

import (
	"encoding/json"
	"math"
	"math/bits"

	"github.com/nicolasacchi/metamusic/internal/dsp/align"
	"github.com/nicolasacchi/metamusic/internal/dsp/ir"
	"github.com/nicolasacchi/metamusic/internal/dsp/ops"
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
	// SafetyUnitsPerFrame is the work of the loudness stage (safety.Finish), per frame.
	SafetyUnitsPerFrame int64
}

// DefaultCosts is PROVISIONAL(T1-X0 step 5): the starting values of C209.2.
//
// SafetyUnitsPerFrame is 700, measured by lane H1 (ops.SafetyUnits; was 30 in the plan). T1-B2b.
var DefaultCosts = CostConstants{NSPerUnitMilli: 1200, DecodeMSPerS: 3, EncodeMSPerS: 10, IngestMSPerS: 30, ToolMSPerS: 40, SafetyUnitsPerFrame: ops.SafetyUnits(1)}

// The phase-vocoder geometry the unit model assumes. PROVISIONAL(T1-X0 step 5 / lane D2): D2 may choose another window; the
// model then moves with it. A frame advances by pvWindow/pvHopDiv samples.
const (
	pvWindow = 4096
	pvHopDiv = 4
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

// unitsGlacier is D4's GlacierUnits (the operator's own price of C209.1) plus safety.
func unitsGlacier(in UnitsIn) int64 {
	return ops.GlacierUnits(in.OutFrames, in.Rate, paramFloat(paramsMap(in.Resolved), "window_s", 0.5)) + safetyUnits(in.OutFrames)
}

// unitsShepard: F x ch x ((1 + layers) FFT(N) + layers x 40 N).
func unitsShepard(in UnitsIn) int64 {
	layers := int64(paramInt(paramsMap(in.Resolved), "layers", 5))
	return pvFrames(in.OutFrames)*int64(in.Channels)*((1+layers)*fftUnits(pvWindow)+layers*40*pvWindow) + safetyUnits(in.OutFrames)
}

// ensembleTailFrames is the room tail the estimate assumes: D4's upper bound of the generated tail (ir.TailBound: pre-delay,
// 70/60 of the slowest band's decay and the onset rise), cut at tail_s. Measured by D4: the effective cathedral tail is about
// 11.6 s, which the former rt60 x 70/60 (10.5 s) under-promised by about 10 %. T1-B2b.
func ensembleTailFrames(m map[string]json.RawMessage, rate int) int64 {
	room, ok := ir.Preset(paramString(m, "space"))
	if !ok {
		room, _ = ir.Preset("cathedral")
	}
	return int64(min(ir.TailBound(room, rate), int(math.Round(paramFloat(m, "tail_s", 20)*float64(rate)))))
}

// unitsEnsemble is D4's EnsembleUnits (voices, room, convolution) plus safety.
func unitsEnsemble(in UnitsIn) int64 {
	m := paramsMap(in.Resolved)
	return ops.EnsembleUnits(in.InFrames, in.Rate, ops.EnsembleParams{Space: paramString(m, "space"), TailS: paramFloat(m, "tail_s", 20)}) + safetyUnits(in.OutFrames)
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
	return int64(math.Ceil(align.Ramp{T0: t0, T1: t1, Beats: float64(bars * bpb)}.Total() * float64(rate)))
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
