package diction

import (
	"math"
	"strings"
	"unicode"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// LineKey addresses one lyric line.
type LineKey struct{ Section, Line int }

// TimedSection is a section with a start and an end from the library's section selection (plan, measured or manual).
type TimedSection struct {
	Idx          int
	StartS, EndS float64
}

// LinesInput is everything BuildLines needs; it holds no I/O.
type LinesInput struct {
	Sections   []yue2.Section          // the lyric sections of the recipe
	Ref        []tierb.RefWord         // tierb.RefWords of the same lyrics
	Results    []tierb.LineResult      // the lines of the current tier_b_diction row; nil when the take has none
	Timeline   []TimedSection          // timed sections for the proportional basis
	Marks      map[LineKey]DictionMark // current marks of the take
	Thresholds Thresholds
}

// matchSimilar is the character similarity from which a substituted word counts as close rather than a miss (C344.2).
const matchSimilar = 0.66

func r3(v float64) float64 { return math.Floor(v*1000+0.5) / 1000 }

func ptr[T any](v T) *T { return &v }

// BuildLines is the pure core of the karaoke view (C344.2). The basis is asr when the take has a tier_b_diction row, else
// proportional when its sections are timed, else none (no lines). Human marks replace the shown class.
func BuildLines(in LinesInput) (string, []DictionLine) {
	th := in.Thresholds
	if th == (Thresholds{}) {
		th = DefaultThresholds
	}
	switch {
	case in.Results != nil:
		return BasisASR, asrLines(in, th)
	case len(in.Timeline) > 0:
		return BasisProportional, proportionalLines(in)
	}
	return BasisNone, nil
}

// classOf is the class of an ASR line from its CER.
func classOf(cer float64, th Thresholds) string {
	switch {
	case cer <= th.Clear:
		return ClassClear
	case cer <= th.Mixed:
		return ClassMixed
	}
	return ClassUnclear
}

// shown applies the human mark to the class: clear and unclear replace it, none withdraws the mark.
func shown(asr string, m DictionMark) (class, source string) {
	if m.Human != nil {
		switch *m.Human {
		case MarkClear:
			return ClassClear, SourceHuman
		case MarkUnclear:
			return ClassUnclear, SourceHuman
		}
	}
	return asr, SourceASR
}

func asrLines(in LinesInput, th Thresholds) []DictionLine {
	results := make(map[LineKey]tierb.LineResult, len(in.Results))
	for _, r := range in.Results {
		results[LineKey{r.Section, r.Line}] = r
	}
	words := map[LineKey][]tierb.RefWord{}
	for _, w := range in.Ref {
		k := LineKey{w.Section, w.Line}
		words[k] = append(words[k], w)
	}
	var out []DictionLine
	for _, sec := range in.Sections {
		start := len(out)
		var section []*DictionWord
		for li, text := range sec.Lines {
			k := LineKey{sec.Idx, li}
			line := DictionLine{SectionIdx: sec.Idx, LineIdx: li, Text: text, Mark: in.Marks[k], Words: []DictionWord{}}
			asr := ClassUnknown
			res, ok := results[k]
			if ok {
				asr = classOf(res.CER, th)
				line.CER = ptr(r3(res.CER))
				if res.Timed {
					line.StartS, line.EndS, line.Timing = ptr(res.StartS), ptr(res.EndS), ptr("heard")
				}
			}
			line.Class, line.ClassSource = shown(asr, line.Mark)
			outcome := map[int]tierb.WordOutcome{}
			for _, o := range res.Words {
				outcome[o.Word] = o
			}
			for _, w := range words[k] {
				if !ok {
					break // an unmeasured line has no word verdicts and takes no interpolated times
				}
				dw := DictionWord{I: w.Word, Text: w.Text, Match: "dropped"}
				if o, ok := outcome[w.Word]; ok {
					switch o.Op {
					case tierb.OpMatch:
						dw.Match = "exact"
					case tierb.OpSub:
						dw.Match, dw.Heard = "miss", o.HeardFold
						if o.Sim >= matchSimilar {
							dw.Match = "close"
						}
					}
					if o.Op != tierb.OpDelete {
						dw.StartS, dw.EndS, dw.Timing = ptr(o.StartS), ptr(o.EndS), ptr("heard")
					}
				}
				line.Words = append(line.Words, dw)
			}
			out = append(out, line)
		}
		for i := start; i < len(out); i++ {
			for j := range out[i].Words {
				section = append(section, &out[i].Words[j])
			}
		}
		interpolate(section)
	}
	return out
}

// interpolate gives a run of dropped words between two heard neighbours of the same section the gap between them, split by
// syllables (runs of vowels). A run at the edge of a section, or whose neighbours leave no gap, stays untimed (C344.2).
func interpolate(ws []*DictionWord) {
	for i := 0; i < len(ws); {
		if ws[i].Match != "dropped" {
			i++
			continue
		}
		j := i
		for j < len(ws) && ws[j].Match == "dropped" {
			j++
		}
		if i > 0 && j < len(ws) && ws[i-1].EndS != nil && ws[j].StartS != nil && *ws[j].StartS > *ws[i-1].EndS {
			from, to := *ws[i-1].EndS, *ws[j].StartS
			total := 0
			for _, w := range ws[i:j] {
				total += syllables(w.Text)
			}
			at, done := from, 0
			for _, w := range ws[i:j] {
				done += syllables(w.Text)
				end := from + (to-from)*float64(done)/float64(total)
				w.StartS, w.EndS, w.Timing = ptr(round2(at)), ptr(round2(end)), ptr("interpolated")
				at = end
			}
		}
		i = j
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// syllables counts runs of vowels, at least one.
func syllables(w string) int {
	n, in := 0, false
	for _, r := range strings.ToLower(w) {
		v := strings.ContainsRune("aeiouàèéìíòóùú", r) && unicode.IsLetter(r)
		if v && !in {
			n++
		}
		in = v
	}
	return max(n, 1)
}

// proportionalLines splits each timed lyric section equally among its lines. The times are estimates and no word has one.
func proportionalLines(in LinesInput) []DictionLine {
	timed := make(map[int]TimedSection, len(in.Timeline))
	for _, t := range in.Timeline {
		timed[t.Idx] = t
	}
	var out []DictionLine
	for _, sec := range in.Sections {
		for li, text := range sec.Lines {
			k := LineKey{sec.Idx, li}
			line := DictionLine{SectionIdx: sec.Idx, LineIdx: li, Text: text, Mark: in.Marks[k], Words: []DictionWord{}}
			line.Class, line.ClassSource = shown(ClassUnknown, line.Mark)
			if t, ok := timed[sec.Idx]; ok && t.EndS > t.StartS {
				n := float64(len(sec.Lines))
				step := (t.EndS - t.StartS) / n
				line.StartS = ptr(round2(t.StartS + float64(li)*step))
				line.EndS = ptr(round2(t.StartS + float64(li+1)*step))
				line.Timing = ptr("estimated")
			}
			out = append(out, line)
		}
	}
	return out
}
