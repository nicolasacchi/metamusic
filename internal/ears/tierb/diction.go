package tierb

import "strings"

// HeardWord is one recognised word after normalisation. A raw word that normalises to several tokens
// ("l'amore") yields several HeardWords sharing its time span; one that normalises to nothing yields none.
type HeardWord struct {
	Raw    int // index into Raw.ASR.Words
	Norm   string
	Fold   string
	StartS float64
	EndS   float64
	P      float64
}

// HeardWords returns the recognised words of raw in order (nil when there is no asr block).
func HeardWords(raw Raw) []HeardWord {
	if raw.ASR == nil {
		return nil
	}
	var out []HeardWord
	for i, w := range raw.ASR.Words {
		for _, t := range tokens(w.W) {
			out = append(out, HeardWord{Raw: i, Norm: t, Fold: Fold(t), StartS: w.Start, EndS: w.End, P: w.P})
		}
	}
	return out
}

// AlignRaw aligns the reference words with the recognised words (Align with CharSim over Fold). Pair.Hyp
// indexes HeardWords(raw).
func AlignRaw(ref []RefWord, raw Raw) []Pair {
	heard := HeardWords(raw)
	rf := make([]string, len(ref))
	for i, w := range ref {
		rf[i] = w.Fold
	}
	hf := make([]string, len(heard))
	for i, w := range heard {
		hf[i] = w.Fold
	}
	return Align(rf, hf, CharSim)
}

// MaxMangled caps DictionResult.Mangled.
const MaxMangled = 200

// WordOutcome is what happened to one reference word.
type WordOutcome struct {
	Word      int     `json:"word"`
	Fold      string  `json:"fold"`
	Op        string  `json:"op"` // M | S | D
	HeardFold string  `json:"heard_fold,omitempty"`
	StartS    float64 `json:"start_s"`
	EndS      float64 `json:"end_s"`
	Sim       float64 `json:"sim"`
}

// LineResult is the diction of one lyric line.
type LineResult struct {
	Section   int           `json:"section"`
	Line      int           `json:"line"`
	Text      string        `json:"text"`  // reference words, Norm, joined by spaces
	Heard     string        `json:"heard"` // heard words assigned to the line, Norm
	CER       float64       `json:"cer"`
	WER       float64       `json:"wer"`
	RefRunes  int           `json:"ref_runes"`
	CharEdits int           `json:"char_edits"`
	RefWords  int           `json:"ref_words"`
	WordEdits int           `json:"word_edits"` // S + D + I
	Inserted  int           `json:"inserted"`
	Timed     bool          `json:"timed"` // StartS/EndS come from heard words
	StartS    float64       `json:"start_s"`
	EndS      float64       `json:"end_s"`
	Words     []WordOutcome `json:"words"`
}

// SectionResult groups the lines of one vocal section.
type SectionResult struct {
	Section   int          `json:"section"`
	CER       float64      `json:"cer"`
	WER       float64      `json:"wer"`
	RefRunes  int          `json:"ref_runes"`
	CharEdits int          `json:"char_edits"`
	RefWords  int          `json:"ref_words"`
	WordEdits int          `json:"word_edits"`
	Lines     []LineResult `json:"lines"`
}

// Mangled is a substituted word: heard, but not as written.
type Mangled struct {
	Section   int     `json:"section"`
	Line      int     `json:"line"`
	Word      int     `json:"word"`
	RefFold   string  `json:"ref_fold"`
	HeardFold string  `json:"heard_fold"`
	Sim       float64 `json:"sim"`
	StartS    float64 `json:"start_s"`
}

// DictionResult is the diction of a whole take. CER and WER are weighted by reference length.
type DictionResult struct {
	CER       float64         `json:"cer"`
	WER       float64         `json:"wer"`
	RefRunes  int             `json:"ref_runes"`
	CharEdits int             `json:"char_edits"`
	RefWords  int             `json:"ref_words"`
	WordEdits int             `json:"word_edits"`
	Sections  []SectionResult `json:"sections"`
	Mangled   []Mangled       `json:"mangled"`
}

func ratio(edits, total int) float64 { return float64(edits) / float64(max(1, total)) }

