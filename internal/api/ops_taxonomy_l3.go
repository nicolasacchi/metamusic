package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerTaxonomyL3 registers the three taxonomy proposal operations (C140). propose_taxonomy is an agent-face op like
// propose_pick (D-L3-38); decide_taxonomy_proposal is human judgment and has no MCP face. map_sound_term is unchanged:
// D-L3-22 is not approved, so agents keep it. Lane TP2 replaces the handler bodies only.
func registerTaxonomyL3(a huma.API, d Deps) {
	Register(a, withBody(created(huma.Operation{
		OperationID: "propose_taxonomy", Method: "POST", Path: "/api/taxonomy-proposals",
		Summary:     "Propose Sound-term mappings",
		Description: "An agent proposes up to 50 term-to-facet mappings with a rationale; the server snapshots the impact and a human accepts or rejects. An identical open proposal is returned instead (reused).",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "none:agent-face (a human maps terms directly with map_sound_term)", Idempotency: "create", Rate: "heavy"},
		func(ctx context.Context, in *proposeTaxonomyIn) (*taxonomyProposalOut, error) {
			if d.Sounds == nil {
				return nil, unavailable("sounds")
			}
			return nil, m2bErr(errors.ErrUnsupported, taskTP2)
		})

	Register(a, huma.Operation{
		OperationID: "list_taxonomy_proposals", Method: "GET", Path: "/api/taxonomy-proposals",
		Summary:     "List taxonomy proposals",
		Description: "Taxonomy proposals by state with their impact snapshot, newest first; include_impact recomputes up to 10 of them now. Agents read the outcome of their proposals here.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/sounds"},
		func(ctx context.Context, in *listTaxonomyProposalsIn) (*taxonomyProposalsOut, error) {
			if d.Sounds == nil {
				return nil, unavailable("sounds")
			}
			return nil, m2bErr(errors.ErrUnsupported, taskTP2)
		})

	Register(a, withBody(huma.Operation{
		OperationID: "decide_taxonomy_proposal", Method: "POST", Path: "/api/taxonomy-proposals/{id}/decision",
		Summary:     "Decide a taxonomy proposal",
		Description: "The operator accepts (all mappings or a subset by index) or rejects an open proposal. Refused with 403 HUMAN_ONLY for agents.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "exclude:human-judgment", UI: "/sounds"},
		func(ctx context.Context, in *decideTaxonomyIn) (*decideResultOut, error) {
			if d.Sounds == nil {
				return nil, unavailable("sounds")
			}
			return nil, m2bErr(errors.ErrUnsupported, taskTP2)
		})
}
