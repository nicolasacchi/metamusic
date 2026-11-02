package analysis

import (
	"math"
)

// TempoDef identifies the tempo analyzer (C94). analyses.version = TempoDef + " " + tool token.
const TempoDef = "mm-tempo/1"

// Tempo analysis parameters (C94).
const (
	tempoWindow     = 1024
	tempoHop        = 128
	tempoLoHz       = 40.0
	tempoHiHz       = 5000.0
	tempoMeanS      = 0.4
	tempoMinBPM     = 50.0
	tempoMaxBPM     = 220.0
	tempoPriorBPM   = 110.0
	tempoPriorSigma = 0.8 // octaves
	tempoMinS       = 20.0
	tempoMaxS       = 240.0
	tempoMinConf    = 2.0
	tempoAmbiguous  = 0.8
	tempoLogGain    = 100.0
	tempoMinFlux    = 5.0 // mean onset strength below which the signal has no rhythmic energy (a sustained chord, near silence)
)

// TempoCandidate is one tempo hypothesis; Strength is relative to the strongest (1.0).
type TempoCandidate struct {
	BPM      float64 `json:"bpm"`
	Strength float64 `json:"strength"`
}

// Tempo is the mm-tempo/1 result. It is an estimate that assumes a steady tempo.
type Tempo struct {
	BPM             *float64         `json:"bpm"` // nil when no steady tempo was found
	Confidence      float64          `json:"confidence"`
	Candidates      []TempoCandidate `json:"candidates" nullable:"false"` // at most 3, strongest first
	OctaveAmbiguous bool             `json:"octave_ambiguous"`
	AnalyzedSeconds float64          `json:"analyzed_s"`
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// ComputeTempo estimates the tempo of mono samples at sampleRate (C94). The input is cut to its first 240 s;
// fewer than 20 s yields no tempo. It is a pure, deterministic function of its arguments.
func ComputeTempo(x []float32, sampleRate int) Tempo {
	res := Tempo{Candidates: []TempoCandidate{}}
	if sampleRate <= 0 {
		return res
	}
	if maxN := int(tempoMaxS * float64(sampleRate)); len(x) > maxN {
		x = x[:maxN]
	}
	res.AnalyzedSeconds = round(float64(len(x))/float64(sampleRate), 2)
	if res.AnalyzedSeconds < tempoMinS {
		return res
	}
	env := OnsetEnvelope(x, sampleRate)
	n := len(env)
	var e1, e2 float64
	for _, v := range env {
		e1 += v
		e2 += v * v
	}
	if n == 0 || e2 < 1e-12 || e1/float64(n) < tempoMinFlux {
		return res
	}
	fps := float64(sampleRate) / tempoHop
	lagMin := int(math.Floor(60 * fps / tempoMaxBPM))
	lagMax := int(math.Ceil(60 * fps / tempoMinBPM))
	// normalised autocorrelation: mean(x[i]·x[i+L]) / mean(x²)
	acf := make([]float64, lagMax+2)
	mean2 := e2 / float64(n)
	for l := max(lagMin-1, 1); l <= lagMax+1; l++ {
		var s float64
		for i := 0; i+l < n; i++ {
			s += env[i] * env[i+l]
		}
		acf[l] = s / float64(n-l) / mean2
	}
	bpmOf := func(lag float64) float64 { return 60 * fps / lag }
	sigma := tempoPriorSigma * math.Ln2
	prior := func(bpm float64) float64 {
		d := math.Log(bpm / tempoPriorBPM)
		return math.Exp(-d * d / (2 * sigma * sigma))
	}
	tg := make([]float64, lagMax+2) // weighted tempogram, indexed by lag
	var sum float64
	var cnt int
	best := 0
	for l := lagMin; l <= lagMax; l++ {
		b := bpmOf(float64(l))
		if b < tempoMinBPM || b > tempoMaxBPM {
			continue
		}
		tg[l] = acf[l] * prior(b)
		sum += tg[l]
		cnt++
		if best == 0 || tg[l] > tg[best] {
			best = l
		}
	}
	if best == 0 || cnt == 0 || sum <= 0 {
		return res
	}
	// refine on the unweighted autocorrelation around the weighted maximum
	pk := best
	for _, l := range []int{best - 1, best + 1} {
		if l >= lagMin-1 && l <= lagMax+1 && acf[l] > acf[pk] {
			pk = l
		}
	}
	lag := float64(pk)
	if pk > 1 && pk+1 < len(acf) {
		a, b, c := acf[pk-1], acf[pk], acf[pk+1]
		if den := a - 2*b + c; den < 0 {
			lag += 0.5 * (a - c) / den
		}
	}
	top := bpmOf(lag)
	res.Confidence = round(tg[best]/(sum/float64(cnt)), 2)
	// candidates: the top peak and its half / double tempo, by the strongest tempogram value within ±5 %
	strength := func(bpm float64) (float64, bool) {
		if bpm < tempoMinBPM || bpm > tempoMaxBPM {
			return 0, false
		}
		l0 := 60 * fps / bpm
		var m float64
		for l := max(int(math.Floor(l0*0.95)), lagMin); l <= min(int(math.Ceil(l0*1.05)), lagMax); l++ {
			m = math.Max(m, tg[l])
		}
		return m / tg[best], true
	}
	res.Candidates = append(res.Candidates, TempoCandidate{BPM: round(top, 2), Strength: 1})
	for _, b := range []float64{top / 2, top * 2} {
		if s, ok := strength(b); ok {
			res.Candidates = append(res.Candidates, TempoCandidate{BPM: round(b, 2), Strength: round(s, 3)})
			if s >= tempoAmbiguous {
				res.OctaveAmbiguous = true
			}
		}
	}
	sortCandidates(res.Candidates)
	if res.Confidence >= tempoMinConf {
		b := round(top, 2)
		res.BPM = &b
	}
	return res
}

// sortCandidates orders by strength descending, stable (insertion sort over at most three items).
func sortCandidates(c []TempoCandidate) {
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j].Strength > c[j-1].Strength; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
}
