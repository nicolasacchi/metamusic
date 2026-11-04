package safety

import (
	"math"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// True-peak detection by 4x oversampling (C207.2): a 48-tap Kaiser-windowed-sinc polyphase filter, i.e. three interpolating
// phases of 12 taps between each pair of samples (phase 0 is the sample itself).

const (
	tpTaps  = 12
	tpLead  = 5 // taps before the sample: the 12 taps cover x[n-5 .. n+6]
	tpBeta  = 8.6
	tpFloor = -200.0 // dBTP reported for digital silence
)

var (
	tpOnce  sync.Once
	tpCoeff [3][tpTaps]float64
)

func tpTable() *[3][tpTaps]float64 {
	tpOnce.Do(func() {
		for p := 1; p <= 3; p++ {
			var sum float64
			for j := 0; j < tpTaps; j++ {
				t := float64(p)/4 - float64(j-tpLead)
				s := math.Sin(math.Pi*t) / (math.Pi * t) // t is never 0: p/4 is not an integer
				tpCoeff[p-1][j] = float64(s * dsp.Kaiser(t/6, tpBeta))
				sum += tpCoeff[p-1][j]
			}
			for j := range tpCoeff[p-1] {
				tpCoeff[p-1][j] /= sum
			}
		}
	})
	return &tpCoeff
}

// framePeaks is, per frame n, the largest |value| of either channel (linked) at the sample n and at the three oversampled
// points between n and n+1.
func framePeaks(b pcm.Buffer) []float64 {
	n := b.Frames()
	out := make([]float64, n)
	tb := tpTable()
	for c := range 2 {
		x := b.Ch[c]
		for i := range n {
			m := math.Abs(x[i])
			for p := range 3 {
				var acc float64
				for j := range tpTaps {
					if k := i + j - tpLead; k >= 0 && k < n {
						acc += float64(x[k] * tb[p][j])
					}
				}
				m = math.Max(m, math.Abs(acc))
			}
			if m > out[i] {
				out[i] = m
			}
		}
	}
	return out
}

func maxOf(x []float64) float64 {
	var m float64
	for _, v := range x {
		if v > m {
			m = v
		}
	}
	return m
}

// TruePeakDBTP is the true peak of b (both channels) in dBTP; digital silence reads -200.
func TruePeakDBTP(b pcm.Buffer) float64 {
	p := maxOf(framePeaks(b))
	if p <= 0 {
		return tpFloor
	}
	return float64(2 * db10(p))
}
