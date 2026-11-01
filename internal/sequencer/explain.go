package sequencer

import (
	"strconv"
	"strings"
)

// The explanation builder (C137.5): explainSteps turns an ordered set into Steps with coded reasons, ExplainText
// renders one Step. Reason params are plain integers: tempo in centi-BPM, energy in 1e-4 of the set range,
// percent in hundredths, a key as (number, 1 when the letter is B). Step.Position is 1-based and Step.EnergyRel is -1
// for an unknown energy.

// Reason codes. tempo_shift, key_diagonal, tempo_unknown and energy_unknown complete the coded set of C137.5 for the
// cost ranges and unknowns it leaves without a code.
const (
	ReasonOpener        = "opener"
	ReasonCloser        = "closer"
	ReasonArcPeak       = "arc_peak"
	ReasonTempoClose    = "tempo_close"
	ReasonTempoShift    = "tempo_shift"
	ReasonTempoOctave   = "tempo_octave"
	ReasonTempoJump     = "tempo_jump"
	ReasonTempoUnknown  = "tempo_unknown"
	ReasonKeySame       = "key_same"
	ReasonKeyRelative   = "key_relative"
	ReasonKeyAdjacent   = "key_adjacent"
	ReasonKeyBoost      = "key_boost"
	ReasonKeyDiagonal   = "key_diagonal"
	ReasonKeyClash      = "key_clash"
	ReasonKeyUnknown    = "key_unknown"
	ReasonEnergyRise    = "energy_rise"
	ReasonEnergyFall    = "energy_fall"
	ReasonEnergyHold    = "energy_hold"
	ReasonEnergyUnknown = "energy_unknown"
	ReasonNoFeatures    = "no_features"
	WarnLowCoverage     = "low_coverage"
	WarnPredictedEnergy = "predicted_energy"
	WarnHeuristic       = "heuristic"
	WarnNoFeatures      = "no_features"
	WarnSingleSong      = "single_song"
	WarnEndpointCost    = "endpoint_cost"
	WarnPendingFeatures = "pending_features"
)

const (
	missingBPM    = "bpm"
	missingK      = "key"
	missingEnergy = "energy"
)

var keyReason = map[string]string{
	"same": ReasonKeySame, "relative": ReasonKeyRelative, "adjacent": ReasonKeyAdjacent, "boost": ReasonKeyBoost,
	"diagonal": ReasonKeyDiagonal, "clash": ReasonKeyClash,
}

func keyParams(prefix string, k *Camelot, p map[string]int) {
	p[prefix+"_n"] = 0
	p[prefix+"_b"] = 0
	if keyKnown(k) {
		p[prefix+"_n"] = k.Num
		if k.Letter == 'B' {
			p[prefix+"_b"] = 1
		}
	}
}

func missingOf(s Song) []string {
	var m []string
	if !bpmKnown(s.BPMx100) {
		m = append(m, missingBPM)
	}
	if !keyKnown(s.Key) {
		m = append(m, missingK)
	}
	if !energyKnown(s.Energyx10000) {
		m = append(m, missingEnergy)
	}
	return m
}

// pctX100 is the signed tempo change in hundredths of a percent, rounded half away from zero.
func pctX100(a, b int) int {
	d := int64(b-a) * 10000
	if d >= 0 {
		return int((d + int64(a)/2) / int64(a))
	}
	return -int((-d + int64(a)/2) / int64(a))
}

// peakIndex is the index of the highest relative energy of the set (first on ties), or -1 when there is no peak
// (fewer than two known energies or all equal).
func peakIndex(rel []int) int {
	best, lo, known := -1, 0, 0
	for i, e := range rel {
		if !energyKnown(e) {
			continue
		}
		if known == 0 || e < lo {
			lo = e
		}
		known++
		if best < 0 || e > rel[best] {
			best = i
		}
	}
	if best < 0 || known < 2 || rel[best] == lo {
		return -1
	}
	return best
}

