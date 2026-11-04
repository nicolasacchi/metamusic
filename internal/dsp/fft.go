package dsp

import (
	"math"
	"math/bits"
	"sync"
)

// The FFT of C203.2: an iterative radix-2 transform with a per-size twiddle table precomputed from math.Sin and math.Cos of
// the exact index (no recurrence, whose error grows with size). Only the first octant is evaluated; the other seven follow by
// exact symmetry, so cos(pi/2) is exactly 0 and the table is reproducible on any host.

// twiddles holds cos and sin of 2*pi*k/n for k < n/2.
type twiddles struct{ cos, sin []float64 }

const maxCachedTwiddle = 1 << 17

var (
	twMu    sync.Mutex
	twCache = map[int]*twiddles{}
)

func buildTwiddles(n int) *twiddles {
	h := n / 2
	t := &twiddles{cos: make([]float64, h), sin: make([]float64, h)}
	direct := func(k int) (c, s float64) {
		a := 2 * math.Pi * float64(k) / float64(n)
		return math.Cos(a), math.Sin(a)
	}
	for k := 0; k < h; k++ {
		switch {
		case n < 8 || k <= n/8:
			t.cos[k], t.sin[k] = direct(k)
		case k <= n/4:
			c, s := direct(n/4 - k) // cos(a) = sin(pi/2 - a), sin(a) = cos(pi/2 - a)
			t.cos[k], t.sin[k] = s, c
		default:
			c, s := t.cos[n/2-k], t.sin[n/2-k] // second quadrant: mirror about pi/2
			t.cos[k], t.sin[k] = -c, s
		}
	}
	return t
}

func twiddlesFor(n int) *twiddles {
	if n > maxCachedTwiddle {
		return buildTwiddles(n)
	}
	twMu.Lock()
	defer twMu.Unlock()
	t, ok := twCache[n]
	if !ok {
		t = buildTwiddles(n)
		twCache[n] = t
	}
	return t
}

func checkPow2(re, im []float64) int {
	n := len(re)
	if n != len(im) || n == 0 || n&(n-1) != 0 {
		panic("dsp: FFT length must be a non-zero power of two and equal for re and im")
	}
	return n
}

func fft(re, im []float64, inverse bool) {
	n := checkPow2(re, im)
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
	tw := twiddlesFor(n)
	sign := -1.0
	if inverse {
		sign = 1
	}
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		stride := n / size
		for start := 0; start < n; start += size {
			for k := 0; k < half; k++ {
				wr, wi := tw.cos[k*stride], float64(sign*tw.sin[k*stride])
				a, b := start+k, start+k+half
				tr := float64(re[b]*wr) - float64(im[b]*wi)
				ti := float64(re[b]*wi) + float64(im[b]*wr)
				re[b], im[b] = re[a]-tr, im[a]-ti
				re[a], im[a] = re[a]+tr, im[a]+ti
			}
		}
	}
}

// FFT transforms (re, im) in place (forward, e^{-i...}). len(re) == len(im) must be a power of two.
func FFT(re, im []float64) { fft(re, im, false) }

// IFFT is the inverse of FFT, scaled by 1/n.
func IFFT(re, im []float64) {
	fft(re, im, true)
	inv := 1 / float64(len(re))
	for i := range re {
		re[i] = float64(re[i] * inv)
		im[i] = float64(im[i] * inv)
	}
}

// RFFT transforms the real signal x (zero-padded, or cut, to n, a power of two) and returns the n/2+1 non-negative bins.
func RFFT(x []float64, n int) (re, im []float64) {
	r, i := make([]float64, n), make([]float64, n)
	copy(r, x)
	FFT(r, i)
	return r[:n/2+1], i[:n/2+1]
}

// IRFFT inverts RFFT, returning n real samples from the n/2+1 bins (the negative half follows by Hermitian symmetry).
func IRFFT(re, im []float64, n int) []float64 {
	r, i := make([]float64, n), make([]float64, n)
	for k := 0; k <= n/2 && k < len(re); k++ {
		r[k], i[k] = re[k], im[k]
		if k > 0 && k < n-k {
			r[n-k], i[n-k] = re[k], -im[k]
		}
	}
	IFFT(r, i)
	return r
}

// NextPow2 is the smallest power of two that is at least n (1 for n <= 1).
func NextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}
