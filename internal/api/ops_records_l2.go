package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerRecordsL2 registers the eight Records operations of C129. STUB(L2-V2): lane V2 replaces the handler bodies and
// nothing else (never the Meta, the path, the types or the operation id).
func registerRecordsL2(a huma.API, d Deps) {
	records := func() error {
		if d.Records == nil || d.RecordView == nil {
			return unavailable("records")
		}
		return nil
	}

	Register(a, huma.Operation{
		OperationID: "list_records", Method: "GET", Path: "/api/records",
		Summary:     "List Records",
		Description: "Records (ordered listening sequences of Songs) by status, title, or a Song they hold; newest first. Hidden ones only with include_hidden.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/records"},
		func(ctx context.Context, in *listRecordsIn) (*recordsOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			return nil, notImplemented(taskV2) // STUB(L2-V2)
		})

	Register(a, huma.Operation{
		OperationID: "get_record", Method: "GET", Path: "/api/records/{id}",
		Summary:     "Get a Record",
		Description: "One Record with its slots (resolved takes, warnings), totals and cover; include adds liner notes, energy, tapes, changes or the publish preview.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/records/:id"},
		func(ctx context.Context, in *getRecordIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			return nil, notImplemented(taskV2) // STUB(L2-V2)
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_record", Method: "POST", Path: "/api/records",
		Summary:     "Create a Record",
		Description: "Creates a draft Record, optionally with Songs (at most 60) as floating slots in order. Floating slots follow each Song's current Pick. Needs an Idempotency-Key.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/records", Idempotency: "create"},
		func(ctx context.Context, in *createRecordIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			return nil, notImplemented(taskV2) // STUB(L2-V2)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_record", Method: "PATCH", Path: "/api/records/{id}",
		Summary:     "Update a Record",
		Description: "Changes title, subtitle, liner note, hides or unhides, or re-rolls the cover, under optimistic locking: send the revision you read. A published Record is edited by a human only.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/records/:id"},
		func(ctx context.Context, in *updateRecordIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			return nil, notImplemented(taskV2) // STUB(L2-V2)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "edit_record_slots", Method: "POST", Path: "/api/records/{id}/slots",
		Summary:     "Edit a Record's slots",
		Description: "Applies up to 50 slot operations in one transaction: add, remove, move, reorder, pin, unpin, set_side, pin_all, unpin_all. Send the revision you read; any error rolls everything back.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/records/:id"},
		func(ctx context.Context, in *editRecordSlotsIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			return nil, notImplemented(taskV2) // STUB(L2-V2)
		})

	Register(a, huma.Operation{
		OperationID: "get_record_changes", Method: "GET", Path: "/api/records/{id}/changes",
		Summary:     "Get a Record's changelog",
		Description: "The append-only changelog of a Record: edits, Pick-driven upgrades with their evidence, proposals and publications. Newest first unless order is asc; cursor is the last seq.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/records/:id"},
		func(ctx context.Context, in *getRecordChangesIn) (*recordChangesOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			return nil, notImplemented(taskV2) // STUB(L2-V2)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "propose_record", Method: "POST", Path: "/api/records/{id}/proposal",
		Summary:     "Propose a Record",
		Description: "Proposes a draft Record for a human to publish, with a rationale of up to 500 characters. Only a human publishes.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/records/:id"},
		func(ctx context.Context, in *proposeRecordIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			return nil, notImplemented(taskV2) // STUB(L2-V2)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "publish_record", Method: "POST", Path: "/human/api/records/{id}/publish",
		Summary:     "Publish or unpublish a Record",
		Description: "Human only. published true publishes a draft or proposed Record (Subsonic clients and tapes then see it); false sends a proposal back to draft or unpublishes. Send the revision you reviewed.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "exclude:human-only", UI: "/human/records/:id/publish", HumanOnly: true},
		func(ctx context.Context, in *publishRecordIn) (*recordViewOut, error) {
			if err := records(); err != nil {
				return nil, err
			}
			return nil, notImplemented(taskV2) // STUB(L2-V2)
		})
}
