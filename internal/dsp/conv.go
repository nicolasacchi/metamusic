package dsp

// ConvolveUP is the uniform-partitioned overlap-save convolution of C206.5: h is cut into partitions of `block` samples whose
// spectra (FFT size 2*block) are multiplied with a frequency-domain delay line of the input's spectra, one block of output
// per step. The output length is len(x) + len(h) - 1.

// DefaultConvBlock is the block size used when ConvolveUP is given block <= 0 (FFT size 8192).
const DefaultConvBlock = 4096

// ConvolveUP convolves x with h. It returns nil when an input is empty or the meter ends the work; use ConvolveUPErr to
// learn which.
func ConvolveUP(x, h []float64, block int, m *Meter) []float64 {
	y, _ := ConvolveUPErr(x, h, block, m)
	return y
}

// ConvolveUPErr is ConvolveUP returning the meter's error (ErrBudget or the context's).
func ConvolveUPErr(x, h []float64, block int, m *Meter) ([]float64, error) {
	if len(x) == 0 || len(h) == 0 {
		return nil, nil
	}
	cs := NewConvStream(h, block)
	b := cs.Block()
	outLen := len(x) + len(h) - 1
	y := make([]float64, outLen)
	for j := 0; j*b < outLen; j++ {
		var in []float64
		if j*b < len(x) {
			in = x[j*b : min((j+1)*b, len(x))]
		}
		out, err := cs.Push(in, m)
		if err != nil {
			return nil, err
		}
		copy(y[j*b:min((j+1)*b, outLen)], out)
	}
	return y, nil
}

// ConvStream is ConvolveUP as a stream (T1 hi-fi): the input is pushed one block at a time and the output comes back one block at a
// time, with the arithmetic of ConvolveUPErr, which is built on it, so a signal convolved in pieces gives exactly the samples of the
// whole. Memory is the spectra of the partitions of h and of the delay line, not of the signal.
type ConvStream struct {
	block, fftN, bins, parts int
	hr, hi, dr, di           [][]float64
	win, prev, accR, accI    []float64
	j                        int
}

// NewConvStream prepares the convolution with h (not empty) at the given block size (<= 0: DefaultConvBlock).
func NewConvStream(h []float64, block int) *ConvStream {
	if block <= 0 {
		block = DefaultConvBlock
	}
	block = NextPow2(block)
	c := &ConvStream{block: block, fftN: 2 * block, bins: block + 1, parts: (len(h) + block - 1) / block}
	// spectra of the filter partitions
	c.hr, c.hi = make([][]float64, c.parts), make([][]float64, c.parts)
	for p := range c.hr {
		seg := h[p*block : min((p+1)*block, len(h))]
		c.hr[p], c.hi[p] = RFFT(seg, c.fftN)
	}
	// frequency-domain delay line of the last `parts` input spectra
	c.dr, c.di = make([][]float64, c.parts), make([][]float64, c.parts)
	for p := range c.dr {
		c.dr[p], c.di[p] = make([]float64, c.bins), make([]float64, c.bins)
	}
	c.win, c.prev = make([]float64, c.fftN), make([]float64, block)
	c.accR, c.accI = make([]float64, c.bins), make([]float64, c.bins)
	return c
}

// Block is the block size: the input of Push is at most this long and its output exactly this long.
func (c *ConvStream) Block() int { return c.block }

// Parts is the number of partitions of the filter.
func (c *ConvStream) Parts() int { return c.parts }

// BlockUnits is what one Push spends on the meter.
func (c *ConvStream) BlockUnits() int64 { return Units.ConvBlock(c.parts, c.fftN) }

// Push gives the next block of the input (at most Block samples; nil or short means zeros: the end of the signal) and returns the
// next block of the output, samples j*Block .. (j+1)*Block of the full convolution (len(x)+len(h)-1 samples). The result is only
// valid until the next call.
func (c *ConvStream) Push(in []float64, m *Meter) ([]float64, error) {
	if err := m.Add(c.BlockUnits()); err != nil {
		return nil, err
	}
	if len(in) > c.block {
		in = in[:c.block]
	}
	// the window is the previous block and this one, zero beyond the signal
	copy(c.win, c.prev)
	n := copy(c.win[c.block:], in)
	clear(c.win[c.block+n:])
	c.prev = append(c.prev[:0], in...)
	c.prev = append(c.prev, make([]float64, c.block-len(in))...)
	slot := c.j % c.parts
	c.dr[slot], c.di[slot] = RFFT(c.win, c.fftN)
	for k := range c.accR {
		c.accR[k], c.accI[k] = 0, 0
	}
	for p := 0; p < c.parts && p <= c.j; p++ {
		s := ((c.j-p)%c.parts + c.parts) % c.parts
		xr, xi := c.dr[s], c.di[s]
		for k := 0; k < c.bins; k++ {
			c.accR[k] += float64(xr[k]*c.hr[p][k]) - float64(xi[k]*c.hi[p][k])
			c.accI[k] += float64(xr[k]*c.hi[p][k]) + float64(xi[k]*c.hr[p][k])
		}
	}
	c.j++
	return IRFFT(c.accR, c.accI, c.fftN)[c.block:], nil
}
