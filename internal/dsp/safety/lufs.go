package safety

import (
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// invLn10 turns dsp.Log into a base-10 logarithm without math.Log10.
const invLn10 = 1 / math.Ln10

func db10(x float64) float64 { return float64(10 * float64(dsp.Log(x)*invLn10)) }

// biquad is a second-order section in transposed direct form II.
type biquad struct{ b0, b1, b2, a1, a2 float64 }

func (q biquad) run(x []float64) []float64 {
	y := make([]float64, len(x))
	var z1, z2 float64
	for i, v := range x {
		o := float64(q.b0*v) + z1
		z1 = float64(q.b1*v) - float64(q.a1*o) + z2
		z2 = float64(q.b2*v) - float64(q.a2*o)
		y[i] = o
	}
	return y
}

// kFilters returns the two K-weighting sections of BS.1770-4 (high shelf, then the RLB high-pass) for rate, computed by
// the standard bilinear-transform formulas so any rate works.
func kFilters(rate int) (shelf, hp biquad) {
	fs := float64(rate)
	{
		const f0, gain, q = 1681.974450955533, 3.999843853973347, 0.7071752369554196
		k := math.Tan(math.Pi * f0 / fs)
		vh := dsp.Pow(10, gain/20)
		vb := dsp.Pow(vh, 0.4996667741545416)
		a0 := 1 + k/q + float64(k*k)
		shelf = biquad{
			b0: (vh + float64(vb*k)/q + float64(k*k)) / a0,
			b1: 2 * (float64(k*k) - vh) / a0,
			b2: (vh - float64(vb*k)/q + float64(k*k)) / a0,
			a1: 2 * (float64(k*k) - 1) / a0,
			a2: (1 - k/q + float64(k*k)) / a0,
		}
	}
	{
		const f0, q = 38.13547087602444, 0.5003270373238773
		k := math.Tan(math.Pi * f0 / fs)
		a0 := 1 + k/q + float64(k*k)
		hp = biquad{b0: 1, b1: -2, b2: 1, a1: 2 * (float64(k*k) - 1) / a0, a2: (1 - k/q + float64(k*k)) / a0}
	}
	return shelf, hp
}

// kWeightedSquares returns, per channel, the sums of squares of the K-weighted signal over consecutive steps of step samples
// (the last partial step is dropped), and the whole-buffer sum of squares.
func kWeightedSquares(b pcm.Buffer, step int) (sums [2][]float64, total [2]float64) {
	shelf, hp := kFilters(b.Rate)
	for c := range 2 {
		y := hp.run(shelf.run(b.Ch[c]))
		for i, v := range y {
			sq := float64(v * v)
			total[c] += sq
			if j := i / step; j < len(y)/step {
				if j >= len(sums[c]) {
					sums[c] = append(sums[c], 0)
				}
				sums[c][j] += sq
			}
		}
	}
	return sums, total
}

// blockEnergies are the mean-square energies z of the 400 ms blocks at a 100 ms step (channel gains 1.0). A buffer shorter
// than one block is measured as a single zero-padded block, so a very short render is not mistaken for silence.
func blockEnergies(b pcm.Buffer) []float64 {
	step := int(math.Floor(float64(b.Rate)*0.1 + 0.5))
	if step < 1 || b.Frames() == 0 {
		return nil
	}
	sums, total := kWeightedSquares(b, step)
	block := 4 * step
	if b.Frames() < block {
		return []float64{(total[0] + total[1]) / float64(block)}
	}
	nb := len(sums[0]) - 3
	z := make([]float64, 0, max(nb, 0))
	for j := 0; j < nb; j++ {
		var e float64
		for c := range 2 {
			e += (sums[c][j] + sums[c][j+1] + sums[c][j+2] + sums[c][j+3]) / float64(block)
		}
		z = append(z, e)
	}
	return z
}

const (
	absGateLUFS = -70.0
	relGateLU   = -10.0
	lkOffset    = -0.691
)

// LUFS is the integrated loudness of b by ITU-R BS.1770-4: K-weighting, channel gains 1.0, 400 ms blocks with a 100 ms
// step, an absolute gate at -70 LUFS and a relative gate 10 LU under the mean of the blocks that pass it. ok is false when no
// block survives the gates.
func LUFS(b pcm.Buffer) (integrated float64, ok bool) {
	z := blockEnergies(b)
	loud := func(e float64) float64 { return lkOffset + db10(e) }
	var sum float64
	var n int
	for _, e := range z {
		if e > 0 && loud(e) > absGateLUFS {
			sum += e
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	gate := loud(sum/float64(n)) + relGateLU
	sum, n = 0, 0
	for _, e := range z {
		if e > 0 && loud(e) > absGateLUFS && loud(e) > gate {
			sum += e
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return loud(sum / float64(n)), true
}
