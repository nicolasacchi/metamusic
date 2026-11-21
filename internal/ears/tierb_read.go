package ears

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// maxTierBSections caps TierBDiction.Sections (C338.5).
const maxTierBSections = 30

// TierBDictionOverall is the diction of a whole take, weighted by reference length. The JSON path $.overall.cer of the stored
// row is frozen for the Critic Bench (C346.3).
type TierBDictionOverall struct {
	CER       float64 `json:"cer" doc:"Character error rate of the recognised words against the lyrics, 0 = perfect"`
	WER       float64 `json:"wer" doc:"Word error rate"`
	RefRunes  int     `json:"ref_runes"`
	CharEdits int     `json:"char_edits"`
	RefWords  int     `json:"ref_words"`
	WordEdits int     `json:"word_edits"`
}

// TierBSection is the diction of one vocal section.
type TierBSection struct {
	Section   int     `json:"section" doc:"Index of the lyric section"`
	Label     string  `json:"label"`
	CER       float64 `json:"cer"`
	WER       float64 `json:"wer"`
	RefWords  int     `json:"ref_words"`
	WordEdits int     `json:"word_edits"`
	LineCount int     `json:"line_count"`
}

// TierBWorstSection names the vocal section with the highest CER.
type TierBWorstSection struct {
	Label string  `json:"label"`
	CER   float64 `json:"cer"`
}

// TierBDiction is the diction block of get_track_tier_b.
type TierBDiction struct {
	Overall      TierBDictionOverall `json:"overall"`
	Sections     []TierBSection      `json:"sections" nullable:"false" doc:"At most 30, in song order"`
	WorstSection *TierBWorstSection  `json:"worst_section"`
}

// TierBMeasuredSection is one section placed in time from the recognised words.
type TierBMeasuredSection struct {
	Idx        int     `json:"idx"`
	Label      string  `json:"label"`
	StartS     float64 `json:"start_s"`
	EndS       float64 `json:"end_s"`
	Confidence float64 `json:"confidence"`
}

// TierBAdherenceSection compares one score section with what was sung.
type TierBAdherenceSection struct {
	Idx        int      `json:"idx"`
	Label      string   `json:"label"`
	Chroma     *float64 `json:"chroma"`
	ChromaBest *float64 `json:"chroma_best"`
	Transpose  *int     `json:"transpose"`
	NotesPlan  int      `json:"notes_plan"`
	NotesSung  int      `json:"notes_sung"`
}

// TierBAdherence is the coarse melody adherence of a take against its plan score (C336.4): pitch classes per section, not
// note by note; harmony is not judged.
type TierBAdherence struct {
	Basis         string                  `json:"basis" example:"coarse"`
	MelodyChroma  *float64                `json:"melody_chroma"`
	TransposeMode *int                    `json:"transpose_mode"`
	TempoRatio    *float64                `json:"tempo_ratio"`
	KeyRelation   string                  `json:"key_relation" enum:"exact,relative,different,unknown"`
	TimingSource  string                  `json:"timing_source" example:"measured"`
	Sections      []TierBAdherenceSection `json:"sections" nullable:"false"`
}

// TierB is the get_track_tier_b answer (C338.5): the listening check of a take. diction is null for an import or a take without
// lyric lines, and for a document with status error; adherence is null without a plan score and a pitch contour.
type TierB struct {
	TrackID          string                 `json:"track_id"`
	State            string                 `json:"state" enum:"ok,error,skipped"`
	Status           string                 `json:"status" enum:"ok,error"`
	Code             *string                `json:"code"`
	Language         string                 `json:"language"`
	Forced           bool                   `json:"forced"`
	Stages           tierb.Stages           `json:"stages"`
	Models           tierb.Models           `json:"models"`
	Timings          tierb.Timings          `json:"timings"`
	Resources        tierb.Resources        `json:"resources"`
	Diction          *TierBDiction          `json:"diction"`
	MeasuredSections []TierBMeasuredSection `json:"measured_sections" doc:"Null when the take has none"`
	Adherence        *TierBAdherence        `json:"adherence"`
	Flags            []Flag                 `json:"flags" nullable:"false"`
	BlindSpots       []string               `json:"blind_spots" nullable:"false" doc:"What the machine cannot judge; read before trusting the numbers"`
	Artifact         TierBArtifact          `json:"artifact"`
}

// worst returns the section with the highest CER among those with words (the first on a tie), or nil.
func worstSection(secs []storedSection) *TierBWorstSection {
	var w *TierBWorstSection
	for _, s := range secs {
		if s.RefWords > 0 && (w == nil || s.CER > w.CER) {
			w = &TierBWorstSection{Label: s.Label, CER: s.CER}
		}
	}
	return w
}

// dictionBlock is the public diction block of a stored row.
func dictionBlock(d *tierBDictionDoc) *TierBDiction {
	out := &TierBDiction{Overall: d.Overall, Sections: []TierBSection{}, WorstSection: worstSection(d.Sections)}
	for _, s := range d.Sections[:min(len(d.Sections), maxTierBSections)] {
		out.Sections = append(out.Sections, s.TierBSection)
	}
	return out
}

// measuredRows reads the track's measured sections (nil when it has none).
func (s *Service) measuredRows(ctx context.Context, trackID string) ([]TierBMeasuredSection, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT idx, label, start_s, end_s, confidence FROM sections
		WHERE track_id = ? AND source = 'measured' ORDER BY idx`, trackID)
	if err != nil {
		return nil, fmt.Errorf("ears: measured sections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []TierBMeasuredSection
	for rows.Next() {
		var m TierBMeasuredSection
		var conf sql.NullFloat64
		if err := rows.Scan(&m.Idx, &m.Label, &m.StartS, &m.EndS, &conf); err != nil {
			return nil, fmt.Errorf("ears: measured sections: %w", err)
		}
		m.Confidence = conf.Float64
		out = append(out, m)
	}
	return out, rows.Err()
}

// TierB builds the get_track_tier_b payload. A take whose state is none, pending or missing answers 409 ANALYSIS_UNAVAILABLE
// with the state in the details; an unknown track is library.ErrNotFound. It never writes.
func (s *Service) TierB(ctx context.Context, trackID string) (TierB, error) {
	st, err := s.load(ctx, trackID)
	if err != nil {
		return TierB{}, err
	}
	if st.tbRaw == nil {
		return TierB{}, problem.E(http.StatusConflict, "ANALYSIS_UNAVAILABLE", "tier_b has not been computed for this track",
			map[string]any{"analyzer": "tier_b", "state": st.tbState, "hint": "a listening check exists for takes rendered with one"})
	}
	r := st.tbRaw
	out := TierB{TrackID: trackID, State: st.tbState, Status: r.Status, Code: r.Code, Language: r.Language, Forced: r.Forced,
		Stages: r.Stages, Models: r.Models, Timings: r.Timings, Resources: r.Resources, Adherence: st.tbAdherence,
		Flags: st.flags, BlindSpots: st.blindSpots(false), Artifact: r.Artifact}
	if st.tbDiction != nil {
		out.Diction = dictionBlock(st.tbDiction)
	}
	if out.MeasuredSections, err = s.measuredRows(ctx, trackID); err != nil {
		return TierB{}, err
	}
	return out, nil
}
