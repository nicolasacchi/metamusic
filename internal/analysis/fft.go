package analysis

import (
	"math"
	"math/bits"
)

// Hann returns the periodic Hann window of length n.
func Hann(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n))
	}
	return w
}

// FFT is an in-place radix-2 decimation-in-time transform of (re, im).
// len(re) == len(im) must be a power of two; otherwise it panics.
func FFT(re, im []float64) {
	n := len(re)
	if n != len(im) || n == 0 || n&(n-1) != 0 {
		panic("analysis: FFT length must be a non-zero power of two")
	}
	if n == 1 {
		return
	}
	shift := 64 - bits.Len(uint(n-1))
	for i := 0; i < n; i++ {
		j := int(bits.Reverse64(uint64(i)) >> uint(shift)) // #nosec G115 -- i < n and the shifted value is < n
		if j > i {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		ang := -2 * math.Pi / float64(size)
		wr, wi := math.Cos(ang), math.Sin(ang)
		for start := 0; start < n; start += size {
			cr, ci := 1.0, 0.0
			for k := 0; k < half; k++ {
				a, b := start+k, start+k+half
				tr := re[b]*cr - im[b]*ci
				ti := re[b]*ci + im[b]*cr
				re[b], im[b] = re[a]-tr, im[a]-ti
				re[a], im[a] = re[a]+tr, im[a]+ti
				cr, ci = cr*wr-ci*wi, cr*wi+ci*wr
			}
		}
	}
}
