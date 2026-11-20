// Package tierbtest builds deterministic fake mm.ears-b/1 documents for tests (C349.4): words, segments and a
// pitch contour laid out from a lyric text, with options to substitute or drop words, drop whole sections,
// change the language or fail the document. No randomness, no clock: the same input gives the same bytes.
package tierbtest

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// Layout constants of the synthetic performance, in seconds.
const (
	WordS          = 0.40
	WordGapS       = 0.05
	LineGapS       = 0.80
	SectionGapS    = 3.00
	InstrumentalS  = 8.00
	DefaultStartS  = 10.0
	TailS          = 12.0
	defaultLang    = "it"
	wordProb       = 0.90
	substitutedP   = 0.60
	pitchConfident = 90
)

// Opts tunes FromLyrics. The zero value is a faithful Italian recognition without a pitch contour.
type Opts struct {
	Lang         string            // ASR language, default "it"; "auto" means not forced
	StartS       float64           // start of the first sung word; 0 means the default 10 (use a small value such as 0.01 for "at once")
	DurationS    float64           // audio duration; 0 = end of the last word + 12 s
	Subs         map[string]string // Fold of a reference word -> what the recogniser heard instead
	Drops        map[string]bool   // Fold of a reference word the recogniser does not hear
	DropSections map[int]bool      // yue2 section indexes whose words are all unheard
	Transcribe   bool              // include the pitch stage and contour
	Status       string            // "" or "ok"; "error" writes a failed document
	Code         string            // error code when Status is "error", default ASR_FAILED
}

// start resolves the start of the first sung word; 0 means DefaultStartS, in layout and in FromLyrics alike.
func (o Opts) start() float64 {
	if o.StartS == 0 {
		return DefaultStartS
	}
	return o.StartS
}

// Span is where the synthetic performance puts a section (ground truth for tests). Vocal sections start at their
// first word; instrumental ones occupy a fixed interval.
type Span struct {
	Idx    int
	Vocal  bool
	StartS float64
	EndS   float64 // end of the last word, or of the instrumental interval
}

type wordAt struct {
	ref        tierb.RefWord
	start, end float64
}

func layout(lyrics string, o Opts) ([]Span, []wordAt) {
	secs := yue2.LyricsSections(lyrics)
	ref := tierb.RefWords(lyrics)
	byLine := map[[2]int][]tierb.RefWord{}
	for _, w := range ref {
		k := [2]int{w.Section, w.Line}
		byLine[k] = append(byLine[k], w)
	}
	t := o.start()
	var spans []Span
	var words []wordAt
	for i, s := range secs {
		if len(s.Lines) == 0 {
			spans = append(spans, Span{Idx: s.Idx, StartS: t, EndS: t + InstrumentalS})
			t += InstrumentalS
			continue
		}
		sp := Span{Idx: s.Idx, Vocal: true, StartS: t}
		for li := range s.Lines {
			for _, w := range byLine[[2]int{s.Idx, li}] {
				words = append(words, wordAt{ref: w, start: t, end: t + WordS})
				sp.EndS = t + WordS
				t += WordS + WordGapS
			}
			t += LineGapS
		}
		spans = append(spans, sp)
		if i+1 < len(secs) {
			t += SectionGapS
		}
	}
	return spans, words
}

// Truth returns the synthetic placement of every section of lyrics.
func Truth(lyrics string, o Opts) []Span {
	s, _ := layout(lyrics, o)
	return s
}

func r2(x float64) float64 { return math.Round(x*100) / 100 }

