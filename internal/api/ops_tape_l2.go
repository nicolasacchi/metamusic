package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// tapeWire renders a tape for the wire. Gains are dB (the service keeps centi-dB); the principal-specific parts (the signed
// link) were minted by the service for the caller's principal.
func tapeWire(t tape.Tape) Tape {
	out := Tape{ID: t.ID, RecordID: t.RecordID, RecordRevision: t.RecordRevision, State: string(t.State), Reused: t.Reused, TargetLUFS: t.TargetLUFS,
		Items: make([]TapeItem, len(t.Items)), Warnings: make([]TapeWarning, len(t.Warnings)), AcceptedWarnings: t.AcceptedWarnings,
		ContainsLossy: t.ContainsLossy, LossySources: t.LossySources, TotalMS: t.TotalMS, Bytes: t.Bytes, SHA256: t.SHA256,
		CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt}
	if t.Progress != nil {
		out.Progress = &TapeProgress{Phase: t.Progress.Phase, Item: t.Progress.Item, Items: t.Progress.Items}
	}
	if t.SpreadLU != nil {
		out.SpreadLU = &TapeSpread{Before: t.SpreadLU.Before, After: t.SpreadLU.After}
	}
	for i, it := range t.Items {
		out.Items[i] = TapeItem{SlotID: it.SlotID, TakeID: it.TakeID, Position: it.Position, GainDB: float64(it.GainCDB) / 100, Status: string(it.Status),
			PredictedLUFS: it.PredictedLUFS, Lossy: it.Lossy, Resampled: it.Resampled, Upmixed: it.Upmixed, Downmixed: it.Downmixed}
	}
	for i, w := range t.Warnings {
		out.Warnings[i] = TapeWarning{SlotID: w.SlotID, Code: w.Code}
	}
	if t.Chapters != nil {
		out.Chapters = make([]TapeChapter, len(t.Chapters))
		for i, c := range t.Chapters {
			out.Chapters[i] = TapeChapter{Index: c.Index, SlotID: c.SlotID, StartMS: c.StartMS, Samples: c.Samples}
		}
	}
	if d := t.Download; d != nil {
		out.Download = &TapeDownload{URL: d.URL, Filename: d.Filename, Bytes: d.Bytes, ExpiresAt: d.ExpiresAt, CueURL: d.CueURL}
	}
	if e := t.Error; e != nil {
		out.Error = &TapeError{Code: e.Code, Message: e.Message}
	}
	return out
}

// registerTapeL2 registers the two tape operations of C129.
func registerTapeL2(a huma.API, d Deps) {
	Register(a, withBody(created(huma.Operation{
		OperationID: "export_record_tape", Method: "POST", Path: "/api/records/{id}/tapes",
		Summary:     "Export a Record as a tape",
		Description: "Queues one lossless 24-bit FLAC with chapters, loudness-matched and gapless, for a published Record; an identical live export is returned instead (reused). Poll get_tape. Needs an Idempotency-Key.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/records/:id", Idempotency: "create", Rate: "heavy"},
		func(ctx context.Context, in *exportRecordTapeIn) (*tapeOut, error) {
			if d.Tapes == nil || d.Records == nil {
				return nil, unavailable("tape")
			}
			ex := tape.ExportInput{Revision: in.Body.Revision, TargetLUFS: in.Body.TargetLUFS, IncludeLiner: in.Body.IncludeLiner == nil || *in.Body.IncludeLiner,
				AcceptWarnings: in.Body.AcceptWarnings != nil && *in.Body.AcceptWarnings}
			t, err := d.Tapes.Export(ctx, auth.FromContext(ctx), in.ID, ex)
			if err != nil {
				return nil, m2bErr(err, taskT2)
			}
			return &tapeOut{Body: tapeWire(t)}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_tape", Method: "GET", Path: "/api/tapes/{id}",
		Summary:     "Get a tape",
		Description: "A tape's state, progress, loudness plan, chapters and, when ready, its signed download link (14 days).",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/records/:id"},
		func(ctx context.Context, in *getTapeIn) (*tapeOut, error) {
			if d.Tapes == nil {
				return nil, unavailable("tape")
			}
			t, err := d.Tapes.Get(ctx, in.ID)
			if err != nil {
				return nil, m2bErr(err, taskT2)
			}
			return &tapeOut{Body: tapeWire(t)}, nil
		})
}
