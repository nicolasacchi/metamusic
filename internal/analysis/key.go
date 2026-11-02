package analysis

import (
	"math"
	"slices"
	"strconv"
)

// KeyDef identifies the key analyzer (C136.1). analyses.version = KeyDef + " " + tool token.
const KeyDef = "mm-key/1"

// KeyEnabled is the scope valve of D-L3-35: false means no key is ever set (the analyzer still stores its null result).
const KeyEnabled = true

// Key analysis parameters (C136.1). The thresholds are the values the E1 prototype measured (the accuracy table is in
// key_test.go next to TestKey_SyntheticTriads); they change only with a new mm-key/2 and a golden.
const (
	keyWindow    = 8192 // Hann; 1.35 Hz per bin at 11 025 Hz, a semitone at 110 Hz is about 5 bins
	keyHop       = 4096
	keyLoHz      = 110.0 // A2
	keyHiHz      = 2000.0
	keyMinS      = 20.0
	keyMaxS      = 240.0
	keyMinR      = 0.45 // Pearson floor of the best profile
	keyMinMargin = 0.04 // best minus runner-up
	keyAmbigRel  = 0.05 // runner-up is the relative key within this margin: ambiguous_relative
	keyPeakRel   = 0.2  // a spectral peak counts when its compressed magnitude is at least this fraction of the frame's strongest
	keyMaxPeaks  = 24   // strongest peaks kept per frame
	keyFloor     = 0.01 // frames whose strongest |X| is below this are silent (about -106 dBFS for a full-window sine)
	keyTuneBins  = 100  // cent histogram
	keyHarmonics = 6    // a peak also votes for the pitch classes of its subharmonics f/2 .. f/6 (an overtone is not a note)
	keyHarmDecay = 0.7  // weight factor per harmonic order
	keyMinTuneR  = 0.2  // resultant length of the cent histogram below which no tuning offset is applied
)

// KeyCandidate is one runner-up key.
type KeyCandidate struct {
	Key string  `json:"key"`
	R   float64 `json:"r"`
}

// Key is the mm-key/1 result. Key, Tonic, Mode and Camelot are nil when no key was set (a short, silent, atonal or
// percussive signal); R and Margin are then still those of the best profile. Tonic is a pitch class (C = 0).
type Key struct {
	Key               *string        `json:"key"`
	Tonic             *int           `json:"tonic"`
	Mode              *string        `json:"mode"`
	Camelot           *string        `json:"camelot"`
	R                 float64        `json:"r"`
	Margin            float64        `json:"margin"`
	AmbiguousRelative bool           `json:"ambiguous_relative"`
	TuningCents       float64        `json:"tuning_cents"`
	Alternatives      []KeyCandidate `json:"alternatives" nullable:"false"` // at most 3, after the best
	AnalyzedS         float64        `json:"analyzed_s"`
}

// Krumhansl-Schmuckler profiles (the published values), indexed from the tonic.
var (
	ksMajor = [12]float64{6.35, 2.23, 3.48, 2.33, 4.38, 4.09, 2.52, 5.19, 2.39, 3.66, 2.29, 2.88}
	ksMinor = [12]float64{6.33, 2.68, 3.52, 5.38, 2.60, 3.53, 2.54, 4.75, 3.98, 2.69, 3.34, 3.17}
)