// FromLyrics returns the analysis.json of a recogniser that sings lyrics back as laid out by Truth, with the
// substitutions and drops of o.
func FromLyrics(lyrics string, o Opts) []byte {
	lang := o.Lang
	if lang == "" {
		lang = defaultLang
	}
	_, placed := layout(lyrics, o)
	dur := o.DurationS
	if dur == 0 {
		end := o.start()
		if len(placed) > 0 {
			end = placed[len(placed)-1].end
		}
		dur = end + TailS
	}
	dur = r2(dur)

	words, segs := []tierb.Word{}, []tierb.Segment{}
	type lineKey struct{ s, l int }
	cur, first := lineKey{-1, -1}, 0
	flush := func() {
		if len(words) > first {
			segs = append(segs, tierb.Segment{Start: words[first].Start, End: words[len(words)-1].End,
				AvgLogprob: -0.3, NoSpeechP: 0.02, W0: first, W1: len(words)})
		}
		first = len(words)
	}
	for _, p := range placed {
		k := lineKey{p.ref.Section, p.ref.Line}
		if k != cur {
			flush()
			cur = k
		}
		if o.DropSections[p.ref.Section] || o.Drops[p.ref.Fold] {
			continue
		}
		text, prob := p.ref.Norm, wordProb
		if sub, ok := o.Subs[p.ref.Fold]; ok {
			text, prob = sub, substitutedP
		}
		words = append(words, tierb.Word{W: " " + text, Start: r2(p.start), End: r2(p.end), P: prob})
	}
	flush()

	raw := tierb.Raw{
		Contract: tierb.ContractID,
		Status:   tierb.StatusOK,
		Stages:   tierb.Stages{Separate: tierb.StageOK, ASR: tierb.StageOK, Transcribe: tierb.StageNotRequested},
		Models: tierb.Models{
			Separator: &tierb.Model{Name: "htdemucs", SHA256: strings.Repeat("a", 64)},
			ASR:       &tierb.ASRModel{Name: "faster-whisper-large-v3", SHA256: strings.Repeat("b", 64), ComputeType: "float16"},
		},
		Input:     &tierb.Input{DurationS: dur, SampleRate: 48000, Channels: 2, Codec: "flac"},
		Timings:   tierb.Timings{LoadS: 12.1, SeparateS: 9.8, ASRS: 11.2, TotalS: 33.1},
		Resources: tierb.Resources{Device: "fake-gpu", PeakVRAMMiB: 6200},
		ASR:       &tierb.ASR{Language: lang, Forced: lang != "auto", Segments: segs, Words: words},
		Vocals:    &tierb.Vocals{RMSdBFS: -21.3, ActiveS: r2(math.Max(0, float64(len(words))*WordS))},
		Errors:    []tierb.ErrorEntry{},
	}
	if o.Transcribe {
		raw.Stages.Transcribe = tierb.StageOK
		raw.Models.Pitch = &tierb.Model{Name: "torchcrepe-full", SHA256: strings.Repeat("c", 64)}
		raw.Timings.TranscribeS, raw.Timings.TotalS = 4.4, 37.5
		raw.Pitch = melody(placed, dur)
	}
	if o.Status == tierb.StatusError {
		code := o.Code
		if code == "" {
			code = tierb.CodeASRFailed
		}
		raw.Status, raw.Code = tierb.StatusError, &code
		raw.Stages.ASR, raw.Stages.Transcribe = tierb.StageError, tierb.StageSkipped
		raw.Models.ASR, raw.Models.Pitch = nil, nil
		raw.ASR, raw.Pitch, raw.Vocals = nil, nil, nil
		raw.Errors = []tierb.ErrorEntry{{Stage: "asr", Code: code, Message: "fake failure"}}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		panic(err) // only plain values are marshalled
	}
	return b
}

// scale is the synthetic melody: the C major scale from middle C, one step per word.
var scale = [...]int{60, 62, 64, 65, 67, 69, 71}

func melody(placed []wordAt, dur float64) *tierb.Pitch {
	n := int(math.Ceil(dur / tierb.PitchHopS))
	p := &tierb.Pitch{HopS: tierb.PitchHopS, Cents: make([]int, n), Conf: make([]int, n)}
	for i, w := range placed {
		note := scale[i%len(scale)]
		for f := int(math.Ceil(w.start / tierb.PitchHopS)); f < n && float64(f)*tierb.PitchHopS < w.end; f++ {
			p.Cents[f], p.Conf[f] = note*100, pitchConfident
		}
	}
	return p
}

// Script maps the sha256 of a synthetic reference file to the lyric it "sings", for fake analyze jobs.
type Script struct{ BySHA map[string]string }

// Lyrics returns the scripted lyric of sha.
func (s Script) Lyrics(sha string) (string, bool) {
	l, ok := s.BySHA[sha]
	return l, ok
}