// Diction computes CER and WER per line, section and overall from the alignment of ref with the recognised
// words. A line's hypothesis is the M and S words aligned to its words plus the insertions between the end of
// the previous line and its own last aligned word; insertions after the last line are dropped.
func Diction(ref []RefWord, raw Raw) DictionResult {
	heard := HeardWords(raw)
	pairs := AlignRaw(ref, raw)
	pos := make([]int, len(ref)) // pair index of each ref word
	for i, p := range pairs {
		if p.Ref >= 0 {
			pos[p.Ref] = i
		}
	}
	res := DictionResult{Sections: []SectionResult{}, Mangled: []Mangled{}}
	prevEnd := -1 // pair index of the previous line's last ref word
	for a := 0; a < len(ref); {
		b := a
		for b < len(ref) && ref[b].Section == ref[a].Section && ref[b].Line == ref[a].Line {
			b++
		}
		line := LineResult{Section: ref[a].Section, Line: ref[a].Line, Words: []WordOutcome{}}
		var refNorms, heardNorms []string
		for i := a; i < b; i++ {
			refNorms = append(refNorms, ref[i].Norm)
		}
		for pi := prevEnd + 1; pi <= pos[b-1]; pi++ {
			p := pairs[pi]
			if p.Hyp >= 0 {
				h := heard[p.Hyp]
				heardNorms = append(heardNorms, h.Norm)
				if !line.Timed || h.StartS < line.StartS {
					line.StartS = h.StartS
				}
				if !line.Timed || h.EndS > line.EndS {
					line.EndS = h.EndS
				}
				line.Timed = true
			}
			switch p.Op {
			case OpInsert:
				line.Inserted++
				line.WordEdits++
			case OpDelete:
				line.WordEdits++
				line.Words = append(line.Words, WordOutcome{Word: ref[p.Ref].Word, Fold: ref[p.Ref].Fold, Op: OpDelete})
			default:
				h := heard[p.Hyp]
				if p.Op == OpSub {
					line.WordEdits++
					if len(res.Mangled) < MaxMangled {
						res.Mangled = append(res.Mangled, Mangled{Section: line.Section, Line: line.Line, Word: ref[p.Ref].Word,
							RefFold: ref[p.Ref].Fold, HeardFold: h.Fold, Sim: p.Sim, StartS: h.StartS})
					}
				}
				line.Words = append(line.Words, WordOutcome{Word: ref[p.Ref].Word, Fold: ref[p.Ref].Fold, Op: p.Op,
					HeardFold: h.Fold, StartS: h.StartS, EndS: h.EndS, Sim: p.Sim})
			}
		}
		line.Text = strings.Join(refNorms, " ")
		line.Heard = strings.Join(heardNorms, " ")
		refRunes := []rune(line.Text)
		line.RefRunes = len(refRunes)
		line.CharEdits = Levenshtein(refRunes, []rune(line.Heard))
		line.RefWords = b - a
		line.CER = ratio(line.CharEdits, line.RefRunes)
		line.WER = ratio(line.WordEdits, line.RefWords)
		prevEnd = pos[b-1]

		if n := len(res.Sections); n == 0 || res.Sections[n-1].Section != line.Section {
			res.Sections = append(res.Sections, SectionResult{Section: line.Section, Lines: []LineResult{}})
		}
		sec := &res.Sections[len(res.Sections)-1]
		sec.Lines = append(sec.Lines, line)
		sec.RefRunes += line.RefRunes
		sec.CharEdits += line.CharEdits
		sec.RefWords += line.RefWords
		sec.WordEdits += line.WordEdits
		a = b
	}
	for i := range res.Sections {
		s := &res.Sections[i]
		s.CER, s.WER = ratio(s.CharEdits, s.RefRunes), ratio(s.WordEdits, s.RefWords)
		res.RefRunes += s.RefRunes
		res.CharEdits += s.CharEdits
		res.RefWords += s.RefWords
		res.WordEdits += s.WordEdits
	}
	res.CER, res.WER = ratio(res.CharEdits, res.RefRunes), ratio(res.WordEdits, res.RefWords)
	return res
}
