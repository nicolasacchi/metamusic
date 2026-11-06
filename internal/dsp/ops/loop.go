package ops

import (
	"encoding/json"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// RuleLoopSource is the shepard rule of C202.3 when neither loop_s nor a bar count with a tempo is available. (ParamError and
// the other Rule* names live in stereo.go.)
const RuleLoopSource = "loop_source_required"

// parseParams decodes the resolved params object of job.json into v (unknown members are the service's business, not the
// operator's): an absent or null object leaves v at its defaults.
func parseParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &ParamError{Field: "params", Rule: "type"}
	}
	return nil
}

// roundHalfUp is the rounding of every length in this package: floor(x + 0.5).
func roundHalfUp(x float64) int { return int(math.Floor(x + 0.5)) }

func ceilDiv(a, b int) int { return (a + b - 1) / b }

// LoopCrossfadeS is the seam crossfade of the shepard loop (C205.4).
const LoopCrossfadeS = 0.030

// MakeLoop cuts a seamless cycle of length samples out of x, starting at start (samples outside x are zeros). The cycle is
// x[start, start+length) whose last xfade samples are replaced by an equal-power fade from the signal into the xfade samples
// that precede start, so the end of the cycle runs on into its own beginning: the wrap has no step beyond the signal's own
// slope. The fade follows a smoothstep so that the outgoing weight leaves the tail with zero slope. xfade is clamped to half
// the length.
func MakeLoop(x []float64, start, length, xfade int) []float64 {
	if length <= 0 {
		return nil
	}
	xfade = max(0, min(xfade, length/2))
	at := func(i int) float64 {
		if i < 0 || i >= len(x) {
			return 0
		}
		return x[i]
	}
	loop := make([]float64, length)
	for i := range loop {
		loop[i] = at(start + i)
	}
	for j := 0; j < xfade; j++ {
		u := float64(j+1) / float64(xfade+1)
		s := float64(u*u) * (3 - 2*u)
		th := math.Pi / 2 * s
		i := length - xfade + j
		loop[i] = float64(math.Cos(th)*loop[i]) + float64(math.Sin(th)*at(start-xfade+j))
	}
	return loop
}

// meterBlock is the granularity of the work meter inside the long linear passes: at most this many elements between checks.
const meterBlock = 8192

// meterBlocks runs fn over [0, n) in blocks of meterBlock elements and charges perElem units for each element before its block.
func meterBlocks(m *dsp.Meter, n int, perElem int64, fn func(lo, hi int)) error {
	for lo := 0; lo < n; lo += meterBlock {
		hi := min(lo+meterBlock, n)
		if err := m.Add(perElem * int64(hi-lo)); err != nil {
			return err
		}
		fn(lo, hi)
	}
	return nil
}

// Tile repeats loop to exactly n samples.
func Tile(loop []float64, n int) []float64 {
	out, _ := tile(loop, n, nil)
	return out
}

// tile is Tile charging one unit per output sample.
func tile(loop []float64, n int, m *dsp.Meter) ([]float64, error) {
	out := make([]float64, n)
	if len(loop) == 0 {
		return out, nil
	}
	err := meterBlocks(m, n, 1, func(lo, hi int) {
		for i := lo; i < hi; i++ {
			out[i] = loop[i%len(loop)]
		}
	})
	return out, err
}
