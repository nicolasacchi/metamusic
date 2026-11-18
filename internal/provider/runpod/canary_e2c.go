package runpod

import (
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
)

// The cover case (post-E2 follow-up, docs/plans/2026-11-18-ace-cover.md): a whole-take restyle of the case's own source at
// two strengths. It is the only way to verify the cover capability; the operator flips features.verified.json in a separate PR.

// CaseCover renders a restyle of a source take at strength 0.5 and 1.0.
const CaseCover CanaryCase = "cover"

const coverCaption = "instrumental, lo-fi jazz trio, brushed drums, upright bass, 90 BPM"

func coverStage(label string, strength json.Number) aceStage {
	return aceStage{label: label, audioS: srcAudioS, class: acestep.ClassTurbo, extraLoadS: 5, want: srcMS,
		item: func(s *aceState, _ string) map[string]any {
			return map[string]any{"mode": "cover", "engine": EngineACEStep, "parent_track_id": s.src,
				"patch": map[string]any{"style": coverCaption}, "edit": map[string]any{"strength": strength}, "seeds": []int64{1}}
		}}
}

func aceCasesC() []aceCaseDef {
	return []aceCaseDef{
		{name: CaseCover, part: "b",
			stages: func(CanaryOptions) []aceStage {
				return []aceStage{sourceStage("source", srcAudioS), coverStage("cover", "0.5"), coverStage("cover-full", "1")}
			},
			finalize: func(c *canary, s *aceState, meas map[string]any) {
				// recorded, not gated: with non-reproducible renders two equal-looking takes prove nothing either way
				if same, known := sameResults(s, "cover", "cover-full"); known {
					meas["cover_strength_moves_output"] = !same
				}
			},
			proposal: func(o CanaryOptions, s *aceState, rep *CanaryReport, p *ProposedPatch) {
				p.TasksAdd = append(p.TasksAdd, acestep.TaskCover)
				proposeRTF(p, s.by["cover"], acestep.ClassTurbo, rep.RequestID)
				p.Notes = append(p.Notes, "cover strength semantics and the quality of a cross-engine restyle need a listening review")
			}},
	}
}
