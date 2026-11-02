package analysis

import (
	"math"
)

// EnergyComponents are the three mapped terms of the energy value, each in [0, 1]; Loudness is nil without a stored LUFS.
type EnergyComponents struct {
	Onset    float64  `json:"onset"`
	Centroid float64  `json:"centroid"`
	Loudness *float64 `json:"loudness"`
}

// Energy is the mm-energy/1 result: a 0..1 mix of onset rate, spectral centroid and loudness. It is a measured proxy for
// "how driving the take sounds", not perceptual arousal. Every field but AnalyzedS is nil when the file is shorter than
// 20 s or silent.
type Energy struct {
	Energy     *float64          `json:"energy"`
	OnsetRate  *float64          `json:"onset_rate"` // onsets per second
	CentroidHz *float64          `json:"centroid_hz"`
	LUFS       *float64          `json:"lufs"`
	Components *EnergyComponents `json:"components"`
	AnalyzedS  float64           `json:"analyzed_s"`
}

// lerp01 maps v linearly from [lo, hi] to [0, 1], clamped.
func lerp01(v, lo, hi float64) float64 { return math.Min(1, math.Max(0, (v-lo)/(hi-lo))) }

// ComputeEnergy measures the energy of p (C136.2). lufs is the stored integrated loudness of the take (nil when absent;
// its term is then dropped and the weights renormalised). Pure and deterministic; input beyond 240 s is ignored.
func ComputeEnergy(p PCM, lufs *float64) Energy {
	var res Energy
	if p.Rate <= 0 {
		return res
	}
	x := p.Samples
	if maxN := int(tempoMaxS * float64(p.Rate)); len(x) > maxN {
		x = x[:maxN]
	}
	res.AnalyzedS = round(float64(len(x))/float64(p.Rate), 2)
	ep := &energyParams
	if res.AnalyzedS < ep.MinS || silent(x, ep.SilentDBFS) {
		return res
	}
	x = sanitize(x)
	rate := onsetRate(x, p.Rate, res.AnalyzedS)
	centroid, ok := meanCentroid(x, p.Rate)
	if !ok {
		return res
	}
	comp := EnergyComponents{Onset: lerp01(rate, ep.OnsetLo, ep.OnsetHi), Centroid: lerp01(centroid, ep.CentroidLo, ep.CentroidHi)}
	num, den := ep.WOnset*comp.Onset+ep.WCentroid*comp.Centroid, ep.WOnset+ep.WCentroid
	if lufs != nil && !math.IsNaN(*lufs) && !math.IsInf(*lufs, 0) {
		m := lerp01(*lufs, ep.LoudLo, ep.LoudHi)
		comp.Loudness = &m
		num, den = num+ep.WLoud*m, den+ep.WLoud
		l := round(*lufs, 1)
		res.LUFS = &l
	}
	e, r, c := round(num/den, 4), round(rate, 3), round(centroid, 1)
	comp.Onset, comp.Centroid = round(comp.Onset, 4), round(comp.Centroid, 4)
	if comp.Loudness != nil {
		m := round(*comp.Loudness, 4)
		comp.Loudness = &m
	}
	res.Energy, res.OnsetRate, res.CentroidHz, res.Components = &e, &r, &c, &comp
	return res
}

// silent reports whether every 1024-sample frame of x has an RMS below thresholdDBFS.
func silent(x []float32, thresholdDBFS float64) bool {
	const frame = 1024
	limit := math.Pow(10, thresholdDBFS/10) // mean square
	for i := 0; i < len(x); i += frame {
		var s float64
		n := min(frame, len(x)-i)
		for _, v := range x[i : i+n] {
			s += float64(v) * float64(v)
		}
		if s/float64(n) >= limit {
			return false
		}
	}
	return true
}

// onsetRate counts the peaks of the onset-strength envelope (C94) above mean + σ at least OnsetGapS apart, per second.
func onsetRate(x []float32, sr int, analyzedS float64) float64 {
	env := OnsetEnvelope(x, sr)
	if len(env) == 0 || analyzedS <= 0 {
		return 0
	}
	var sum, sq float64
	for _, v := range env {
		sum += v
		sq += v * v
	}
	n := float64(len(env))
	mean := sum / n
	thr := mean + energyParams.OnsetSigma*math.Sqrt(math.Max(sq/n-mean*mean, 0))
	gap := max(int(math.Round(energyParams.OnsetGapS*float64(sr)/tempoHop)), 1)
	var peaks []int
	for i, v := range env {
		if v <= thr || v < energyParams.OnsetFloor || (i > 0 && v <= env[i-1]) || (i+1 < len(env) && v < env[i+1]) {
			continue
		}
		if last := len(peaks) - 1; last >= 0 && i-peaks[last] < gap {
			if v > env[peaks[last]] {
				peaks[last] = i
			}
			continue
		}
		peaks = append(peaks, i)
	}
	return float64(len(peaks)) / analyzedS
}

// meanCentroid is the mean magnitude-weighted spectral centroid (Hz) over the STFT frames within CentroidRangeDB of the
// loudest one; ok is false when no frame has energy in the band.
func meanCentroid(x []float32, sr int) (float64, bool) {
	ep := &energyParams
	w := ep.CentroidWindow
	if len(x) < w {
		return 0, false
	}
	nf := (len(x)-w)/ep.CentroidHop + 1
	win := Hann(w)
	lo := max(int(math.Ceil(ep.CentroidLoHz*float64(w)/float64(sr))), 1)
	hi := min(int(ep.CentroidHiHz*float64(w)/float64(sr)), w/2)
	re, im := make([]float64, w), make([]float64, w)
	cents, energies := make([]float64, nf), make([]float64, nf)
	var top float64
	band := func(f int, mag func(k int) float64) {
		var e, num, den float64
		for k := lo; k <= hi; k++ {
			m := mag(k)
			e += m * m
			num += m * float64(k)
			den += m
		}
		energies[f] = e
		if den > 0 {
			cents[f] = num / den * float64(sr) / float64(w)
		}
		top = max(top, e)
	}
	// two real frames per complex transform: frame f in the real part, frame f+1 in the imaginary part
	for f := 0; f < nf; f += 2 {
		two := f+1 < nf
		o0, o1 := f*ep.CentroidHop, (f+1)*ep.CentroidHop
		for i := range w {
			re[i] = float64(x[o0+i]) * win[i]
			im[i] = 0
			if two {
				im[i] = float64(x[o1+i]) * win[i]
			}
		}
		FFT(re, im)
		band(f, func(k int) float64 { return math.Hypot((re[k]+re[w-k])/2, (im[k]-im[w-k])/2) })
		if two {
			band(f+1, func(k int) float64 { return math.Hypot((im[k]+im[w-k])/2, (re[w-k]-re[k])/2) })
		}
	}
	if top <= 0 {
		return 0, false
	}
	floor := top * math.Pow(10, -ep.CentroidRangeDB/10)
	var sum float64
	var cnt int
	for f := range nf {
		if energies[f] >= floor {
			sum += cents[f]
			cnt++
		}
	}
	return sum / float64(cnt), cnt > 0
}