var (
	keyMajorNames = [12]string{"C", "Db", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B"}
	keyMinorNames = [12]string{"C", "C#", "D", "Eb", "E", "F", "F#", "G", "G#", "A", "Bb", "B"}
	// camelotMajor is the Camelot number of each major tonic; a minor key shares the number of its relative major.
	camelotMajor = [12]int{8, 3, 10, 5, 12, 7, 2, 9, 4, 11, 6, 1}
)

// KeyName renders "A minor" for a tonic pitch class (C = 0) and mode.
func KeyName(tonic int, minor bool) string {
	if minor {
		return keyMinorNames[tonic] + " minor"
	}
	return keyMajorNames[tonic] + " major"
}

// KeyCamelot renders the Camelot code ("8A") for a tonic pitch class and mode.
func KeyCamelot(tonic int, minor bool) string {
	if minor {
		return strconv.Itoa(camelotMajor[(tonic+3)%12]) + "A"
	}
	return strconv.Itoa(camelotMajor[tonic]) + "B"
}

type keyPeak struct{ semis, w float64 } // semitones above A4 and compressed magnitude

type keyCand struct {
	tonic int
	minor bool
	r     float64
}

// ComputeKey estimates the musical key of p (C136.1): a tuning-corrected chroma from the spectral peaks of an
// 8192-point STFT, correlated with the 24 Krumhansl-Schmuckler profiles. Pure and deterministic; input beyond 240 s is
// ignored and under 20 s there is no key.
func ComputeKey(p PCM) Key {
	res := Key{Alternatives: []KeyCandidate{}}
	if p.Rate <= 0 {
		return res
	}
	x := p.Samples
	if maxN := int(keyMaxS * float64(p.Rate)); len(x) > maxN {
		x = x[:maxN]
	}
	res.AnalyzedS = round(float64(len(x))/float64(p.Rate), 2)
	if res.AnalyzedS < keyMinS || !KeyEnabled {
		return res
	}
	frames := keyPeaks(sanitize(x), p.Rate)
	tuning, cents := keyTuning(frames)
	res.TuningCents = round(cents, 1)

	var chroma [12]float64
	for _, fr := range frames {
		var v [12]float64
		var n2 float64
		for _, pk := range fr {
			wt := pk.w
			for h := 1; h <= keyHarmonics; h++ {
				pc := ((int(math.Round(pk.semis-12*math.Log2(float64(h))-tuning)) % 12) + 12 + 9) % 12
				v[pc] += wt
				wt *= keyHarmDecay
			}
		}
		for _, a := range v {
			n2 += a * a
		}
		if n2 == 0 {
			continue
		}
		n := math.Sqrt(n2)
		for i := range chroma {
			chroma[i] += v[i] / n
		}
	}
	cands := make([]keyCand, 0, 24)
	for t := range 12 {
		cands = append(cands, keyCand{t, false, rotatedPearson(chroma, ksMajor, t)}, keyCand{t, true, rotatedPearson(chroma, ksMinor, t)})
	}
	slices.SortStableFunc(cands, func(a, b keyCand) int {
		switch {
		case a.r > b.r:
			return -1
		case a.r < b.r:
			return 1
		}
		return 0
	})
	best, second := cands[0], cands[1]
	res.R, res.Margin = round(best.r, 3), round(best.r-second.r, 3)
	for _, c := range cands[1:4] {
		res.Alternatives = append(res.Alternatives, KeyCandidate{Key: KeyName(c.tonic, c.minor), R: round(c.r, 3)})
	}
	if best.r < keyMinR || best.r-second.r < keyMinMargin {
		return res
	}
	name, cam, tonic, mode := KeyName(best.tonic, best.minor), KeyCamelot(best.tonic, best.minor), best.tonic, "major"
	if best.minor {
		mode = "minor"
	}
	res.Key, res.Camelot, res.Tonic, res.Mode = &name, &cam, &tonic, &mode
	// the relative key of a minor tonic t is the major tonic t+3 (and the other way round)
	if best.minor {
		res.AmbiguousRelative = !second.minor && second.tonic == (best.tonic+3)%12 && best.r-second.r < keyAmbigRel
	} else {
		res.AmbiguousRelative = second.minor && second.tonic == (best.tonic+9)%12 && best.r-second.r < keyAmbigRel
	}
	return res
}

// rotatedPearson correlates chroma (C = 0) with a profile whose tonic is pitch class t.
func rotatedPearson(chroma, prof [12]float64, t int) float64 {
	var mc, mp float64
	for i := range 12 {
		mc += chroma[i]
		mp += prof[i]
	}
	mc, mp = mc/12, mp/12
	var sxy, sxx, syy float64
	for i := range 12 {
		a, b := chroma[(t+i)%12]-mc, prof[i]-mp
		sxy += a * b
		sxx += a * a
		syy += b * b
	}
	if sxx <= 1e-12 || syy <= 0 {
		return 0
	}
	return sxy / math.Sqrt(sxx*syy)
}

// keyPeaks is the STFT of the key analyzer reduced to its strongest spectral peaks per frame (steps 1 and 2).
func keyPeaks(x []float32, sr int) [][]keyPeak {
	if len(x) < keyWindow {
		return nil
	}
	nf := (len(x)-keyWindow)/keyHop + 1
	win := Hann(keyWindow)
	lo := max(int(math.Ceil(keyLoHz*keyWindow/float64(sr))), 2)
	hi := min(int(keyHiHz*keyWindow/float64(sr)), keyWindow/2-2)
	re, im := make([]float64, keyWindow), make([]float64, keyWindow)
	width := hi - lo + 3 // bins lo-1 .. hi+1
	mag := [2][]float64{make([]float64, width), make([]float64, width)}
	out := make([][]keyPeak, nf)
	binHz := float64(sr) / keyWindow
	for f := 0; f < nf; f += 2 {
		two := f+1 < nf // two real frames per complex transform: f in the real part, f+1 in the imaginary part
		o0, o1 := f*keyHop, (f+1)*keyHop
		for i := range keyWindow {
			re[i] = float64(x[o0+i]) * win[i]
			im[i] = 0
			if two {
				im[i] = float64(x[o1+i]) * win[i]
			}
		}
		FFT(re, im)
		for k := lo - 1; k <= hi+1; k++ {
			m := keyWindow - k
			ar, ai := (re[k]+re[m])/2, (im[k]-im[m])/2
			mag[0][k-lo+1] = math.Hypot(ar, ai)
			if two {
				br, bi := (im[k]+im[m])/2, (re[m]-re[k])/2
				mag[1][k-lo+1] = math.Hypot(br, bi)
			}
		}
		out[f] = framePeaks(mag[0], lo-1, binHz)
		if two {
			out[f+1] = framePeaks(mag[1], lo-1, binHz)
		}
	}
	return out
}

// framePeaks picks the local maxima of sqrt(|X|) in m (bin index first+i), refines each by parabolic interpolation on
// the log magnitude, and keeps the strongest keyMaxPeaks at or above keyPeakRel of the strongest.
func framePeaks(m []float64, first int, binHz float64) []keyPeak {
	var top float64
	for _, v := range m[1 : len(m)-1] {
		top = max(top, v)
	}
	if top < keyFloor {
		return nil
	}
	ctop := math.Sqrt(top)
	var peaks []keyPeak
	for i := 1; i < len(m)-1; i++ {
		c := math.Sqrt(m[i])
		if c < keyPeakRel*ctop || m[i] <= m[i-1] || m[i] < m[i+1] {
			continue
		}
		a, b, d := math.Log(m[i-1]+1e-12), math.Log(m[i]+1e-12), math.Log(m[i+1]+1e-12)
		delta := 0.0
		if den := a - 2*b + d; den < 0 {
			delta = math.Max(-0.5, math.Min(0.5, 0.5*(a-d)/den))
		}
		f := (float64(first+i) + delta) * binHz
		peaks = append(peaks, keyPeak{semis: 12 * math.Log2(f/440), w: c})
	}
	if len(peaks) > keyMaxPeaks {
		slices.SortStableFunc(peaks, func(a, b keyPeak) int {
			switch {
			case a.w > b.w:
				return -1
			case a.w < b.w:
				return 1
			}
			return 0
		})
		peaks = peaks[:keyMaxPeaks]
	}
	return peaks
}

// keyTuning is step 3: the circular mean of the magnitude-weighted histogram of the peaks' distance from the nearest
// equal-tempered pitch. It returns the offset in semitones and in cents (within ±50).
func keyTuning(frames [][]keyPeak) (semis, cents float64) {
	var hist [keyTuneBins]float64
	var total float64
	for _, fr := range frames {
		for _, pk := range fr {
			frac := pk.semis - math.Round(pk.semis) // [-0.5, 0.5]
			b := int(math.Floor((frac + 0.5) * keyTuneBins))
			hist[min(max(b, 0), keyTuneBins-1)] += pk.w
			total += pk.w
		}
	}
	if total == 0 {
		return 0, 0
	}
	var sx, sy float64
	for b, w := range hist {
		ang := 2 * math.Pi * ((float64(b)+0.5)/keyTuneBins - 0.5)
		sx += w * math.Cos(ang)
		sy += w * math.Sin(ang)
	}
	if math.Hypot(sx, sy)/total < keyMinTuneR {
		return 0, 0 // no consistent tuning (noise, percussion): keep the grid at A = 440
	}
	semis = math.Atan2(sy, sx) / (2 * math.Pi)
	return semis, semis * 100
}
