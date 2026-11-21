package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/desk"
)

// maskPlanRefs renders the principal references of a plan for the viewer (D-M2a-19, C309): the stored ones carry the raw key.
func maskPlanRefs(viewer auth.Principal, s *desk.PlanSummary) {
	s.CreatedBy = RefFor(viewer, s.CreatedBy.ID, s.CreatedBy.Kind, s.CreatedBy.Name)
}

func maskPlan(viewer auth.Principal, p *desk.Plan) {
	maskPlanRefs(viewer, &p.PlanSummary)
	if c := p.Verdicts.Critic; c != nil {
		c.By = RefFor(viewer, c.By.ID, c.By.Kind, c.By.Name)
	}
}

// registerDesk registers the Composer's Desk operations (M5b C359.1): plans (candidate scores) from a take or from an edit of
// another plan, their verdicts and the stateless score check. All are generic over MCP (D-DESK-14); a plan is never a take. The
// Owner and Editors use the Desk, Listeners reach none (U1 defaults of 2026-11-21); a person's verdict is the Owner's alone.
func registerDesk(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_plans", Method: "GET", Path: "/api/plans",
		Summary: "List Desk plans",
		Description: "Candidate scores on the Composer's Desk, newest first: a take's own score (source take), bar edits of another plan " +
			"(source edit) and the seeds of a plan job (source job). A plan is a score text with a seed, a cot and the request it belongs to; it is not a take. " +
			"Filter by job, batch, the take it was made for, source, your verdict (the Owner's for a person, the critic's for an agent) and whether a take was rendered from it. " +
			"Pages of up to 100: follow next_cursor.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/desk"},
		func(ctx context.Context, in *listPlansIn) (*planPageOut, error) {
			if d.Desk == nil {
				return nil, unavailable("desk")
			}
			p := auth.FromContext(ctx)
			pg, err := d.Desk.List(ctx, p, desk.Filter{JobID: in.JobID, BatchID: in.BatchID, BaseTrackID: in.BaseTrackID, Source: in.Source,
				Verdict: in.Verdict, Rendered: in.Rendered.ptr(), Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, "DESK-S1")
			}
			for i := range pg.Items {
				maskPlanRefs(p, &pg.Items[i])
			}
			return &planPageOut{Body: pg}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_plan", Method: "GET", Path: "/api/plans/{id}",
		Summary: "Get a Desk plan",
		Description: "One plan with its score: header facts, voices, sections with their bar ranges and seconds, the edit that made it, the plans edited " +
			"from it, both raters' verdicts, its auditions and the render jobs that used it. include picks abc (the score text), bars (the byte span and start " +
			"second of every bar, for bar editing), display (a standard-ABC rewrite when a YuE2 score reads differently under the standard accidental rule), " +
			"auditions and renders; the default is every part but bars.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/desk/plans/:id"},
		func(ctx context.Context, in *getPlanIn) (*planOut, error) {
			if d.Desk == nil {
				return nil, unavailable("desk")
			}
			inc, err := desk.ParseInclude(in.Include)
			if err != nil {
				return nil, m2bErr(err, "DESK-S1")
			}
			p := auth.FromContext(ctx)
			pl, err := d.Desk.Get(ctx, p, in.ID, inc)
			if err != nil {
				return nil, m2bErr(err, "DESK-S1")
			}
			maskPlan(p, &pl)
			return &planOut{Body: pl}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "check_abc", Method: "POST", Path: "/api/abc/check",
		Summary: "Check an ABC score",
		Description: "Parses a score text and reports what the Desk shows about it, without storing anything: syntax errors with line and column, warnings, " +
			"header (meter, unit, tempo, key), voices, sections, bar count, whether the score is timed and its seconds per bar and duration, event and chord counts, " +
			"the bars that read differently under the standard accidental rule, and a display text. With base_abc it also lists how the text breaks the structure of the base " +
			"(voices, bar counts, bar lengths, header, sections). The dialect is yue2 unless given.",
	}, bodyDesk), Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/desk"},
		func(ctx context.Context, in *checkABCIn) (*checkABCOut, error) {
			if d.Desk == nil {
				return nil, unavailable("desk")
			}
			rep, err := d.Desk.Check(ctx, desk.CheckIn{ABC: in.Body.ABC, BaseABC: in.Body.BaseABC, Dialect: in.Body.Dialect})
			if err != nil {
				return nil, m2bErr(err, "DESK-S1")
			}
			return &checkABCOut{Body: rep}, nil
		})

	Register(a, created(huma.Operation{
		OperationID: "create_plan_from_take", Method: "POST", Path: "/api/tracks/{id}/plan",
		Summary: "Open a take's score on the Desk",
		Description: "Makes the plan of a take's own score so it can be edited and auditioned. One plan per take: a repeat returns the existing plan (200 instead of 201). " +
			"422 PLAN_SOURCE_UNSUPPORTED with reason engine, reference, lossy, transform, no_score or too_large when the take has no usable YuE2 score. Needs an Idempotency-Key.",
	}), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id", Idempotency: "create"},
		func(ctx context.Context, in *createPlanFromTakeIn) (*planCreatedOut, error) {
			if d.Desk == nil {
				return nil, unavailable("desk")
			}
			if err := d.visible(ctx, in.ID); err != nil {
				return nil, err
			}
			p := auth.FromContext(ctx)
			pl, made, err := d.Desk.FromTake(ctx, p, in.ID)
			if err != nil {
				return nil, m2bErr(err, "DESK-S1")
			}
			maskPlan(p, &pl)
			return &planCreatedOut{Status: createdStatus(made), Body: pl}, nil
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "edit_plan", Method: "POST", Path: "/api/plans/{id}/edits",
		Summary: "Edit a plan's bars",
		Description: "Applies up to 20 bar edits (replace_bars, transpose_bars, set_tempo) and stores the result as a new plan; the edited plan never changes. " +
			"Voices, bar counts, headers and sections must stay as they are and each edited bar must have the meter's length (set_tempo may change the tempo). " +
			"422 PLAN_EDIT_INVALID names the rule or lists the violations; 422 PLAN_NO_CHANGE when the bytes do not change. The same result of the same plan is one plan: " +
			"a repeat returns it (200 instead of 201). Needs an Idempotency-Key.",
	}), bodyDesk), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/desk/plans/:id", Idempotency: "create"},
		func(ctx context.Context, in *editPlanIn) (*planCreatedOut, error) {
			if d.Desk == nil {
				return nil, unavailable("desk")
			}
			p := auth.FromContext(ctx)
			pl, made, err := d.Desk.Edit(ctx, p, in.ID, desk.EditIn{Ops: in.Body.Ops, Rationale: in.Body.Rationale})
			if err != nil {
				return nil, m2bErr(err, "DESK-S1")
			}
			maskPlan(p, &pl)
			return &planCreatedOut{Status: createdStatus(made), Body: pl}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "set_plan_verdict", Method: "PUT", Path: "/api/plans/{id}/verdict",
		Summary: "Like or reject a plan",
		Description: "Records a verdict on a plan: liked, rejected, or null to clear it, with an optional note. A person's verdict is the Owner's; an agent's is the critic's, " +
			"stored apart, and neither changes the other. Answers the plan.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/desk/plans/:id", OwnerOnly: true},
		func(ctx context.Context, in *setPlanVerdictIn) (*planOut, error) {
			if d.Desk == nil {
				return nil, unavailable("desk")
			}
			v := desk.VerdictIn{}
			if in.Body.Verdict != nil {
				v.Verdict = *in.Body.Verdict
			}
			if in.Body.Note != nil {
				v.Note = *in.Body.Note
			}
			p := auth.FromContext(ctx)
			pl, err := d.Desk.SetVerdict(ctx, p, in.ID, v)
			if err != nil {
				return nil, m2bErr(err, "DESK-S1")
			}
			maskPlan(p, &pl)
			return &planOut{Body: pl}, nil
		})
}

func createdStatus(made bool) int {
	if made {
		return 201
	}
	return 200
}