// explainSteps builds the steps of an ordered set under the strategy. Transition costs are weighted as in the
// objective, so their sum plus the shape terms is objective(order, spec).
func explainSteps(order []Song, spec Spec) []Step {
	n := len(order)
	rel := withRel(order)
	w := effWeights(spec.Weights, coverageOf(order))
	relOnly := make([]int, n)
	for i := range rel {
		relOnly[i] = rel[i].Energyx10000
	}
	peak := -1
	if spec.Strategy == StrategyArc {
		if p := peakIndex(relOnly); p >= 0 {
			if pk := (7*(n-1) + 5) / 10; p >= pk-1 && p <= pk+1 {
				peak = p
			}
		}
	}
	steps := make([]Step, n)
	var prev, p Song
	for i, s := range order {
		r := rel[i]
		st := Step{Position: i + 1, SongID: s.ID, EnergyRel: r.Energyx10000, Target: targetFor(spec.Strategy, i, n), Missing: missingOf(s)}
		if len(st.Missing) == 3 {
			st.Reasons = append(st.Reasons, Reason{Code: ReasonNoFeatures})
		}
		var pos []Reason
		feat := func() map[string]int {
			p := map[string]int{"bpm": s.BPMx100, "energy": r.Energyx10000}
			keyParams("key", s.Key, p)
			return p
		}
		if i == 0 {
			pos = append(pos, Reason{Code: ReasonOpener, Params: feat()})
		}
		if i == peak && i > 0 {
			pos = append(pos, Reason{Code: ReasonArcPeak, Params: feat()})
		}
		if i == n-1 && n > 1 {
			from := NoEnergy
			if i > 0 {
				from = p.Energyx10000
			}
			pos = append(pos, Reason{Code: ReasonCloser, Params: map[string]int{"from": from, "to": r.Energyx10000}})
		}
		if i > 0 {
			tr := &Transition{Parts: TransitionMicro(p, r)}
			tr.Cost = w.Tempo*tr.Parts.Tempo + w.Key*tr.Parts.Key + w.Energy*tr.Parts.Energy
			var tem, key, en Reason
			if bpmKnown(prev.BPMx100) && bpmKnown(s.BPMx100) {
				d := pctX100(prev.BPMx100, s.BPMx100)
				tr.TempoDeltaPctX100 = &d
				direct, oct := tempoBranches(prev.BPMx100, s.BPMx100)
				code := ReasonTempoShift
				switch c := tr.Parts.Tempo; {
				case oct < direct:
					code = ReasonTempoOctave
				case c <= 250_000:
					code = ReasonTempoClose
				case c >= 800_000:
					code = ReasonTempoJump
				}
				tem = Reason{Code: code, Params: map[string]int{"from": prev.BPMx100, "to": s.BPMx100, "pct": d}}
			} else {
				tem = Reason{Code: ReasonTempoUnknown}
			}
			rn, _ := keyFacts(prev.Key, s.Key)
			tr.KeyRelation = rn
			if rn == "unknown" {
				key = Reason{Code: ReasonKeyUnknown}
			} else {
				p := map[string]int{}
				keyParams("from", prev.Key, p)
				keyParams("to", s.Key, p)
				key = Reason{Code: keyReason[rn], Params: p}
			}
			if energyKnown(p.Energyx10000) && energyKnown(r.Energyx10000) {
				d := r.Energyx10000 - p.Energyx10000
				tr.EnergyDelta = &d
				code := ReasonEnergyHold
				if d >= 1000 {
					code = ReasonEnergyRise
				} else if d <= -1000 {
					code = ReasonEnergyFall
				}
				en = Reason{Code: code, Params: map[string]int{"delta": d, "from": p.Energyx10000, "to": r.Energyx10000}}
			} else {
				en = Reason{Code: ReasonEnergyUnknown}
			}
			st.From = tr
			pos = append(pos, tem, key, en)
		}
		st.Reasons = append(st.Reasons, pos...)
		steps[i] = st
		prev, p = s, r
	}
	return steps
}

// coverageWarnings are the warnings that follow from the songs alone (C137.5); Plan adds heuristic, endpoint_cost,
// and callers add pending_features.
func coverageWarnings(order []Song) []Warning {
	n := len(order)
	var ws []Warning
	if n == 1 {
		ws = append(ws, Warning{Code: WarnSingleSong})
	}
	predicted := 0
	var tk, kk, ek int
	for _, s := range order {
		if len(missingOf(s)) == 3 {
			ws = append(ws, Warning{Code: WarnNoFeatures, Song: s.ID})
		}
		if bpmKnown(s.BPMx100) {
			tk++
		}
		if keyKnown(s.Key) {
			kk++
		}
		if energyKnown(s.Energyx10000) {
			ek++
			if s.Bases.Energy == "predicted" {
				predicted++
			}
		}
	}
	for _, f := range []struct {
		name  string
		known int
	}{{missingBPM, tk}, {missingK, kk}, {missingEnergy, ek}} {
		if n > 0 && 100*f.known/n < 50 {
			ws = append(ws, Warning{Code: WarnLowCoverage, Feature: f.name, Known: f.known, Total: n})
		}
	}
	if predicted > 0 {
		ws = append(ws, Warning{Code: WarnPredictedEnergy, N: predicted})
	}
	return ws
}

func decimal(v, scale, digits int) string {
	// v/scale rounded half away from zero to digits decimals, unsigned.
	if v < 0 {
		v = -v
	}
	pow := 1
	for range digits {
		pow *= 10
	}
	x := (v*pow + scale/2) / scale
	if digits == 0 {
		return strconv.Itoa(x)
	}
	frac := strconv.Itoa(x % pow)
	frac = strings.Repeat("0", digits-len(frac)) + frac
	return strconv.Itoa(x/pow) + "." + frac
}

func signed(v int, s string) string {
	if v < 0 && strings.Trim(s, "0.") != "" {
		return "-" + s
	}
	return "+" + s
}

func fmtBPM(v int) string {
	s := decimal(v, 100, 1)
	return strings.TrimSuffix(s, ".0")
}

func fmtEnergy(v int) string { return decimal(v, 10000, 2) }

