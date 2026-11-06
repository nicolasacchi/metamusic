package ir

import (
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// bandMasks are the zero-phase frequency masks of the seven octave bands over an n-point spectrum: around every band edge the
// two neighbouring masks cross over with a raised cosine in log-frequency (half an octave wide), so they sum to one at every
// bin and the band-split noise is exactly the noise when all bands decay alike.
type bandMasks struct {
	n      int
	ka, kb [len(BandEdgesHz)]int       // transition bin range of each edge
	lp     [len(BandEdgesHz)][]float64 // low-pass weight of each edge over its transition range
}

func newBandMasks(n, rate int) *bandMasks {
	m := &bandMasks{n: n}
	w := dsp.Exp(edgeOctaves * math.Ln2)
	for e, edge := range BandEdgesHz {
		fa, fb := edge/w, edge*w
		m.ka[e] = int(math.Ceil(fa * float64(n) / float64(rate)))
		m.kb[e] = int(math.Floor(fb * float64(n) / float64(rate)))
		m.lp[e] = make([]float64, max(m.kb[e]-m.ka[e]+1, 0))
		for k := m.ka[e]; k <= m.kb[e]; k++ {
			f := float64(k) * float64(rate) / float64(n)
			u := (dsp.Log(f/edge)/math.Ln2 + edgeOctaves) / (2 * edgeOctaves)
			m.lp[e][k-m.ka[e]] = 0.5 * (1 + math.Cos(math.Pi*math.Max(0, math.Min(1, u))))
		}
	}
	return m
}

// lowpass is the weight of edge e at bin k: 1 below the transition, 0 above.
func (m *bandMasks) lowpass(e, k int) float64 {
	switch {
	case k < m.ka[e]:
		return 1
	case k > m.kb[e]:
		return 0
	}
	return m.lp[e][k-m.ka[e]]
}

// at is the weight of band b at bin k.
func (m *bandMasks) at(b, k int) float64 {
	v := 1.0
	if b > 0 {
		v = 1 - m.lowpass(b-1, k)
	}
	if b < len(BandEdgesHz) {
		v *= m.lowpass(b, k)
	}
	return v
}

// span is the bin range (inclusive, within 0..n/2) where band b can be non-zero.
func (m *bandMasks) span(b int) (lo, hi int) {
	lo, hi = 0, m.n/2
	if b > 0 {
		lo = max(m.ka[b-1], 0)
	}
	if b < len(BandEdgesHz) {
		hi = min(m.kb[b], m.n/2)
	}
	return lo, hi
}
