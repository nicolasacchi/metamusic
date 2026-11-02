package analysis

import "math"

// OnsetEnvelope is the C94 steps 1 and 2 (exported for the energy analyzer, C136.2): a half-wave-rectified log-spectral flux per hop, minus a moving mean.
func OnsetEnvelope(x []float32, sr int) []float64 {
	if len(x) < tempoWindow {
		return nil
	}
	nf := (len(x)-tempoWindow)/tempoHop + 1
	win := Hann(tempoWindow)
	lo := max(int(math.Ceil(tempoLoHz*tempoWindow/float64(sr))), 1)
	hi := min(int(tempoHiHz*tempoWindow/float64(sr)), tempoWindow/2)
	re, im := make([]float64, tempoWindow), make([]float64, tempoWindow)
	width := hi - lo + 1
	prev, cur, nxt := make([]float64, width), make([]float64, width), make([]float64, width)
	env := make([]float64, nf)
	flux := func(f int) {
		if f == 0 {
			return
		}
		var s float64
		for k := range cur {
			if d := cur[k] - prev[k]; d > 0 {
				s += d
			}
		}
		env[f] = s
	}
	// two real frames per complex transform: frame f in the real part, frame f+1 in the imaginary part
	for f := 0; f < nf; f += 2 {
		two := f+1 < nf
		o0, o1 := f*tempoHop, (f+1)*tempoHop
		for i := range tempoWindow {
			re[i] = float64(x[o0+i]) * win[i]
			im[i] = 0
			if two {
				im[i] = float64(x[o1+i]) * win[i]
			}
		}
		FFT(re, im)
		for k := lo; k <= hi; k++ {
			m := tempoWindow - k
			ar, ai := (re[k]+re[m])/2, (im[k]-im[m])/2
			cur[k-lo] = math.Log1p(tempoLogGain * math.Sqrt(ar*ar+ai*ai))
			if two {
				br, bi := (im[k]+im[m])/2, (re[m]-re[k])/2
				nxt[k-lo] = math.Log1p(tempoLogGain * math.Sqrt(br*br+bi*bi))
			}
		}
		flux(f)
		if two {
			prev, cur, nxt = cur, nxt, prev
			flux(f + 1)
			prev, cur = cur, prev
		} else {
			prev, cur = cur, prev
		}
	}
	// subtract a centred moving mean of 0.4 s, clamp at zero
	fps := float64(sr) / tempoHop
	w := max(int(tempoMeanS*fps), 3)
	pre := make([]float64, nf+1)
	for i, v := range env {
		pre[i+1] = pre[i] + v
	}
	out := make([]float64, nf)
	for i := range env {
		a, b := max(i-w/2, 0), min(i+w/2+1, nf)
		if v := env[i] - (pre[b]-pre[a])/float64(b-a); v > 0 {
			out[i] = v
		}
	}
	return out
}
