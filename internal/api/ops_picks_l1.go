package api

import (
	"context"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/picks"
)

// pickViewOut renders a picks.PickView for viewer (the operator's key is hidden from agents by RefFor).
func pickViewOut(viewer auth.Principal, v picks.PickView) PickView {
	out := PickView{
		TakeID: v.TakeID, Source: string(v.Source), PinnedTakeID: v.PinnedTakeID, PinIneligible: v.PinIneligible, PinnedAt: v.PinnedAt,
		Evidence: RawRequest(v.Evidence), ChangedAt: v.ChangedAt, LastEventID: v.LastEventID, Seq: v.Seq, Pending: v.Pending, OpenProposals: v.OpenProposals,
	}
	if v.PinnedBy != nil && v.PinnedByKind != nil && v.PinnedByName != nil {
		r := RefFor(viewer, *v.PinnedBy, *v.PinnedByKind, *v.PinnedByName)
		out.PinnedBy = &r
	}
	return out
}

func proposalOut(viewer auth.Principal, p picks.Proposal) PickProposal {
	out := PickProposal{
		ID: p.ID, SongID: p.SongID, TakeID: p.TakeID, Rationale: p.Rationale, State: p.State, CreatedAt: p.CreatedAt, DecidedAt: p.DecidedAt,
		ProposedBy: RefFor(viewer, p.PrincipalID, string(auth.KindAgent), p.PrincipalName),
	}
	if p.DecidedBy != nil {
		k := KeyFor(viewer, *p.DecidedBy)
		out.DecidedBy = &k
	}
	return out
}

// actorKind restores "device" for the events of a paired device (the table records kind human for it).
func actorKind(key, kind string) string {
	if strings.HasPrefix(key, string(auth.KindDevice)+":") {
		return string(auth.KindDevice)
	}
	return kind
}

// registerPicksL1 registers the Pick operations (C96).
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
			viewer := auth.FromContext(ctx)
			pr, err := d.Picks.Propose(ctx, viewer, in.ID, in.Body.TakeID, in.Body.Rationale)
			if err != nil {
				return nil, m2bErr(err, "L1-B4")
			}
			audit.NoteCreated(ctx, pr.ID)
			return &pickProposalOut{Body: proposalOut(viewer, pr)}, nil
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
			viewer := auth.FromContext(ctx)
			res, err := d.Picks.Pin(ctx, viewer, in.ID, in.Body.TakeID, in.Body.ProposalID)
			if err != nil {
				return nil, m2bErr(err, "L1-B4")
			}
			out := &pinResultOut{}
			out.Body = PinResult{Pick: pickViewOut(viewer, res.Pick), Changed: res.Changed}
			if res.Proposal != nil {
				pr := proposalOut(viewer, *res.Proposal)
				out.Body.Proposal = &pr
			}
			return out, nil
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
			viewer := auth.FromContext(ctx)
			pr, err := d.Picks.Dismiss(ctx, viewer, in.ID)
			if err != nil {
				return nil, m2bErr(err, "L1-B4")
			}
			return &pickProposalOut{Body: proposalOut(viewer, pr)}, nil
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
			viewer := auth.FromContext(ctx)
			page, err := d.Picks.Events(ctx, picks.EventFilter{SongID: in.SongID, SongIDs: in.SongIDs, After: in.After, Limit: in.Limit})
			if err != nil {
				return nil, m2bErr(err, "L1-B4")
			}
			out := &pickEventsOut{}
			out.Body.Items = make([]PickEvent, 0, len(page.Items))
			for _, e := range page.Items {
				out.Body.Items = append(out.Body.Items, PickEvent{
					Cursor: e.Cursor, ID: e.ID, SongID: e.SongID, Seq: e.Seq, At: e.At, FromTakeID: e.FromTakeID, ToTakeID: e.ToTakeID,
					FromSource: e.FromSource, ToSource: e.ToSource, Reason: e.Reason, Actor: RefFor(viewer, e.Actor, actorKind(e.Actor, e.ActorKind), e.ActorName),
					Evidence: RawRequest(e.Evidence),
				})
			}
			out.Body.NextCursor = page.NextCursor
			return out, nil
		})
}
