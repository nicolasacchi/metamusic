package dsp

// STUB(T1-D1): the FFT of C203.2: an iterative radix-2 transform with a per-size twiddle table precomputed from math.Sin
// and math.Cos of each index (no recurrence). The signatures are frozen; the bodies are D1's.

// FFT transforms (re, im) in place. len(re) == len(im) must be a power of two.
func FFT(re, im []float64) { panic("STUB(T1-D1): dsp.FFT") }

// IFFT is the inverse of FFT, scaled by 1/n.
func IFFT(re, im []float64) { panic("STUB(T1-D1): dsp.IFFT") }

// RFFT transforms the real signal x (zero-padded to n, a power of two) and returns the n/2+1 non-negative bins.
func RFFT(x []float64, n int) (re, im []float64) { panic("STUB(T1-D1): dsp.RFFT") }

// IRFFT inverts RFFT, returning n real samples.
func IRFFT(re, im []float64, n int) []float64 { panic("STUB(T1-D1): dsp.IRFFT") }

// NextPow2 is the smallest power of two that is at least n (1 for n <= 1).
func NextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}
