package tierb

import (
	"math"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/score/abc"
)

// Adherence labels and thresholds (C336.4).
const (
	BasisCoarse        = "coarse"
	TimingMeasured     = "measured"
	TimingPlan         = "plan_timing"
	TimingNone         = "none"
	KeyExact           = "exact"
	KeyRelative        = "relative"
	KeyDifferent       = "different"
	KeyUnknown         = "unknown"
	adherenceMinNotes  = 4 // notes needed on both sides of a section to compare it
	transposeMin       = -5
	transposeMax       = 6
	wholeNotesPerQuart = 4.0
)

// Window is where a score section sits in the audio: a measured section of the take.
type Window struct {
	Idx          int
	Label        string
	StartS, EndS float64
}

// KeyFact is Tier A's measured key: Tonic is a pitch class 0..11 (C = 0), Mode "major" or "minor".
type KeyFact struct {
	Tonic int
	Mode  string
}

// AdherenceInput is what Adherence compares. Measured are the take's measured sections in order (nil when it has none: the
// score's own timing is used); TempoBPM and Key are Tier A's, nil when absent.
type AdherenceInput struct {
	Tune     *abc.Tune
	Sung     []Note
	Measured []Window
	TempoBPM *float64
	Key      *KeyFact
}

// SectionAdherence compares one score section with what was sung. The numbers are null when either side has fewer than four
// notes.
type SectionAdherence struct {
	Idx        int
	Label      string
	Chroma     *float64
	ChromaBest *float64
	Transpose  *int
	NotesPlan  int
	NotesSung  int
}

// AdherenceResult is the coarse melody adherence of a take against its plan score: pitch classes per section, not note by note.
// Transpose is the semitone offset of the sung melody from the plan, folded into -5..+6.
type AdherenceResult struct {
	Basis         string
	MelodyChroma  *float64
	TransposeMode *int
	TempoRatio    *float64
	KeyRelation   string
	TimingSource  string
	Sections      []SectionAdherence
}

type histogram [12]float64

func (h histogram) cos(o histogram) float64 {
	var dot, a, b float64
	for i := range h {
		dot += h[i] * o[i]
		a += h[i] * h[i]
		b += o[i] * o[i]
	}
	if a == 0 || b == 0 {
		return 0
	}
	return dot / math.Sqrt(a*b)
}

// shift moves every pitch class up by k semitones.
func (h histogram) shift(k int) histogram {
	var out histogram
	for i := range h {
		out[((i+k)%12+12)%12] = h[i]
	}
	return out
}

func r3f(v float64) float64 { return math.Round(v*1000) / 1000 }

func ptr[T any](v T) *T { return &v }

// span is a score section reduced to what the comparison needs.
type span struct {
	label    string
	from, to int // bar indexes of the first voice, half-open
}

