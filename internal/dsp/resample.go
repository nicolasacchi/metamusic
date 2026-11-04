package dsp

import "math"

// The resampler of C203.3: a windowed-sinc interpolator (Kaiser window, beta 8.6; `zeros` zero crossings on each side of the
// lowpass; cutoff min(1, ratio) * 0.97 of the input Nyquist (ratio = out/in, so the lower of the two Nyquist rates)), table-driven with 1024 sub-sample steps per zero spacing and
// linear interpolation between steps. The output length is exactly round(len(x) * ratio). The pipeline never calls an ffmpeg
// resampler (D-T1-19): a second input at another rate lands on the first input's grid through this function.

const (
	// DefaultZeros is the number of zero crossings used when Resample is given zeros <= 0.
	DefaultZeros = 32

	kaiserBeta       = 8.6
	resampleSteps    = 1024
	resampleCutoff   = 0.97
	resampleMeterGrp = 1024 // outputs between meter checks
)

// besselI0 is the modified Bessel function of the first kind, order 0, by its power series (only + and *).
func besselI0(x float64) float64 {
	q := float64(x*x) / 4
	sum, term := 1.0, 1.0
	for k := 1; k < 500; k++ {
		term = float64(term*q) / float64(k*k)
		sum += term
		if term < 1e-18*sum {
			break
		}
	}
	return sum
}

// Kaiser is the Kaiser window at r in [-1, 1] (0 outside): I0(beta*sqrt(1-r^2)) / I0(beta).
func Kaiser(r, beta float64) float64 {
	if r < -1 || r > 1 {
		return 0
	}
	return besselI0(beta*math.Sqrt(1-float64(r*r))) / besselI0(beta)
}

// kernelTable is g(u) = sinc(u) * kaiser(u / zeros) sampled at u = i / resampleSteps.
func kernelTable(zeros int) []float64 {
	n := zeros*resampleSteps + 2
	tbl := make([]float64, n)
	i0 := besselI0(kaiserBeta)
	for i := range tbl {
		u := float64(i) / resampleSteps
		r := u / float64(zeros)
		if r > 1 {
			continue
		}
		w := besselI0(kaiserBeta*math.Sqrt(1-float64(r*r))) / i0
		sinc := 1.0
		if i > 0 {
			sinc = math.Sin(math.Pi*u) / (math.Pi * u)
		}
		tbl[i] = float64(sinc * w)
	}
	return tbl
}

// Resample converts x by ratio = outRate/inRate. zeros <= 0 means DefaultZeros.
func Resample(x []float64, ratio float64, zeros int) []float64 {
	y, _ := ResampleMetered(x, ratio, zeros, nil)
	return y
}

// ResampleMetered is Resample with a work meter (2*zeros units per output sample, checked every 1024 samples). It returns
// nil and the meter's error when the budget or the context ends the work, and nil for a ratio that is not positive and finite.
func ResampleMetered(x []float64, ratio float64, zeros int, m *Meter) ([]float64, error) {
	if !(ratio > 0) || math.IsInf(ratio, 0) {
		return nil, nil
	}
	if zeros <= 0 {
		zeros = DefaultZeros
	}
	outLen := int(math.Floor(float64(len(x))*ratio + 0.5))
	y := make([]float64, outLen)
	if outLen == 0 || len(x) == 0 {
		return y, nil
	}
	fc := math.Min(1, ratio) * resampleCutoff // the lower of the two Nyquist rates, relative to the input's
	halfWidth := float64(zeros) / fc
	tbl := kernelTable(zeros)
	last := len(x) - 1
	for n := range y {
		if n%resampleMeterGrp == 0 {
			if err := m.Add(Units.ResampleSample(zeros) * int64(min(resampleMeterGrp, outLen-n))); err != nil {
				return nil, err
			}
		}
		p := float64(n) / ratio
		lo := max(int(math.Ceil(p-halfWidth)), 0)
		hi := min(int(math.Floor(p+halfWidth)), last)
		var acc float64
		for i := lo; i <= hi; i++ {
			pos := float64(fc*math.Abs(p-float64(i))) * resampleSteps
			idx := int(pos)
			frac := pos - float64(idx)
			w := tbl[idx] + float64(frac*(tbl[idx+1]-tbl[idx]))
			acc += float64(x[i] * w)
		}
		y[n] = float64(fc * acc)
	}
	return y, nil
}
