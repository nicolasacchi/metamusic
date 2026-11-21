package generation

import (
	"context"
	"strings"
)

// EarsSeams are the M5b Ears seams (C340.5, C345.6). generation never imports diction or ears: the app injects implementations through
// (*Service).UseEars, a setter because the Diction Lab is built after this service. A zero EarsSeams changes nothing.
type EarsSeams struct {
	// DictionRisk returns the diction_risk warnings of a take's lyrics (the words the personal lexicon says were often misheard,
	// C345.6). It is called once per planned take; an error leaves the warnings out (an estimate never fails for a warning).
	DictionRisk func(ctx context.Context, lyrics string) ([]Issue, error)
}

// UseEars wires the Ears seams.
func (s *Service) UseEars(e EarsSeams) { s.ears = e }

// The M5b Ears seam (C339.5, D-EARS-2): the quote parameters of a listening check (Tier B). A Service on which UseEars was never
// called plans and quotes every take exactly as before (no option, no warning, no needs); internal/app wires it from the config.

// EarsOptions are the MM_EARS_* values of the quote: model load seconds, and the separation-plus-recognition and pitch-contour
// seconds per 1000 audio seconds.
type EarsOptions struct {
	LoadS, RTFMilli, TranscribeRTFMilli int
	// AnalyzeFetchS is MM_ANALYZE_FETCH_S: the fixed seconds an analyze job spends fetching the pinned master (C340.4). 0 = the default, 20.
	AnalyzeFetchS int
	// Language returns the ISO 639-1 code the lyrics are written in, "" when unknown (the engine's detector); LyricLines counts the
	// sung lines of a lyrics text, section headers excluded. generation imports no engine implementation, so internal/app passes
	// them; nil means "language unknown" and a plain count of the non-empty lines that are not a [Header].
	Language   func(lyrics string) string
	LyricLines func(lyrics string) int
}

type earsSeams struct {
	on bool
	o  EarsOptions
}

// UseEarsQuote turns the Tier B option on for this service: renders with lyric lines get a listening check by default.
func (s *Service) UseEarsQuote(o EarsOptions) { s.earsQ = earsSeams{on: true, o: o} }

func (o EarsOptions) language(lyrics string) string {
	if o.Language == nil {
		return ""
	}
	return o.Language(lyrics)
}

func (o EarsOptions) lyricLines(lyrics string) int {
	if o.LyricLines != nil {
		return o.LyricLines(lyrics)
	}
	n := 0
	for _, l := range strings.Split(lyrics, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !(strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]")) {
			n++
		}
	}
	return n
}
