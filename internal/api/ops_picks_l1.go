package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerPicksL1 registers the Pick operations (C96). The handlers are stubs until L1-B4 (STUB(L1-B4)).
// pin_pick and dismiss_pick_proposal are human judgment: scope write, applied only for principals that act as the
// operator (D-L1-11), no MCP face; agents use propose_pick.
func registerPicksL1(a huma.API, d Deps) {
	Register(a, withBody(created(huma.Operation{
		OperationID: "propose_pick", Method: "POST", Path: "/api/songs/{id}/pick-proposals",
		Summary:     "Propose a Pick",
		Description: "An agent suggests which take should be the Song's Pick, with a rationale; one open proposal per agent per Song (a new one supersedes it). Only the operator can pin a Pick.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "none:agent-face (a human pins directly with pin_pick)", Idempotency: "create"},
		func(ctx context.Context, in *proposePickIn) (*pickProposalOut, error) {
			if d.Picks == nil {
				return nil, unavailable("picks")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B4") // STUB(L1-B4)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "pin_pick", Method: "PUT", Path: "/api/songs/{id}/pin",
		Summary:     "Pin or unpin a Pick",
		Description: "The operator pins a take as the Song's Pick (optionally accepting an open proposal) or unpins it with take_id null. Refused with 403 HUMAN_ONLY for agents.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "exclude:human-judgment", UI: "/songs/:id"},
		func(ctx context.Context, in *pinPickIn) (*pinResultOut, error) {
			if d.Picks == nil {
				return nil, unavailable("picks")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B4") // STUB(L1-B4)
		})

	Register(a, huma.Operation{
		OperationID: "dismiss_pick_proposal", Method: "POST", Path: "/api/pick-proposals/{id}/dismiss",
		Summary:     "Dismiss a Pick proposal",
		Description: "The operator closes an open proposal as dismissed. Refused with 403 HUMAN_ONLY for agents.",
	}, Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "exclude:human-judgment", UI: "/songs/:id"},
		func(ctx context.Context, in *dismissProposalIn) (*pickProposalOut, error) {
			if d.Picks == nil {
				return nil, unavailable("picks")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B4") // STUB(L1-B4)
		})

	Register(a, huma.Operation{
		OperationID: "list_pick_events", Method: "GET", Path: "/api/pick-events",
		Summary:     "List Pick events",
		Description: "The append-only feed of Pick changes (initial, pin, unpin, ladder, stars, newest, emptied), ascending by an integer cursor; pass the previous page's cursor as after.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/songs/:id"},
		func(ctx context.Context, in *listPickEventsIn) (*pickEventsOut, error) {
			if d.Picks == nil {
				return nil, unavailable("picks")
			}
			return nil, m2bErr(errors.ErrUnsupported, "L1-B4") // STUB(L1-B4)
		})
}
