package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerGenerationM3 registers the estimate, create, approval and lineage operations (C81). Every
// handler is a STUB(M3-B4): it checks its dependency (503 UNAVAILABLE {component}) and answers 501.
func registerGenerationM3(a huma.API, d Deps) {
	const spendNote = "Spends money: needs the confirmation_token of a fresh estimate. "

	Register(a, withBody(huma.Operation{
		OperationID: "estimate_generation", Method: "POST", Path: "/api/generations/estimate",
		Summary:     "Estimate a generation batch",
		Description: "Plans 1-20 items (new seeds, edits, drafts, templates, new songs) into at most 50 takes and prices them. Free. Returns the worst-case cost, warnings and the confirmation_token create_generation needs.",
	}, bodyEstimateGeneration), Meta{Scope: auth.ScopeGenerate, Cost: "free", MCP: "tool", UI: "/human/generate", Rate: "heavy"},
		func(ctx context.Context, in *estimateGenerationIn) (*estimateOut, error) {
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_generation", Method: "POST", Path: "/api/generations",
		Summary:     "Create a generation batch",
		Description: spendNote + "Reserves the cost ceiling and queues one job per take; above your auto-approve limits a human must approve (request_approval).",
	}), bodyConfirm), Meta{Scope: auth.ScopeGenerate, Cost: "spend", MCP: "tool", Idempotency: "create", Rate: "heavy",
		UI: "none:agent-face (the SPA confirms through create_generation_human)"},
		func(ctx context.Context, in *createGenerationIn) (*batchOut, error) {
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_generation_human", Method: "POST", Path: "/human/api/generations",
		Summary:     "Create a generation batch (human)",
		Description: spendNote + "The operator's confirmation counts as the approval: the batch starts at once.",
	}), bodyConfirm), Meta{Scope: auth.ScopeGenerate, Cost: "spend", MCP: "exclude:human-only", HumanOnly: true, Idempotency: "create", Rate: "heavy",
		UI: "/human/generate", AliasOf: "create_generation"},
		func(ctx context.Context, in *createGenerationIn) (*batchOut, error) {
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "estimate_variant", Method: "POST", Path: "/api/tracks/{id}/variants/estimate",
		Summary:     "Estimate variants of a track",
		Description: "Prices new-seed, same-seed edit, lyric edit or style edit takes of one mother track. Free; returns the confirmation_token create_variant needs. Reference tracks cannot be mothers.",
	}, bodyEstimateVariant), Meta{Scope: auth.ScopeGenerate, Cost: "free", MCP: "tool", UI: "/human/generate", Rate: "heavy"},
		func(ctx context.Context, in *estimateVariantIn) (*estimateOut, error) {
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_variant", Method: "POST", Path: "/api/tracks/{id}/variants",
		Summary:     "Create variants of a track",
		Description: spendNote + "Confirms an estimate_variant of this track; same approval rules as create_generation. Needs an Idempotency-Key.",
	}), bodyConfirm), Meta{Scope: auth.ScopeGenerate, Cost: "spend", MCP: "tool", Idempotency: "create", Rate: "heavy",
		UI: "none:agent-face (the SPA confirms through create_generation_human)"},
		func(ctx context.Context, in *createVariantIn) (*batchOut, error) {
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, huma.Operation{
		OperationID: "request_approval", Method: "POST", Path: "/api/batches/{id}/approval-request",
		Summary:     "Ask the human to approve a batch",
		Description: "Pushes one notification (at most one per 10 minutes) asking the operator to approve your pending batch. Only the batch creator may ask.",
	}, Meta{Scope: auth.ScopeGenerate, Cost: "free", MCP: "generic", UI: "none:agent-face (humans approve directly)"},
		func(ctx context.Context, in *batchIDIn) (*approvalOut, error) {
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, huma.Operation{
		OperationID: "get_approval_status", Method: "GET", Path: "/api/batches/{id}/approval",
		Summary:     "Get a batch's approval status",
		Description: "The approval state of a batch (pending, approved, rejected, expired or auto) with who decided and when it expires.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "none:agent-face (get_batch carries the approval for the SPA)"},
		func(ctx context.Context, in *batchIDIn) (*approvalOut, error) {
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "approve_batch", Method: "POST", Path: "/human/api/batches/{id}/approve",
		Summary:     "Approve a pending batch",
		Description: "The operator approves an agent's pending batch; its jobs start. Approving an approved batch changes nothing.",
	}, bodySmall), Meta{Scope: auth.ScopeGenerate, Cost: "approve", MCP: "exclude:human-only", HumanOnly: true, UI: "/human/confirm/:batch"},
		func(ctx context.Context, in *approveBatchIn) (*batchOut, error) {
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "reject_batch", Method: "POST", Path: "/human/api/batches/{id}/reject",
		Summary:     "Reject a pending batch",
		Description: "The operator rejects an agent's pending batch; the whole reservation is released.",
	}, bodySmall), Meta{Scope: auth.ScopeGenerate, Cost: "approve", MCP: "exclude:human-only", HumanOnly: true, UI: "/human/confirm/:batch"},
		func(ctx context.Context, in *rejectBatchIn) (*batchOut, error) {
			if d.Generation == nil {
				return nil, unavailable("generation")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})

	Register(a, huma.Operation{
		OperationID: "get_lineage", Method: "GET", Path: "/api/tracks/{id}/lineage",
		Summary:     "Get the lineage tree of a track",
		Description: "Every track of the tree around the given track in pre-order, with the operator, rationale, cost and ratings of each edge. Cut at limit or the page byte cap (truncated).",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/lineage/:id"},
		func(ctx context.Context, in *getLineageIn) (*lineageOut, error) {
			if d.Lineage == nil {
				return nil, unavailable("lineage")
			}
			return nil, notImplemented("M3-B4") // STUB(M3-B4)
		})
}
