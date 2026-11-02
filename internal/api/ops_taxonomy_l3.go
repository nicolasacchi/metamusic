package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

// registerTaxonomyL3 registers the three taxonomy proposal operations (C140). propose_taxonomy is an agent-face op like
// propose_pick (D-L3-38); decide_taxonomy_proposal is human judgment and has no MCP face. map_sound_term is unchanged:
// D-L3-22 is not approved, so agents keep it. decide_taxonomy_proposal lives on /api/ with a runtime ActsAsHuman check (D-L3-21, the pin_pick precedent).
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
			viewer := auth.FromContext(ctx)
			ms := make([]sounds.Mapping, len(in.Body.Mappings))
			for i, m := range in.Body.Mappings {
				ms[i] = sounds.Mapping{Term: m.Term, Facet: m.Facet, Value: m.Value, Op: m.Op}
			}
			pr, err := d.Sounds.Propose(ctx, viewer, in.Body.Rationale, ms)
			if err != nil {
				return nil, m2bErr(err, taskTP2)
			}
			out := &taxonomyProposalOut{Status: 201, Body: taxonomyProposalView(viewer, pr)}
			if pr.Reused {
				out.Status = 200
			} else {
				audit.NoteCreated(ctx, pr.ID)
			}
			return out, nil
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
			viewer := auth.FromContext(ctx)
			page, err := d.Sounds.ListProposals(ctx, sounds.ProposalFilter{States: in.State, IncludeImpact: in.IncludeImpact, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, taskTP2)
			}
			out := &taxonomyProposalsOut{}
			out.Body.Items = make([]TaxonomyProposal, 0, len(page.Items))
			for _, pr := range page.Items {
				out.Body.Items = append(out.Body.Items, taxonomyProposalView(viewer, pr))
			}
			out.Body.NextCursor, out.Body.Total = page.NextCursor, page.Total
			return out, nil
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
			viewer := auth.FromContext(ctx)
			dec := sounds.Decision{Decision: in.Body.Decision, AcceptMappings: in.Body.AcceptMappings}
			if in.Body.Note != nil {
				dec.Note = *in.Body.Note
			}
			res, err := d.Sounds.Decide(ctx, viewer, in.ID, dec)
			if errors.Is(err, sounds.ErrTaxonomyStale) {
				// the expiry is committed; the body names only ids
				return nil, Err(409, "TAXONOMY_STALE", "every accepted mapping is no longer applicable; the proposal expired",
					map[string]any{"proposal_id": in.ID, "skipped": taxonomySkipped(res.Skipped)})
			}
			if err != nil {
				return nil, m2bErr(err, taskTP2)
			}
			out := &decideResultOut{}
			out.Body = DecideResult{Proposal: taxonomyProposalView(viewer, res.Proposal), Skipped: taxonomySkipped(res.Skipped)}
			if res.Applied != nil {
				out.Body.Applied = &MapResult{Version: res.Applied.Version, MappingsApplied: res.Applied.MappingsApplied, Unchanged: res.Applied.Unchanged,
					Recomputed: MapRecomputed{Takes: res.Applied.RecomputedTakes, Songs: res.Applied.RecomputedSongs}}
			}
			return out, nil
		})
}

func taxonomySkipped(in []sounds.Skipped) []TaxonomySkipped {
	out := make([]TaxonomySkipped, len(in))
	for i, s := range in {
		out[i] = TaxonomySkipped{Index: s.Index, Reason: s.Reason}
	}
	return out
}

func facetCounts(in []sounds.FacetCount) []ProposalFacetCount {
	out := make([]ProposalFacetCount, len(in))
	for i, c := range in {
		out[i] = ProposalFacetCount{Facet: c.Facet, Value: c.Value, Songs: c.Songs}
	}
	return out
}

// taxonomyProposalView maps a stored proposal to the wire shape for viewer; principals go through RefFor.
func taxonomyProposalView(viewer auth.Principal, p sounds.Proposal) TaxonomyProposal {
	out := TaxonomyProposal{
		ID: p.ID, State: p.State, BaseVersion: p.BaseVersion, Rationale: p.Rationale, Mappings: make([]TaxonomyMapping, len(p.Mappings)),
		ProposedBy: RefFor(viewer, p.ProposedBy.ID, p.ProposedBy.Kind, p.ProposedBy.Name), CreatedAt: p.CreatedAt,
		DecidedAt: p.DecidedAt, DecidedNote: p.DecidedNote, AppliedVersion: p.AppliedVersion, Reused: p.Reused,
		Impact: TaxonomyImpact{TaxonomyVersion: p.Impact.TaxonomyVersion, SongsScanned: p.Impact.SongsScanned, Truncated: p.Impact.Truncated,
			Gain: facetCounts(p.Impact.Gain), Loss: facetCounts(p.Impact.Loss), UnmappedCleared: make([]ProposalTermCount, len(p.Impact.UnmappedCleared))},
	}
	for i, m := range p.Mappings {
		out.Mappings[i] = TaxonomyMapping{Term: m.Term, Facet: m.Facet, Value: m.Value, Op: m.Op}
	}
	for i, c := range p.Impact.UnmappedCleared {
		out.Impact.UnmappedCleared[i] = ProposalTermCount{Term: c.Term, Songs: c.Songs}
	}
	if p.DecidedBy != nil {
		kind, _, _ := strings.Cut(*p.DecidedBy, ":")
		ref := RefFor(viewer, *p.DecidedBy, kind, kind)
		out.DecidedBy = &ref
	}
	if len(p.Accepted) > 0 {
		_ = json.Unmarshal(p.Accepted, &out.Accepted)
	}
	if len(p.Skipped) > 0 {
		_ = json.Unmarshal(p.Skipped, &out.Skipped)
	}
	return out
}
