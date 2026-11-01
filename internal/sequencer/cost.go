package sequencer

// The cost model of C137.2/C137.3. Every cost is an integer micro-unit in [0, M]; weights are per hundred and are
// multiplied by a coverage percent, so a weighted cost is in units of 1e-10 (CostUnitsPerWholeJ per whole unit).

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func bpmKnown(b int) bool { return b > 0 }

func energyKnown(e int) bool { return e >= 0 }

func keyKnown(k *Camelot) bool {
	return k != nil && k.Num >= 1 && k.Num <= 12 && (k.Letter == 'A' || k.Letter == 'B')
}

// tempoBranches returns the direct and the octave cost of two known tempi (centi-BPM).
func tempoBranches(a, b int) (direct, oct int64) {
	lo, hi := int64(a), int64(b)
	if lo > hi {
		lo, hi = hi, lo
	}
	dd := (hi - lo) * M / lo
	direct = minInt64(dd*M/TempoSaturateMicro, M)
	r := hi * M / lo
	do := r - 2*M
	if do < 0 {
		do = -do
	}
	do /= 2
	oct = 300_000 + minInt64(do*700_000/TempoSaturateMicro, 700_000)
	return direct, oct
}

// tempoMicro is the tempo cost of a transition: 0 at equal tempo, M at 20 % apart, 0.30 at an exact octave.
func tempoMicro(a, b int) int64 {
	if !bpmKnown(a) || !bpmKnown(b) {
		return NeutralMicro
	}
	direct, oct := tempoBranches(a, b)
	return minInt64(direct, oct)
}

// keyFacts returns the relation name of two keys and its cost.
func keyFacts(a, b *Camelot) (string, int64) {
	if !keyKnown(a) || !keyKnown(b) {
		return "unknown", NeutralMicro
	}
	dn := absInt(a.Num - b.Num)
	if dn > 6 {
		dn = 12 - dn
	}
	if a.Letter == b.Letter {
		switch dn {
		case 0:
			return "same", 0
		case 1:
			return "adjacent", 150_000
		case 2:
			return "boost", 450_000
		case 3:
			return "clash", 700_000
		}
		return "clash", M
	}
	switch dn {
	case 0:
		return "relative", 100_000
	case 1:
		return "diagonal", 500_000
	}
	return "clash", M
}

func keyMicro(a, b *Camelot) int64 {
	_, c := keyFacts(a, b)
	return c
}

// energyMicro is the energy cost of a step between two energies on the same scale (0..10000).
func energyMicro(a, b int) int64 {
	if !energyKnown(a) || !energyKnown(b) {
		return NeutralMicro
	}
	return minInt64(int64(absInt(b-a))*(M/(EnergySaturateMicro/100)), M)
}

// shapeMicro is the distance of a relative energy from the strategy's target.
func shapeMicro(e, target int) int64 {
	if !energyKnown(e) {
		return ShapeNeutralMicro
	}
	d := int64(e - target)
	return minInt64(d*d*M/25_000_000, M)
}

// Weights are per hundred. Shape weighs the set's energy coverage.
type Weights struct {
	Tempo  int64 `json:"tempo"`
	Key    int64 `json:"key"`
	Energy int64 `json:"energy"`
	Shape  int64 `json:"shape"`
}

// effWeights multiplies the weights by the coverage percent of each feature (C137.3).
func effWeights(w Weights, c Coverage) Weights {
	return Weights{Tempo: w.Tempo * int64(c.Tempo), Key: w.Key * int64(c.Key), Energy: w.Energy * int64(c.Energy), Shape: w.Shape * int64(c.Energy)}
}

// transJ is the weighted cost of one transition between songs whose energies are on one scale.
func transJ(w Weights, a, b Song) int64 {
	return w.Tempo*tempoMicro(a.BPMx100, b.BPMx100) + w.Key*keyMicro(a.Key, b.Key) + w.Energy*energyMicro(a.Energyx10000, b.Energyx10000)
}

// shapeJ is the weighted shape cost of relative energy e at position p of n.
func shapeJ(w Weights, spec Spec, e, p, n int) int64 {
	if w.Shape == 0 {
		return 0
	}
	t := targetFor(spec.Strategy, p, n)
	if t < 0 {
		return 0
	}
	return w.Shape * shapeMicro(e, t)
}

// coverageOf is the floor percent of songs that carry each feature.
func coverageOf(songs []Song) Coverage {
	n := len(songs)
	if n == 0 {
		return Coverage{}
	}
	var t, k, e int
	for _, s := range songs {
		if bpmKnown(s.BPMx100) {
			t++
		}
		if keyKnown(s.Key) {
			k++
		}
		if energyKnown(s.Energyx10000) {
			e++
		}
	}
	return Coverage{Tempo: 100 * t / n, Key: 100 * k / n, Energy: 100 * e / n}
}

// relEnergies rescales the known energies of the set to 0..10000 (NoEnergy stays NoEnergy). A set whose energies
// span less than 500 is flat at 5000.
func relEnergies(songs []Song) []int {
	lo, hi, seen := 0, 0, false
	for _, s := range songs {
		if !energyKnown(s.Energyx10000) {
			continue
		}
		if !seen || s.Energyx10000 < lo {
			lo = s.Energyx10000
		}
		if !seen || s.Energyx10000 > hi {
			hi = s.Energyx10000
		}
		seen = true
	}
	out := make([]int, len(songs))
	for i, s := range songs {
		switch {
		case !energyKnown(s.Energyx10000):
			out[i] = NoEnergy
		case hi-lo >= 500:
			out[i] = (s.Energyx10000 - lo) * 10000 / (hi - lo)
		default:
			out[i] = 5000
		}
	}
	return out
}

// withRel returns a copy of songs whose energies are the set-relative ones.
func withRel(songs []Song) []Song {
	rel := relEnergies(songs)
	out := make([]Song, len(songs))
	copy(out, songs)
	for i := range out {
		out[i].Energyx10000 = rel[i]
	}
	return out
}

// objective is J of an order (C137.3) for the strategy, in units of 1e-10: relative energies and coverage come from
// the order itself, which holds the whole set.
func objective(order []Song, spec Spec) int64 {
	n := len(order)
	w := effWeights(spec.Weights, coverageOf(order))
	rel := withRel(order)
	var j int64
	for i := range rel {
		if i+1 < n {
			j += transJ(w, rel[i], rel[i+1])
		}
		j += shapeJ(w, spec, rel[i].Energyx10000, i, n)
	}
	return j
}

// TransitionMicro is the micro-cost of one transition (C137.2): the parts that Distance weighs.
func TransitionMicro(a, b Song) Parts {
	return Parts{Tempo: tempoMicro(a.BPMx100, b.BPMx100), Key: keyMicro(a.Key, b.Key), Energy: energyMicro(a.Energyx10000, b.Energyx10000)}
}

// Distance is one transition under the steady weights at full coverage with absolute energies (C137.3), in the units of J.
func Distance(a, b Song) int64 {
	w := effWeights(steadySpec.Weights, Coverage{Tempo: 100, Key: 100, Energy: 100})
	return transJ(w, a, b)
}
