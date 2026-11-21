package tierb

import (
	"errors"
	"math"
	"strings"
)

// ErrTranscriptEmpty is returned by RecoverLyrics when no usable sung line is left after the guards.
var ErrTranscriptEmpty = errors.New("tierb: the transcript has no usable lines")

// Recover thresholds (C342.1).
const (
	recoverMaxNoSpeech = 0.6
	recoverMinLogprob  = -1.0
	recoverMaxRepeat   = 4
	recoverMaxLineWord = 12
	recoverBlockGapS   = 4.0
	recoverGroupSim    = 0.8
	recoverIntroS      = 8.0
	recoverOutroS      = 10.0
)

// RecoveredSection is one section of a recovered lyric.
type RecoveredSection struct {
	Kind  string `json:"kind" doc:"intro, verse, chorus, bridge or outro"`
	Lines int    `json:"lines"`
}

// Recovered is a lyric rebuilt from a recogniser's transcript (C342.1). Lyrics is a yue2 lyric text: [section] headers
// followed by one line per recognised phrase. Confidence is the duration-weighted mean word probability.
type Recovered struct {
	Lyrics     string             `json:"lyrics"`
	Language   string             `json:"language"`
	Words      int                `json:"words"`
	Lines      int                `json:"lines"`
	Sections   []RecoveredSection `json:"sections" nullable:"false"`
	Confidence float64            `json:"confidence"`
}

type recLine struct {
	words      []Word
	start, end float64
	text, fold string
}

func newRecLine(ws []Word) recLine {
	// brackets would turn a heard word into a section header of the recovered lyric
	strip := strings.NewReplacer("[", "", "]", "")
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		parts = append(parts, strings.Fields(strip.Replace(w.W))...)
	}
	text := strings.Join(parts, " ")
	return recLine{words: ws, start: ws[0].Start, end: ws[len(ws)-1].End, text: text, fold: Fold(text)}
}

// splitLine splits ws at its largest internal gap until no piece has more than recoverMaxLineWord words.
func splitLine(ws []Word) [][]Word {
	if len(ws) <= recoverMaxLineWord {
		return [][]Word{ws}
	}
	best, gap := 1, -1.0
	for i := 1; i < len(ws); i++ {
		if g := ws[i].Start - ws[i-1].End; g > gap {
			best, gap = i, g
		}
	}
	return append(splitLine(ws[:best]), splitLine(ws[best:])...)
}

// recoverLines applies the segment guards: segments with no speech or a low log-probability are dropped, a phrase repeated more
// than four times in a row is cut to four (a recogniser loop), a long segment is split.
func recoverLines(raw Raw) []recLine {
	var lines []recLine
	prev, run := "", 0
	for _, sg := range raw.ASR.Segments {
		if sg.NoSpeechP > recoverMaxNoSpeech || sg.AvgLogprob < recoverMinLogprob {
			continue
		}
		if sg.W0 < 0 || sg.W0 >= sg.W1 || sg.W1 > len(raw.ASR.Words) {
			continue // a hand-built document; Parse never lets one through
		}
		ws := raw.ASR.Words[sg.W0:sg.W1]
		whole := newRecLine(ws)
		if whole.fold == "" {
			continue
		}
		if whole.fold == prev {
			run++
		} else {
			prev, run = whole.fold, 1
		}
		if run > recoverMaxRepeat {
			continue
		}
		for _, piece := range splitLine(ws) {
			if l := newRecLine(piece); l.fold != "" {
				lines = append(lines, l)
			}
		}
	}
	return lines
}

type recBlock struct {
	lines []recLine
	fold  string
	kind  string
}

func recoverBlocks(lines []recLine) []*recBlock {
	var blocks []*recBlock
	for i, l := range lines {
		if i == 0 || l.start-lines[i-1].end >= recoverBlockGapS {
			blocks = append(blocks, &recBlock{})
		}
		b := blocks[len(blocks)-1]
		b.lines = append(b.lines, l)
	}
	for _, b := range blocks {
		folds := make([]string, len(b.lines))
		for i, l := range b.lines {
			folds[i] = l.fold
		}
		b.fold = strings.Join(folds, " ")
	}
	return blocks
}

// labelBlocks names the blocks: the most repeated group of similar blocks is the chorus, a lone block between two choruses
// after the second one is the bridge, the rest are verses.
func labelBlocks(blocks []*recBlock) {
	group := make([]int, len(blocks))
	var heads []string
	var sizes []int
	for i, b := range blocks {
		group[i] = -1
		for g, h := range heads {
			if CharSim(b.fold, h) >= recoverGroupSim {
				group[i] = g
				break
			}
		}
		if group[i] < 0 {
			group[i] = len(heads)
			heads = append(heads, b.fold)
			sizes = append(sizes, 0)
		}
		sizes[group[i]]++
	}
	chorus := -1
	for g, n := range sizes {
		if n >= 2 && (chorus < 0 || n > sizes[chorus]) {
			chorus = g
		}
	}
	isChorus := func(i int) bool { return i >= 0 && i < len(blocks) && group[i] == chorus && chorus >= 0 }
	seen := 0
	for i, b := range blocks {
		switch {
		case isChorus(i):
			b.kind = "chorus"
			seen++
		case seen >= 2 && isChorus(i-1) && isChorus(i+1):
			b.kind = "bridge"
		default:
			b.kind = "verse"
		}
	}
}

// RecoverLyrics rebuilds a lyric from a Tier B transcript (C342.1): one line per segment, blocks at gaps of 4 s or more,
// repeated blocks as the chorus, an intro and an outro for long instrumental stretches. It makes no claim to be what the
// singer wrote: a recogniser mishears sung words, so the caller offers the result as a draft to edit. An unusable transcript
// (status error, no recogniser, nothing left after the guards) is ErrTranscriptEmpty.
func RecoverLyrics(raw Raw, durationS float64) (Recovered, error) {
	if raw.ASR == nil || raw.Status != StatusOK {
		return Recovered{}, ErrTranscriptEmpty
	}
	lines := recoverLines(raw)
	if len(lines) == 0 {
		return Recovered{}, ErrTranscriptEmpty
	}
	blocks := recoverBlocks(lines)
	labelBlocks(blocks)

	out := Recovered{Language: raw.ASR.Language, Sections: []RecoveredSection{}}
	var sb strings.Builder
	emit := func(kind string, ls []recLine) {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("[" + kind + "]\n")
		for _, l := range ls {
			sb.WriteString(l.text + "\n")
		}
		out.Sections = append(out.Sections, RecoveredSection{Kind: kind, Lines: len(ls)})
	}
	if lines[0].start >= recoverIntroS {
		emit("intro", nil)
	}
	var num, den float64
	for _, b := range blocks {
		emit(b.kind, b.lines)
		for _, l := range b.lines {
			out.Lines++
			for _, w := range l.words {
				d := max(w.End-w.Start, 0.001)
				num += w.P * d
				den += d
				out.Words++
			}
		}
	}
	if durationS > 0 && lines[len(lines)-1].end <= durationS-recoverOutroS {
		emit("outro", nil)
	}
	out.Lyrics = sb.String()
	if den > 0 {
		out.Confidence = math.Round(min(1, max(0, num/den))*1000) / 1000
	}
	return out, nil
}
