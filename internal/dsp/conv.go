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
	if block <= 0 {
		block = DefaultConvBlock
	}
	block = NextPow2(block)
	fftN := 2 * block
	bins := block + 1
	outLen := len(x) + len(h) - 1
	parts := (len(h) + block - 1) / block
	nBlocks := (outLen + block - 1) / block

	// spectra of the filter partitions
	hr, hi := make([][]float64, parts), make([][]float64, parts)
	for p := range hr {
		seg := h[p*block : min((p+1)*block, len(h))]
		hr[p], hi[p] = RFFT(seg, fftN)
	}
	// frequency-domain delay line of the last `parts` input spectra
	dr, di := make([][]float64, parts), make([][]float64, parts)
	for p := range dr {
		dr[p], di[p] = make([]float64, bins), make([]float64, bins)
	}
	y := make([]float64, outLen)
	win := make([]float64, fftN)
	accR, accI := make([]float64, bins), make([]float64, bins)
	for j := 0; j < nBlocks; j++ {
		if err := m.Add(Units.ConvBlock(parts, fftN)); err != nil {
			return nil, err
		}
		// input window [(j-1)*block, (j+1)*block)
		for i := range win {
			src := (j-1)*block + i
			if src >= 0 && src < len(x) {
				win[i] = x[src]
			} else {
				win[i] = 0
			}
		}
		slot := j % parts
		dr[slot], di[slot] = RFFT(win, fftN)
		for k := range accR {
			accR[k], accI[k] = 0, 0
		}
		for p := 0; p < parts && p <= j; p++ {
			s := ((j-p)%parts + parts) % parts
			xr, xi := dr[s], di[s]
			for k := 0; k < bins; k++ {
				accR[k] += float64(xr[k]*hr[p][k]) - float64(xi[k]*hi[p][k])
				accI[k] += float64(xr[k]*hi[p][k]) + float64(xi[k]*hr[p][k])
			}
		}
		out := IRFFT(accR, accI, fftN)
		copy(y[j*block:min((j+1)*block, outLen)], out[block:])
	}
	return y, nil
}
