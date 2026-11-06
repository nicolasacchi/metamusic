package transform

import (
	"encoding/json"
	"math"
	"math/bits"

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

// maxUnits keeps 13 x units x NSPerUnitMilli inside int64.
const maxUnits = int64(1) << 50

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

func safetyUnits(outFrames int64) int64 { return DefaultCosts.SafetyUnitsPerFrame * outFrames }

// opsUnits is the work-unit model of a whole render of kind: D5's ops.Units (the operator's registered model plus safety for
// the kinds that do not finish themselves), so the estimate and the work meter agree by construction (T1-B2c; the plan's own
// formulas were replaced by the operators' prices). A kind ops does not register costs safety alone.
func opsUnits(kind Kind) func(UnitsIn) int64 {
	return func(in UnitsIn) int64 {
		if u, ok := ops.Units(string(kind), in.InFrames, in.OutFrames, in.Rate, in.Resolved); ok {
			return u
		}
		return safetyUnits(in.OutFrames)
	}
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

// planCPU turns the unit model of a kind into the CPU plan of one transform.
func planCPU(c CostConstants, spec KindSpec, u UnitsIn, inMS int64, maxCPUS int) CPUPlan {
	units := spec.Units(u)
	outMS := framesMS(u.OutFrames, u.Rate)
	exp := c.ExpectedMS(units, inMS, outMS, spec.Kind == KindScoreRender)
	return CPUPlan{
		Units: units, ExpectedMS: exp, MaxMS: BudgetMS(exp, maxCPUS),
		MemMB: kindMemoryMB(spec, u), OutBytes: OutBytesEst(u.OutFrames),
	}
}

// The streamed finish keeps scratch in the work dir (D5, measured): glacier 16 (raw spool) + 8 (detector) + 16 (limited
// signal) = 40 bytes a frame, every other kind 24, and out.pcm24 adds 6. T1-B2c.
const (
	scratchBytesGlacier = 40
	scratchBytesOther   = 24
	pcm24Bytes          = 6
)

// DiskBytesNeeded is the free space a render of outFrames frames needs under DataDir: the child's scratch and out.pcm24, then the
// FLAC encode and the ingested copy (twice out_bytes_est). It replaces the plan's 3 x out_bytes_est (about 11.7 bytes a frame,
// which a 600 s glacier passed and then ran out of disk).
func DiskBytesNeeded(kind Kind, outFrames int64) int64 {
	scratch := int64(scratchBytesOther)
	if kind == KindGlacier {
		scratch = scratchBytesGlacier
	}
	return (scratch+pcm24Bytes)*outFrames + 2*OutBytesEst(outFrames)
}

// shepardBankMB is D4's bound on the voice signals one phase-vocoder bank call keeps alive (ops: 384 MiB), which dominates a
// long shepard. Measured by D5: the default shepard (120 s out, 5 layers) peaks at 780 MiB resident.
const shepardBankMB = 384

// kindMemoryMB is mem_mb_est (C209.4) with the per-kind corrections measured by D5 (T1-B2c): glacier streams through
// safety.FinishStream, so it holds the input and a window (124 MiB peak for 600 s of output), not its output; shepard adds the
// voice-group buffers of the bank on top of the generic model (estimate 840 MiB against 780 measured for the default).
func kindMemoryMB(spec KindSpec, u UnitsIn) int64 {
	switch spec.Kind {
	case KindGlacier:
		n := nextPow2(int64(math.Round(paramFloat(paramsMap(u.Resolved), "window_s", 0.5) * float64(u.Rate))))
		return MemoryMB(u.InFrames+8*n, 0, 0)
	case KindShepard:
		return MemoryMB(u.InFrames, u.OutFrames, spec.MemFactor) + shepardBankMB
	}
	return MemoryMB(u.InFrames, u.OutFrames, spec.MemFactor)
}
