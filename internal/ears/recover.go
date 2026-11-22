package ears

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Recover style sources (draft_recoveries.style_source).
const (
	StyleCaller      = "caller"
	StylePlaceholder = "placeholder"
)

// RecoverOptions tunes RecoverInput.
type RecoverOptions struct {
	Style string // the caller's style text; empty asks for the placeholder built from Tier A
}

// RecoverEvidence is what the stored analyses measured about the reference (Tier A and the recogniser's language); a part that was
// never measured is nil.
type RecoverEvidence struct {
	TempoBPM  *float64 `json:"tempo_bpm" doc:"Estimated tempo, null when none was found"`
	Key       *string  `json:"key" doc:"Estimated key, null when none was set"`
	LufsI     *float64 `json:"lufs_i" doc:"Integrated loudness"`
	Energy    *float64 `json:"energy" doc:"0..1 energy proxy"`
	DurationS float64  `json:"duration_s"`
}

// RecoverInput is everything a recovered draft is made of, read from stored rows only (D-EARS-13): the lyric rebuilt from the
// transcript, the evidence, the style and the draft_recoveries fields. Reading it never writes and never spends.
type RecoverInput struct {
	TrackID        string
	Title          string // the reference's title, for the draft's label and request id
	Transcript     tierb.Recovered
	Evidence       RecoverEvidence
	Style          string
	StyleSource    string // caller | placeholder
	AnalysisSHA256 string // of the analysis*.json the transcript came from
}

// languageNames are the names a style string uses for the languages the lint knows (yue2.StyleLanguage); another language keeps its
// code.
var languageNames = map[string]string{"it": "italian", "en": "english", "es": "spanish", "fr": "french", "de": "german", "pt": "portuguese"}

// PlaceholderStyle is the style of a recovered draft whose caller gave none: "<language> vocals, <tempo> BPM, <key>" with every part
// that is unknown left out (C343.3).
func PlaceholderStyle(language string, ev RecoverEvidence) string {
	var parts []string
	if language != "" {
		name := languageNames[language]
		if name == "" {
			name = language
		}
		parts = append(parts, name+" vocals")
	} else {
		parts = append(parts, "vocals")
	}
	if ev.TempoBPM != nil {
		parts = append(parts, fmt.Sprintf("%.0f BPM", *ev.TempoBPM))
	}
	if ev.Key != nil && *ev.Key != "" {
		parts = append(parts, *ev.Key)
	}
	return strings.Join(parts, ", ")
}

// cleanLanguage keeps a recogniser language that is two or three lower-case ASCII letters and answers "" (unknown) for anything else, such
// as the "auto" of a document whose recogniser never settled on a language.
func cleanLanguage(l string) string {
	if len(l) < 2 || len(l) > 3 {
		return ""
	}
	for _, r := range l {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return l
}

func unavailable(state, hint string) error {
	return problem.E(http.StatusConflict, "ANALYSIS_UNAVAILABLE", "the listening check of this track holds no transcript to recover from",
		map[string]any{"analyzer": "tier_b", "state": state, "hint": hint})
}

// RecoverInput reads the stored Tier B document of a reference track and prepares a recovered draft (C343.3). library.ErrNotFound for
// an unknown track; 422 RECIPE_KNOWN for a track whose recipe is known (only a reference has a transcript worth recovering: every
// other take has its recipe); 409 ANALYSIS_UNAVAILABLE {analyzer, state, hint} when the reference has no usable listening check; 422
// TRANSCRIPT_EMPTY when the guards leave no line.
func (s *Service) RecoverInput(ctx context.Context, trackID string, o RecoverOptions) (RecoverInput, error) {
	st, err := s.load(ctx, trackID)
	if err != nil {
		return RecoverInput{}, err
	}
	if st.d.Import.Rule != library.RuleReference {
		return RecoverInput{}, problem.E(http.StatusUnprocessableEntity, "RECIPE_KNOWN", "the recipe of this track is known; only an imported reference can be recovered",
			map[string]any{"track_id": trackID})
	}
	switch st.tbState {
	case TierBOK:
	case TierBPending:
		return RecoverInput{}, unavailable(st.tbState, "an analyze job for this track is still running")
	case TierBError, TierBSkipped:
		return RecoverInput{}, unavailable(st.tbState, "the listening check of this track holds no transcript")
	default:
		return RecoverInput{}, unavailable(st.tbState, "estimate an analyze item")
	}
	art, doc, err := s.readTierBDocument(ctx, trackID)
	if err != nil {
		if errors.Is(err, ErrNoTierBDocument) || errors.Is(err, ErrBadTierBDocument) {
			return RecoverInput{}, unavailable(TierBError, "the stored document cannot be read")
		}
		return RecoverInput{}, err
	}
	raw, err := tierb.Parse(doc)
	if err != nil {
		return RecoverInput{}, unavailable(TierBError, "the stored document cannot be read")
	}
	rec, err := tierb.RecoverLyrics(raw, st.d.DurationS)
	if errors.Is(err, tierb.ErrTranscriptEmpty) {
		return RecoverInput{}, problem.E(http.StatusUnprocessableEntity, "TRANSCRIPT_EMPTY", "the recogniser heard no sung line in this reference",
			map[string]any{"track_id": trackID})
	}
	if err != nil {
		return RecoverInput{}, err
	}
	rec.Language = cleanLanguage(rec.Language)
	in := RecoverInput{TrackID: trackID, Title: st.d.Title, Transcript: rec, AnalysisSHA256: art.SHA256,
		Evidence: RecoverEvidence{DurationS: st.d.DurationS}}
	if t := st.tempo; t != nil && t.BPM != nil {
		v := *t.BPM
		in.Evidence.TempoBPM = &v
	}
	if k := st.key; k != nil && k.Key != nil {
		v := *k.Key
		in.Evidence.Key = &v
	}
	if l := st.loudness; l != nil {
		v := l.IntegratedLUFS
		in.Evidence.LufsI = &v
	}
	if e := st.energy; e != nil && e.Energy != nil {
		v := *e.Energy
		in.Evidence.Energy = &v
	}
	if style := strings.TrimSpace(o.Style); style != "" {
		in.Style, in.StyleSource = style, StyleCaller
	} else {
		in.Style, in.StyleSource = PlaceholderStyle(rec.Language, in.Evidence), StylePlaceholder
	}
	return in, nil
}
