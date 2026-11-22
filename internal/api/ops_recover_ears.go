package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/drafts"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// RecoverTranscript summarises the lyric recovered from the stored listening check.
type RecoverTranscript struct {
	Language   string                   `json:"language" doc:"Language the recogniser heard"`
	Words      int                      `json:"words"`
	Lines      int                      `json:"lines"`
	Sections   []tierb.RecoveredSection `json:"sections" nullable:"false" doc:"The sections of the recovered lyric, in order"`
	Confidence float64                  `json:"confidence" doc:"Duration-weighted mean word probability, 0..1; low means the recogniser was unsure"`
}

// RecoverResult is the recover_recipe answer (C343.2).
type RecoverResult struct {
	Draft       library.DraftDetail  `json:"draft"`
	Transcript  RecoverTranscript    `json:"transcript"`
	Evidence    ears.RecoverEvidence `json:"evidence"`
	StyleSource string               `json:"style_source" enum:"caller,placeholder" doc:"placeholder: the style was built from the measurements"`
}

type recoverRecipeIn struct {
	IdemHeader
	ID   string `path:"id" doc:"Track id of the imported reference"`
	Body struct {
		Style     string `json:"style,omitempty" minLength:"1" maxLength:"4000" doc:"Style text of the new draft; omitted: a placeholder from the measured tempo and key"`
		Cot       string `json:"cot,omitempty" enum:"melody,full,off" default:"melody"`
		Title     string `json:"title,omitempty" minLength:"1" maxLength:"200" doc:"Label of the draft; cut to 40 characters"`
		Rationale string `json:"rationale,omitempty" minLength:"1" maxLength:"1000"`
	}
}

type recoverRecipeOut struct{ Body RecoverResult }

// registerRecoverEars registers Recover (M5b C343.2): a new draft from the stored listening check of an imported reference. It spends
// nothing and never touches the reference; rendering the draft is an ordinary estimate, approval and create (D-EARS-13).
func registerRecoverEars(a huma.API, d Deps) {
	Register(a, withBody(created(huma.Operation{
		OperationID: "recover_recipe", Method: "POST", Path: "/api/tracks/{id}/recover",
		Summary: "Recover a draft from an imported reference",
		Description: "Turns the stored listening check of an imported reference (a track whose recipe is unknown) into a NEW draft: the lyric is " +
			"rebuilt from what the recogniser heard, so it is a starting point to edit, not the singer's text; read the transcript confidence. " +
			"The style is yours, or a placeholder from the measured tempo and key. Free: nothing is rendered and the reference never changes; " +
			"render the draft with an ordinary estimate. Each call makes a new draft. 409 ANALYSIS_UNAVAILABLE until an analyze of the reference has " +
			"completed (estimate an analyze item); 422 RECIPE_KNOWN for a track whose recipe is known; 422 TRANSCRIPT_EMPTY when nothing sung was heard.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id", Idempotency: "create"},
		func(ctx context.Context, in *recoverRecipeIn) (*recoverRecipeOut, error) {
			if d.Ears == nil {
				return nil, unavailable("ears")
			}
			if d.Drafts == nil {
				return nil, unavailable("drafts")
			}
			if err := d.visible(ctx, in.ID); err != nil {
				return nil, err
			}
			ri, err := d.Ears.RecoverInput(ctx, in.ID, ears.RecoverOptions{Style: in.Body.Style})
			if err != nil {
				return nil, m2bErr(err, "EARS-R1")
			}
			p := auth.FromContext(ctx)
			tr := ri.Transcript
			dd, err := d.Drafts.CreateRecovered(ctx, p, drafts.RecoveredDraft{
				ReferenceTrackID: ri.TrackID, ReferenceTitle: ri.Title, Title: in.Body.Title, Style: ri.Style, StyleSource: ri.StyleSource,
				Lyrics: tr.Lyrics, Cot: in.Body.Cot, Rationale: in.Body.Rationale, AnalysisSHA256: ri.AnalysisSHA256,
				Language: tr.Language, Words: tr.Words, Confidence: tr.Confidence,
			})
			if err != nil {
				return nil, draftsErr(err)
			}
			maskDraftDetail(p, &dd)
			return &recoverRecipeOut{Body: RecoverResult{Draft: dd, StyleSource: ri.StyleSource, Evidence: ri.Evidence,
				Transcript: RecoverTranscript{Language: tr.Language, Words: tr.Words, Lines: tr.Lines, Sections: tr.Sections, Confidence: tr.Confidence}}}, nil
		})
}