// Adherence compares the sung notes with the plan score's first voice, section by section (C336.4). Score section i maps to
// measured section i; without measured sections the score's own timing places them (TimingPlan), and an untimed score is
// listed but not compared (TimingNone). Sections with fewer than four notes on either side are listed with null numbers.
func Adherence(in AdherenceInput) AdherenceResult {
	res := AdherenceResult{Basis: BasisCoarse, KeyRelation: KeyUnknown, TimingSource: TimingNone, Sections: []SectionAdherence{}}
	t := in.Tune
	if t == nil || len(t.Voices) == 0 {
		return res
	}
	res.TempoRatio = tempoRatio(t, in.TempoBPM)
	res.KeyRelation = keyRelation(t, in.Key)

	nbars := len(t.Voices[0].Bars)
	var spans []span
	for _, s := range t.Sections {
		spans = append(spans, span{s.Label, s.StartBar, s.StartBar + s.Bars})
	}
	if len(spans) == 0 {
		spans = []span{{"score", 0, nbars}}
	}
	whole := len(t.Sections) == 0

	spb, timed := t.SecondsPerBar()
	windows := make([]*Window, len(spans))
	switch {
	case len(in.Measured) > 0:
		res.TimingSource = TimingMeasured
		for i := range spans {
			switch {
			case whole:
				w := Window{Label: "score", StartS: in.Measured[0].StartS, EndS: in.Measured[len(in.Measured)-1].EndS}
				windows[i] = &w
			default:
				// by measured index, not by position: a measured section without times is absent from the list
				for j := range in.Measured {
					if in.Measured[j].Idx == i {
						windows[i] = &in.Measured[j]
						break
					}
				}
			}
		}
	case timed:
		res.TimingSource = TimingPlan
		for i, s := range spans {
			windows[i] = &Window{Label: s.label, StartS: float64(s.from) * spb, EndS: float64(s.to) * spb}
		}
	}

	plan := t.Notes(0)
	var sumBest, sumW float64
	modes := map[int]int{}
	for i, s := range spans {
		var hp, hs histogram
		sa := SectionAdherence{Idx: i, Label: s.label}
		var wPlan float64
		for _, n := range plan {
			if n.MIDI >= 0 && n.Bar >= s.from && n.Bar < s.to {
				sa.NotesPlan++
				hp[n.MIDI%12] += n.Dur.Float()
				wPlan += n.Dur.Float()
			}
		}
		if w := windows[i]; w != nil {
			for _, n := range in.Sung {
				if mid := n.StartS + n.DurS/2; mid >= w.StartS && mid < w.EndS {
					sa.NotesSung++
					hs[((n.Pitch%12)+12)%12] += n.DurS
				}
			}
		}
		if sa.NotesPlan >= adherenceMinNotes && sa.NotesSung >= adherenceMinNotes && wPlan > 0 {
			sa.Chroma = ptr(r3f(hs.cos(hp)))
			best, bestK := -1.0, 0
			for _, k := range transposeOrder() {
				// the sung melody sits k semitones above the plan, so bring it back down by k
				if c := hs.shift(-k).cos(hp); c > best+1e-9 {
					best, bestK = c, k
				}
			}
			sa.ChromaBest, sa.Transpose = ptr(r3f(best)), ptr(bestK)
			sumBest += best * wPlan
			sumW += wPlan
			modes[bestK]++
		}
		res.Sections = append(res.Sections, sa)
	}
	if sumW > 0 {
		res.MelodyChroma = ptr(r3f(sumBest / sumW))
		bestK, bestN := 0, 0
		for _, k := range transposeOrder() {
			if modes[k] > bestN {
				bestK, bestN = k, modes[k]
			}
		}
		res.TransposeMode = ptr(bestK)
	}
	return res
}

// transposeOrder lists -5..+6 with the smallest shifts first, so a tie keeps the least transposition.
func transposeOrder() []int {
	out := []int{0}
	for d := 1; d <= transposeMax; d++ {
		out = append(out, d)
		if -d >= transposeMin {
			out = append(out, -d)
		}
	}
	return out
}

// tempoRatio is Tier A's BPM over the score's Q: expressed in quarter notes per minute; nil when either is absent.
func tempoRatio(t *abc.Tune, bpm *float64) *float64 {
	tp := t.Header.Tempo
	if bpm == nil || tp == nil || tp.BPM <= 0 || tp.Unit.IsZero() {
		return nil
	}
	q := tp.BPM * tp.Unit.Float() * wholeNotesPerQuart
	if q <= 0 || *bpm <= 0 {
		return nil
	}
	return ptr(r3f(*bpm / q))
}

// keyRelation compares the score's K: with Tier A's key: exact (same tonic and mode), relative (major and minor a minor third
// apart), different, or unknown when either side lacks a major or minor reading.
func keyRelation(t *abc.Tune, k *KeyFact) string {
	if k == nil || (k.Mode != "major" && k.Mode != "minor") {
		return KeyUnknown
	}
	pc, ok := tonicClass(t.Header.Key.Tonic)
	if !ok {
		return KeyUnknown
	}
	mode := t.Header.Key.Mode
	switch {
	case pc == k.Tonic && mode == k.Mode:
		return KeyExact
	case mode == "major" && k.Mode == "minor" && ((pc-k.Tonic)%12+12)%12 == 3:
		return KeyRelative
	case mode == "minor" && k.Mode == "major" && ((k.Tonic-pc)%12+12)%12 == 3:
		return KeyRelative
	}
	return KeyDifferent
}

// tonicClass is the pitch class of a tonic name such as "C", "F#" or "Bb".
func tonicClass(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	base, ok := map[byte]int{'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}[strings.ToUpper(s)[0]]
	if !ok {
		return 0, false
	}
	for _, c := range s[1:] {
		switch c {
		case '#':
			base++
		case 'b':
			base--
		}
	}
	return ((base % 12) + 12) % 12, true
}
