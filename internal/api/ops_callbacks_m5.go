package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/callbacks"
)

// registerCallbacksM5 registers the three Callback Listening operations (C255, C260). Frozen by M5-F2; lane M5-C1 fills the
// handlers.
func registerCallbacksM5(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_callbacks", Method: "GET", Path: "/api/callbacks",
		Summary:     "List callbacks",
		Description: "Callbacks of Song PR children: pending ones first (oldest first), then answered ones. A callback asks the human whether a child fixed a comment, or kept what a keep comment praised, at the same musical spot.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/judge/callbacks"},
		func(ctx context.Context, in *listCallbacksIn) (*callbackPageOut, error) {
			if d.Callbacks == nil {
				return nil, unavailable("callbacks")
			}
			pg, err := d.Callbacks.List(ctx, auth.FromContext(ctx), callbacks.Filter{State: in.State, Kind: in.Kind, ChildTrackID: in.ChildTrackID,
				TrackID: in.TrackID, CommentID: in.CommentID, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, taskC1)
			}
			out := CallbackPage{Items: make([]Callback, len(pg.Items)), NextCursor: pg.NextCursor, Total: pg.Total}
			for i, c := range pg.Items {
				out.Items[i] = callbackWire(c)
			}
			return &callbackPageOut{Body: out}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_callback", Method: "GET", Path: "/api/callbacks/{id}",
		Summary:     "Get a callback",
		Description: "One callback with the comment, the child, the spot on the child and on the source take, and the verdicts the caller may see.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/judge/callbacks/:id"},
		func(ctx context.Context, in *getCallbackIn) (*callbackOut, error) {
			if d.Callbacks == nil {
				return nil, unavailable("callbacks")
			}
			c, err := d.Callbacks.Get(ctx, auth.FromContext(ctx), in.ID)
			if err != nil {
				return nil, m2bErr(err, taskC1)
			}
			return &callbackOut{Body: callbackWire(c)}, nil
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "submit_callback_verdict", Method: "POST", Path: "/api/callbacks/{id}/verdicts",
		Summary:     "Answer a callback",
		Description: "fixed, not_yet or worse for a fix callback; kept or lost for a keep callback. A human answer moves the comment; an agent answer is a critic verdict that never does. Repeating an identical answer returns it with replayed true.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/judge/callbacks/:id"},
		func(ctx context.Context, in *submitCallbackVerdictIn) (*callbackSubmitOut, error) {
			if d.Callbacks == nil {
				return nil, unavailable("callbacks")
			}
			b := in.Body
			res, err := d.Callbacks.Submit(ctx, auth.FromContext(ctx), in.ID, callbacks.VerdictInput{Verdict: b.Verdict, Note: b.Note, HeardAtS: b.HeardAtS})
			if err != nil {
				return nil, m2bErr(err, taskC1)
			}
			out := &callbackSubmitOut{Status: 201, Body: CallbackSubmitResult{Replayed: res.Replayed, Verdict: verdictWire(res.Verdict),
				CommentState: res.CommentState, RegressionCommentID: res.RegressionCommentID}}
			if res.Replayed {
				out.Status = 200
			} else {
				audit.NoteCreated(ctx, res.Verdict.ID)
			}
			return out, nil
		})
}

func verdictWire(v callbacks.Verdict) CallbackVerdict {
	return CallbackVerdict{ID: v.ID, Source: v.Source, PrincipalKind: v.PrincipalKind, Verdict: v.Verdict, Note: v.Note, HeardAtS: v.HeardAtS,
		MapBasis: v.MapBasis, StateBefore: v.StateBefore, StateAfter: v.StateAfter, RegressionCommentID: v.RegressionCommentID, CreatedAt: v.CreatedAt}
}

func verdictOrNull(v *callbacks.Verdict) *CallbackVerdictOrNull {
	if v == nil {
		return nil
	}
	w := CallbackVerdictOrNull(verdictWire(*v))
	return &w
}

// callbackWire maps the service view onto the frozen wire type.
func callbackWire(c callbacks.Callback) Callback {
	out := Callback{ID: c.ID, Kind: c.Kind, Child: CallbackChild{ID: c.Child.ID, Title: c.Child.Title, Seed: c.Child.Seed}, PatchID: c.PatchID, JobID: c.JobID,
		Comment: CallbackComment{ID: c.Comment.ID, TrackID: c.Comment.TrackID, Kind: c.Comment.Kind, State: c.Comment.State, Body: c.Comment.Body,
			AtSecond: c.Comment.AtSecond, Section: c.Comment.Section, Voice: c.Comment.Voice},
		Relation: c.Relation, Spot: CallbackSpot{ChildAtS: c.Spot.ChildAtS, SourceAtS: c.Spot.SourceAtS, Basis: c.Spot.Basis, Confidence: c.Spot.Confidence, Label: c.Spot.Label},
		Links: CallbackLinks{Child: c.Links.Child, Source: c.Links.Source}, State: c.State, MyVerdict: verdictOrNull(c.MyVerdict), HumanVerdict: verdictOrNull(c.HumanVerdict),
		CriticVerdicts: make([]CallbackVerdict, len(c.CriticVerdicts)), CreatedAt: c.CreatedAt}
	for i, v := range c.CriticVerdicts {
		out.CriticVerdicts[i] = verdictWire(v)
	}
	return out
}
