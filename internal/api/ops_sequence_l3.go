package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/sequence"
	"github.com/nicolasacchi/metamusic/internal/sequencer"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// registerSequenceL3 registers suggest_sequence (C138.1): a read-only preview, never a write.
func registerSequenceL3(a huma.API, d Deps) {
	if d.Sequence != nil && d.Crates != nil && d.Songs != nil {
		d.Sequence.SetCrates(crateSongs(d))
	}
	Register(a, huma.Operation{
		OperationID: "suggest_sequence", Method: "GET", Path: "/api/sequence",
		Summary:     "Suggest a sequence",
		Description: "Previews the best order of a Record's slots, of 2 to 60 Songs, or of a Crate by tempo, key and energy (strategy arc, ramp_up or steady) with a per-step explanation. Read-only; nothing is stored.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/records/:id"},
		func(ctx context.Context, in *suggestSequenceIn) (*suggestionOut, error) {
			if d.Sequence == nil {
				return nil, unavailable("sequence")
			}
			s, err := d.Sequence.Suggest(ctx, sequence.SuggestInput{RecordID: in.RecordID, SongIDs: in.SongIDs, CrateID: in.CrateID,
				Strategy: sequencer.Strategy(in.Strategy), First: in.FirstSongID, Last: in.LastSongID})
			if err != nil {
				return nil, m2bErr(err, taskSR)
			}
			return &suggestionOut{Body: suggestionWire(s)}, nil
		})
}

// crateSongs resolves a Song Crate to its Songs through the Crates and Songs services (sequence imports neither): a manual
// Crate by membership, a smart one by its stored filter. It returns the full match count and at most limit ids.
func crateSongs(d Deps) sequence.CrateSongs {
	return func(ctx context.Context, crateID string, limit int) ([]string, int, error) {
		f, err := d.Crates.ExpandSongs(ctx, crateID, songs.ListFilter{})
		if err != nil {
			return nil, 0, err
		}
		total, err := d.Songs.Count(ctx, f)
		if err != nil {
			return nil, 0, err
		}
		var ids []string
		f.Limit = min(limit, 100)
		for len(ids) < limit {
			page, err := d.Songs.List(ctx, f)
			if err != nil {
				return nil, 0, err
			}
			for _, it := range page.Items {
				ids = append(ids, it.ID)
			}
			if page.NextCursor == nil || len(page.Items) == 0 {
				break
			}
			f.Cursor = *page.NextCursor
		}
		return ids[:min(len(ids), limit)], total, nil
	}
}

func transitionWire(t *sequencer.Transition) *SequenceTransition {
	if t == nil {
		return nil
	}
	return &SequenceTransition{Cost: t.Cost, Parts: SequenceParts{Tempo: t.Parts.Tempo, Key: t.Parts.Key, Energy: t.Parts.Energy},
		TempoDeltaPctX100: t.TempoDeltaPctX100, KeyRelation: t.KeyRelation, EnergyDelta: t.EnergyDelta}
}

func warningsWire(ws []sequencer.Warning) []SequenceWarning {
	out := make([]SequenceWarning, len(ws))
	for i, w := range ws {
		o := SequenceWarning{Code: w.Code}
		switch w.Code {
		case "low_coverage":
			known, total := w.Known, w.Total
			o.Feature, o.Known, o.Total = w.Feature, &known, &total
		case "predicted_energy":
			n := w.N
			o.N = &n
		case "no_features":
			o.Song = w.Song
		}
		out[i] = o
	}
	return out
}

// suggestionWire maps the service's preview onto the wire document (mm.sequence/1 plus titles).
func suggestionWire(s sequence.Suggestion) Suggestion {
	out := Suggestion{Def: s.Def, Strategy: s.Strategy, InputOrder: nonNilStrings(s.InputOrder), Order: nonNilStrings(s.Order),
		Steps: make([]SequenceStep, len(s.Steps)), Cost: SequenceCost{Input: s.Cost.Input, Output: s.Cost.Output, ImprovementPct: s.Cost.ImprovementPct},
		Coverage: SequenceCoverage{Tempo: s.Coverage.Tempo, Key: s.Coverage.Key, Energy: s.Coverage.Energy}, Exact: s.Exact,
		Warnings: warningsWire(s.Warnings), Digest: s.Digest, Pending: s.Pending}
	for i, v := range s.Steps {
		st := SequenceStep{Position: v.Step.Position, SongID: v.Step.SongID, Title: v.Title, TakeTitle: v.TakeTitle, EnergyRel: v.Step.EnergyRel,
			Target: v.Step.Target, From: transitionWire(v.Step.From), Reasons: make([]SequenceReason, len(v.Step.Reasons)),
			Missing: nonNilStrings(v.Step.Missing), Text: v.Text}
		for j, r := range v.Step.Reasons {
			p := r.Params
			if p == nil {
				p = map[string]int{}
			}
			st.Reasons[j] = SequenceReason{Code: r.Code, Params: p}
		}
		out.Steps[i] = st
	}
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// sequenceWire maps the latest stored run of a Record (get_record include=sequence): the stored document, whose steps
// recordview has given titles, plus the actor as the viewer may see it, the staleness and the stored costs.
func sequenceWire(v auth.Principal, s *recordview.SequenceView) (*SequenceExplanation, error) {
	var doc struct {
		Steps    []SequenceStep    `json:"steps"`
		Coverage SequenceCoverage  `json:"coverage"`
		Warnings []SequenceWarning `json:"warnings"`
		Cost     SequenceCost      `json:"cost"`
		Digest   string            `json:"digest"`
	}
	if err := json.Unmarshal(s.Explanation, &doc); err != nil {
		return nil, fmt.Errorf("api: sequence document: %w", err)
	}
	out := &SequenceExplanation{ID: s.ID, Strategy: s.Strategy, At: s.At, Actor: RefFor(v, s.Actor, s.ActorKind, s.ActorName), Stale: s.Stale,
		Exact: s.Exact, Cost: doc.Cost, Coverage: doc.Coverage, Steps: doc.Steps, Warnings: doc.Warnings, Digest: doc.Digest}
	if out.Steps == nil {
		out.Steps = []SequenceStep{}
	}
	if out.Warnings == nil {
		out.Warnings = []SequenceWarning{}
	}
	return out, nil
}
