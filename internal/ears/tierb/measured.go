package tierb

import "github.com/nicolasacchi/metamusic/internal/engine/yue2"

// Measured section tuning (C335.1).
const (
	alignedSim     = 0.6  // an S pair counts as aligned from this similarity
	alignedRatio   = 0.4  // a vocal section is aligned from this share of its words
	leadS          = 0.25 // a section starts this long before its first aligned word
	tailGapS       = 0.5  // an instrumental gap starts this long after the last aligned word
	instrumentalC  = 0.5  // confidence of a section placed by gap filling
	minSectionS    = 1.0
	minAlignedFrac = 0.5 // share of vocal sections that must be aligned
)

// Measured is one section placed in time from the recogniser's word times. Raw is the lyric header as written
// ("[Verse 1]"), Lines the section's lyric lines (main's lines_json shape).
type Measured struct {
	Idx        int
	Kind       string
	Label      string
	Raw        string
	Lines      []string
	StartS     float64
	EndS       float64
	Confidence float64
}

type placed struct {
	vocal    bool
	aligned  bool
	start    float64 // anchor start (aligned vocal sections)
	wordEnd  float64 // end of the last aligned word (aligned vocal sections)
	ratio    float64
	startOut float64
}

// MeasuredSections places the lyric sections in time from the alignment pairs of AlignRaw(ref, raw) (C335.1).
// A vocal section is aligned when (M + S with sim >= 0.6) / its words >= 0.4; it starts 0.25 s before its first
// such word. Instrumental sections, and vocal sections that did not align, share the gap up to the next aligned
// section (leading: from 0; between and trailing: from 0.5 s after the last aligned word of the previous
// section; trailing: up to the audio duration), confidence 0.5 (0 for an unaligned vocal section). Each end is
// the next start, the last the duration. ok is false (the track keeps no measured sections and carries the flag
// alignment_failed) unless at least half of the vocal sections aligned, starts strictly increase and every
// section lasts at least one second.
func MeasuredSections(ref []RefWord, secs []yue2.Section, pairs []Pair, raw Raw) ([]Measured, bool) {
	heard := HeardWords(raw)
	n := len(secs)
	if n == 0 || len(heard) == 0 {
		return nil, false
	}
	idxOf := make(map[int]int, n) // yue2 section index -> position
	for i, s := range secs {
		idxOf[s.Idx] = i
	}
	words := make([]int, n)
	for _, w := range ref {
		if i, ok := idxOf[w.Section]; ok {
			words[i]++
		}
	}
	ps := make([]placed, n)
	hits := make([]int, n)
	for _, p := range pairs {
		if p.Ref < 0 || p.Hyp < 0 || (p.Op != OpMatch && !(p.Op == OpSub && p.Sim >= alignedSim)) {
			continue
		}
		i, ok := idxOf[ref[p.Ref].Section]
		if !ok {
			continue
		}
		h := heard[p.Hyp]
		if hits[i] == 0 || h.StartS < ps[i].start {
			ps[i].start = h.StartS
		}
		if hits[i] == 0 || h.EndS > ps[i].wordEnd {
			ps[i].wordEnd = h.EndS
		}
		hits[i]++
	}
	vocal, aligned := 0, 0
	for i := range ps {
		ps[i].vocal = words[i] > 0
		if !ps[i].vocal {
			continue
		}
		vocal++
		ps[i].ratio = float64(hits[i]) / float64(words[i])
		if ps[i].ratio >= alignedRatio {
			ps[i].aligned = true
			ps[i].start = max(0, ps[i].start-leadS)
			aligned++
		}
	}
	if vocal == 0 || float64(aligned) < minAlignedFrac*float64(vocal) {
		return nil, false
	}
	duration := 0.0
	if raw.Input != nil {
		duration = raw.Input.DurationS
	}
	if last := heard[len(heard)-1].EndS; duration < last {
		duration = last + tailGapS
	}

	// Anchors keep their starts; every run of unanchored sections shares the gap before the next anchor.
	for i := 0; i < n; {
		if ps[i].aligned {
			ps[i].startOut = ps[i].start
			i++
			continue
		}
		j := i
		for j < n && !ps[j].aligned {
			j++
		}
		lo, hi := 0.0, duration
		if i > 0 {
			lo = ps[i-1].wordEnd + tailGapS
		}
		if j < n {
			hi = ps[j].start
		}
		if hi <= lo {
			return nil, false
		}
		for k := i; k < j; k++ {
			ps[k].startOut = lo + (hi-lo)*float64(k-i)/float64(j-i)
		}
		i = j
	}
	out := make([]Measured, n)
	for i, s := range secs {
		end := duration
		if i+1 < n {
			end = ps[i+1].startOut
		}
		conf := instrumentalC
		switch {
		case ps[i].aligned:
			conf = ps[i].ratio
		case ps[i].vocal:
			conf = 0
		}
		out[i] = Measured{Idx: s.Idx, Kind: s.Kind, Label: s.Label, Raw: s.Raw, Lines: s.Lines,
			StartS: ps[i].startOut, EndS: end, Confidence: conf}
		if i > 0 && ps[i].startOut <= ps[i-1].startOut {
			return nil, false
		}
		if end-ps[i].startOut < minSectionS {
			return nil, false
		}
	}
	return out, true
}
