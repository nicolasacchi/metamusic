package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/money"
)

// registerMandatesM6N registers the four mandate operations (M6N C365.3, C369). A mandate is a standing spend approval: only the
// Owner, in the app, creates one (human-only and Owner-only); every Owner-side caller may read and revoke, because revoking only
// reduces spend. Nothing here raises a cap: the global caps stay the ceiling.
func registerMandatesM6N(a huma.API, d Deps) {
	Register(a, withBody(created(huma.Operation{
		OperationID: "create_mandate", Method: "POST", Path: "/human/api/mandates",
		Summary: "Create a spending mandate",
		Description: "Human only, Owner only. A mandate lets the night shift spend without a click: a budget (micro-USD, counted against settled plus reserved spend), " +
			"an expiry up to 31 days away, and a scope (modes, adopted engines, minimum parent stars, lineage roots, takes per lineage and per batch, the " +
			"largest batch ceiling, batches per day, listening checks). It never raises a spend cap: the global caps are checked first. One scheduled or " +
			"live mandate per holder (409 MANDATE_ACTIVE); a scope that breaks a rule is 422 MANDATE_SCOPE_INVALID {field, rule}. Needs an Idempotency-Key.",
	}), bodySmall), Meta{Scope: auth.ScopeGenerate, Cost: "approve", MCP: "exclude:human-only", HumanOnly: true, UI: "/human/mandates/new", OwnerOnly: true, Idempotency: "create"},
		func(ctx context.Context, in *createMandateIn) (*mandateOut, error) {
			if d.Mandates == nil {
				return nil, unavailable("mandates")
			}
			p := auth.FromContext(ctx)
			b := in.Body
			m, err := d.Mandates.Create(ctx, p, mandates.CreateInput{Holder: b.Holder, Title: b.Title, Budget: money.MicroUSD(b.BudgetMicroUSD),
				ValidFrom: b.ValidFrom, ValidUntil: b.ValidUntil, Scope: scopeIn(b.Scope)})
			if err != nil {
				return nil, m2bErr(err, "M6N-M1")
			}
			w, err := mandateWire(ctx, d, p, m)
			if err != nil {
				return nil, m2bErr(err, "M6N-M1")
			}
			return &mandateOut{Status: 201, Body: w}, nil
		})

	Register(a, huma.Operation{
		OperationID: "list_mandates", Method: "GET", Path: "/api/mandates",
		Summary:     "List spending mandates",
		Description: "Mandates newest first with their usage. Filter by the stored state or by effective_state (scheduled, live, ended, revoked; repeat the parameter). Owner side only.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/mandates", OwnerOnly: true},
		func(ctx context.Context, in *listMandatesIn) (*mandatePageOut, error) {
			if d.Mandates == nil {
				return nil, unavailable("mandates")
			}
			p := auth.FromContext(ctx)
			pg, err := d.Mandates.List(ctx, mandates.Filter{State: in.State, EffectiveStates: in.EffectiveState, Holder: in.Holder, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, "M6N-M1")
			}
			out := MandatePage{Items: make([]Mandate, 0, len(pg.Items)), NextCursor: pg.NextCursor}
			for _, m := range pg.Items {
				w, err := mandateWire(ctx, d, p, m)
				if err != nil {
					return nil, m2bErr(err, "M6N-M1")
				}
				out.Items = append(out.Items, w)
			}
			return &mandatePageOut{Body: out}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_mandate", Method: "GET", Path: "/api/mandates/{id}",
		Summary:     "Get a spending mandate",
		Description: "One mandate with its scope and usage: settled, reserved, used, committed (what a running batch still holds), remaining and excess, the takes per lineage. Owner side only.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/mandates/:id", OwnerOnly: true},
		func(ctx context.Context, in *getMandateIn) (*mandateOut, error) {
			if d.Mandates == nil {
				return nil, unavailable("mandates")
			}
			m, err := d.Mandates.Get(ctx, in.ID)
			if err != nil {
				return nil, m2bErr(err, "M6N-M1")
			}
			w, err := mandateWire(ctx, d, auth.FromContext(ctx), m)
			if err != nil {
				return nil, m2bErr(err, "M6N-M1")
			}
			return &mandateOut{Status: 200, Body: w}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "revoke_mandate", Method: "POST", Path: "/api/mandates/{id}/revoke",
		Summary: "Revoke a spending mandate",
		Description: "Ends a scheduled or live mandate and records a cancel intent on every unfinished job of its batches, in one transaction. It only reduces spend, so " +
			"an agent and the Owner's paired phone may do it. A revoked or ended mandate is 409 INVALID_TRANSITION.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/mandates/:id", OwnerOnly: true},
		func(ctx context.Context, in *revokeMandateIn) (*mandateOut, error) {
			if d.Mandates == nil {
				return nil, unavailable("mandates")
			}
			p := auth.FromContext(ctx)
			m, err := d.Mandates.Revoke(ctx, p, in.ID, in.Body.Reason)
			if err != nil {
				return nil, m2bErr(err, "M6N-M1")
			}
			w, err := mandateWire(ctx, d, p, m)
			if err != nil {
				return nil, m2bErr(err, "M6N-M1")
			}
			return &mandateOut{Status: 200, Body: w}, nil
		})
}

// mandateWire renders one mandate with its usage for the viewer.
func mandateWire(ctx context.Context, d Deps, viewer auth.Principal, m mandates.Mandate) (Mandate, error) {
	u, err := d.Mandates.Usage(ctx, m.ID)
	if err != nil {
		return Mandate{}, err
	}
	now := d.clock()
	w := Mandate{ID: m.ID, Holder: m.HolderName(), Title: m.Title, BudgetMicroUSD: int64(m.Budget), BudgetUSD: m.Budget.USD(),
		ValidFrom: m.ValidFrom, ValidUntil: m.ValidUntil, EffectiveState: m.EffectiveState(now), Scope: scopeOut(m.Scope),
		Usage: usageOut(m.Budget, u), CreatedBy: mandateRef(viewer, m.CreatedBy), CreatedAt: m.CreatedAt}
	if m.RevokedAt != nil {
		w.Revoked = &MandateRevokedOrNull{By: mandateRef(viewer, *m.RevokedBy), At: *m.RevokedAt, Reason: *m.RevokeReason}
	}
	return w, nil
}
