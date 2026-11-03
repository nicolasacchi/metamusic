// Package anchor is the musical anchor of a time point (D-M5-7): the section kind, the nth occurrence of that
// kind and the fraction through the section, so a spot of one take can be found again in another take of the
// same lineage. It is a leaf (standard library only), pure, and computed on read: nothing here is stored.
package anchor

import "math"

// Anchor bases.
const (
	BasisSection      = "section"
	BasisProportional = "proportional"
)

// ProportionalConfidence is the confidence of a point that is not tied to a section.
const ProportionalConfidence = 0.3

// Section is one timed section of a take. Ordinal is the 1-based occurrence of its Kind in the take.
type Section struct {
	Kind         string
	Ordinal      int
	Label        string
	StartS, EndS float64
}

// Timing is the best timed section list of a take. Source is "manual", "measured" or "plan"; it is empty (and
// Sections nil) when the take has no timed sections.
type Timing struct {
	Source    string
	Sections  []Section
	DurationS float64
}

// Point is a time point seen as a musical position.
type Point struct {
	AtS        float64
	Basis      string  // section | proportional
	Kind       string  // section basis only
	Ordinal    int     // section basis only
	Fraction   float64 // within the section (section basis, 1 only at the end of the last section) or of the duration
	Confidence float64
	Label      string // section basis only
	// DurFraction is AtS over the duration of the take the point was computed on (0 without a duration); it is what
	// a proportional mapping uses, also for a section point whose section the target lacks.
	DurFraction float64
}

// sourceConfidence ranks how far a timing source can be trusted.
func sourceConfidence(src string) float64 {
	switch src {
	case "manual":
		return 1
	case "measured":
		return 0.9
	case "plan":
		return 0.8
	}
	return ProportionalConfidence
}

func clamp01(x float64) float64 {
	if math.IsNaN(x) {
		return 0
	}
	return math.Max(0, math.Min(1, x))
}

func durFraction(atS, dur float64) float64 {
	if dur > 0 {
		return clamp01(atS / dur)
	}
	return 0
}

func proportional(atS, dur float64) Point {
	f := durFraction(atS, dur)
	return Point{AtS: atS, Basis: BasisProportional, Fraction: f, DurFraction: f, Confidence: ProportionalConfidence}
}

// Compute anchors atS in t. A time inside a section ([start, end), the end of the last section included)
// anchors to that section; a time in a gap, past the sections or in an untimed take anchors proportionally
// (confidence 0.3). Negative times count as 0.
func Compute(t Timing, atS float64) Point {
	if atS < 0 || math.IsNaN(atS) {
		atS = 0
	}
	for i, s := range t.Sections {
		last := i == len(t.Sections)-1
		if s.EndS <= s.StartS || atS < s.StartS || atS > s.EndS || (atS == s.EndS && !last) {
			continue
		}
		return Point{AtS: atS, Basis: BasisSection, Kind: s.Kind, Ordinal: s.Ordinal, Label: s.Label,
			Fraction: clamp01((atS - s.StartS) / (s.EndS - s.StartS)), DurFraction: durFraction(atS, t.DurationS), Confidence: sourceConfidence(t.Source)}
	}
	return proportional(atS, t.DurationS)
}

// Map finds the spot of p in another take: the same kind and ordinal at the same fraction when `to` has that
// section, else the same fraction of the duration (DurFraction) of `to` (proportional, confidence 0.3). The confidence of a
// section mapping is the lower of the two sources'.
func Map(p Point, to Timing) (atS float64, basis string, conf float64) {
	if p.Basis == BasisSection {
		for _, s := range to.Sections {
			if s.Kind == p.Kind && s.Ordinal == p.Ordinal && s.EndS > s.StartS {
				return s.StartS + p.Fraction*(s.EndS-s.StartS), BasisSection, math.Min(p.Confidence, sourceConfidence(to.Source))
			}
		}
	}
	return p.DurFraction * to.DurationS, BasisProportional, ProportionalConfidence
}