func fmtDelta(v int) string { return signed(v, fmtEnergy(v)) }

func fmtKey(n, b int) string {
	l := "A"
	if b == 1 {
		l = "B"
	}
	return strconv.Itoa(n) + l
}

func fmtPct(v int) string { return signed(v, decimal(v, 100, 1)) + " %" }

func (r Reason) p(k string) int { return r.Params[k] }

func findReason(rs []Reason, codes ...string) (Reason, bool) {
	for _, r := range rs {
		for _, c := range codes {
			if r.Code == c {
				return r, true
			}
		}
	}
	return Reason{}, false
}

// stateText describes a song for the opener and the peak: known parts only name themselves, unknown ones say so.
func stateText(r Reason) string {
	var parts []string
	if r.p("bpm") > 0 {
		parts = append(parts, fmtBPM(r.p("bpm"))+" BPM")
	} else {
		parts = append(parts, "tempo unknown")
	}
	if r.p("key_n") > 0 {
		parts = append(parts, "key "+fmtKey(r.p("key_n"), r.p("key_b")))
	} else {
		parts = append(parts, "key unknown")
	}
	if r.p("energy") >= 0 {
		parts = append(parts, "energy "+fmtEnergy(r.p("energy"))+" (relative)")
	} else {
		parts = append(parts, "no energy measurement")
	}
	return strings.Join(parts, ", ")
}

func peakStateText(r Reason) string {
	return strings.ReplaceAll(stateText(r), " (relative)", "")
}

var keyWording = map[string]string{
	ReasonKeySame:     "same",
	ReasonKeyRelative: "relative major/minor",
	ReasonKeyAdjacent: "neighbour on the wheel",
	ReasonKeyBoost:    "two steps apart: a deliberate lift, costlier than a neighbour",
	ReasonKeyDiagonal: "across modes, a step apart",
	ReasonKeyClash:    "far apart on the wheel",
}

// transitionText renders the tempo, key and energy reasons of a step: known facts joined by ", " (then the
// direction of the energy), unknown ones appended with "; ".
func transitionText(rs []Reason) string {
	var known, unknown []string
	suffix := ""
	for _, r := range rs {
		switch r.Code {
		case ReasonTempoClose, ReasonTempoShift, ReasonTempoOctave, ReasonTempoJump:
			note := fmtPct(r.p("pct"))
			switch r.Code {
			case ReasonTempoOctave:
				note = "octave"
			case ReasonTempoJump:
				note += ", a big jump"
			}
			known = append(known, "tempo "+fmtBPM(r.p("from"))+" → "+fmtBPM(r.p("to"))+" BPM ("+note+")")
		case ReasonTempoUnknown:
			unknown = append(unknown, "tempo unknown (neutral)")
		case ReasonKeyUnknown:
			unknown = append(unknown, "key unknown (neutral)")
		case ReasonEnergyUnknown:
			unknown = append(unknown, "no energy measurement (neutral)")
		case ReasonEnergyRise, ReasonEnergyFall, ReasonEnergyHold:
			known = append(known, "energy "+fmtDelta(r.p("delta")))
			suffix = map[string]string{ReasonEnergyRise: " — on the way up", ReasonEnergyFall: " — on the way down", ReasonEnergyHold: " — holding steady"}[r.Code]
		default:
			if w, ok := keyWording[r.Code]; ok {
				known = append(known, "key "+fmtKey(r.p("from_n"), r.p("from_b"))+" → "+fmtKey(r.p("to_n"), r.p("to_b"))+" ("+w+")")
			}
		}
	}
	text := strings.Join(known, ", ") + suffix
	if len(unknown) > 0 {
		if text != "" {
			text += "; "
		}
		text += strings.Join(unknown, "; ")
	}
	return text
}

// ExplainText renders one step deterministically (C137.5). It never contains a title or an id.
func ExplainText(s Step, strategy Strategy) string {
	head := strconv.Itoa(s.Position) + ". "
	if r, ok := findReason(s.Reasons, ReasonOpener); ok {
		word := "opens"
		if strategy != StrategySteady {
			word = "opens low"
		}
		return head + word + ": " + stateText(r)
	}
	peak, isPeak := findReason(s.Reasons, ReasonArcPeak)
	closer, isCloser := findReason(s.Reasons, ReasonCloser)
	switch {
	case isPeak && isCloser:
		return head + "peak: " + peakStateText(peak) + " (closes the set)"
	case isPeak:
		return head + "peak: " + peakStateText(peak)
	case isCloser && strategy == StrategyArc:
		if closer.p("from") >= 0 && closer.p("to") >= 0 {
			return head + "lands softly: energy " + fmtEnergy(closer.p("from")) + " → " + fmtEnergy(closer.p("to"))
		}
		return head + "lands softly (energy unknown)"
	}
	if _, ok := findReason(s.Reasons, ReasonNoFeatures); ok {
		return head + "no tempo, key or energy known (neutral)"
	}
	text := transitionText(s.Reasons)
	if isCloser {
		text = "closes: " + text
	}
	if text == "" {
		text = "no data"
	}
	return head + text
}
