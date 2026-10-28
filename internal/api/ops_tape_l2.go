package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerTapeL2 registers the two tape operations of C129. STUB(L2-T2): lane T2 replaces the handler bodies only.
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
			return nil, notImplemented(taskT2) // STUB(L2-T2)
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
			return nil, notImplemented(taskT2) // STUB(L2-T2)
		})
}
