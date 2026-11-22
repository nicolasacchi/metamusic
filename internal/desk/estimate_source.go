package desk

import (
	"context"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// CodePlanNotReady is the 409 of a plan whose score cannot be read back as recorded (C360.3).
const CodePlanNotReady = "PLAN_NOT_READY"

var _ generation.PlanSource = (*Service)(nil)

// PlansForEstimate returns the plans a render_plan estimate renders, in the order of ids (C360.4). q is the reader the estimate
// runs in. An unknown plan is 404; a plan whose score file is missing, unreadable or no longer the recorded bytes is 409
// PLAN_NOT_READY naming every such plan, so a corrupted score is never sent to the GPU.
func (s *Service) PlansForEstimate(ctx context.Context, q lineage.Querier, ids []string) ([]generation.PlanRow, error) {
	if err := s.wired(); err != nil {
		return nil, err
	}
	out := make([]generation.PlanRow, 0, len(ids))
	var notReady []string
	for _, id := range ids {
		r, err := load(ctx, q, id)
		if err != nil {
			return nil, err
		}
		score, err := s.readScore(r.scorePath)
		if err != nil || sha256Hex(score) != r.scoreSHA {
			notReady = append(notReady, id)
			continue
		}
		out = append(out, generation.PlanRow{
			ID: r.ID, Engine: "yue2", Cot: r.Cot, Source: r.Source, Title: r.Title, Seed: r.Seed,
			Request: []byte(r.requestJSON), Score: score, ScoreSHA256: r.scoreSHA, BaseTrackID: r.Base.TrackID, BaseDraftID: r.Base.DraftID,
			Truncated: r.Truncated != nil && *r.Truncated, Timed: r.SecondsPerBar != nil, ExpectedAudioS: r.ExpectedAudioS,
		})
	}
	if len(notReady) > 0 {
		return nil, problem.E(http.StatusConflict, CodePlanNotReady, "the score file of a plan is missing or has changed", map[string]any{"plan_ids": notReady})
	}
	return out, nil
}
