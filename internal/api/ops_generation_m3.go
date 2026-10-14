package api

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// genErr maps generation, spend and lineage errors to the problem model: a *problem.Error keeps its status,
// code and details; a missing track is 404; library errors go through libErr.
func genErr(err error) error {
	var pe *problem.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &pe):
		return Err(pe.Status, pe.Code, pe.Message, pe.Details)
	case errors.Is(err, lineage.ErrUnconfigured):
		return unavailable("lineage")
	case errors.Is(err, lineage.ErrNotFound):
		return Err(404, "NOT_FOUND", "no such resource", nil)
	}
	return libErr(err)
}

func usd(m money.MicroUSD) string { return m.USD() }

func optStr(p *time.Time) *string {
	if p == nil {
		return nil
	}
	s := store.FormatTime(*p)
	return &s
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func refOf(viewer auth.Principal, a generation.Actor) PrincipalRef {
	return RefFor(viewer, a.Key, a.Kind, a.Name)
}

// ---- item conversion ----

func patchIn(p *Patch) *lineage.Patch {
	if p == nil {
		return nil
	}
	out := &lineage.Patch{Style: p.Style, Lyrics: p.Lyrics, Cot: p.Cot}
	if p.ABC.Set {
		out.ABC = lineage.OptString{Set: true, Null: p.ABC.Null, Value: p.ABC.Value}
	}
	if p.CFGScale.Set {
		out.CFGScale = lineage.OptNumber{Set: true, Null: p.CFGScale.Null, Value: p.CFGScale.Value}
	}
	return out
}

func itemIn(it ItemIn) generation.Item {
	return generation.Item{Mode: it.Mode, ParentTrackID: it.ParentTrackID, ParentTrackIDs: it.ParentTrackIDs, DraftIDs: it.DraftIDs,
		Recipe: []byte(it.Recipe), Patch: patchIn(it.Patch), TemplateMode: it.TemplateMode, NSeeds: it.NSeeds, Seeds: it.Seeds, Title: it.Title}
}

// estimateVia is the path the later create will take: the operator's SPA confirms through the human path,
// everyone else through /api/ (C72.6).
func estimateVia(p auth.Principal) string {
	if p.Kind == auth.KindHuman {
		return spend.ViaHumanCreate
	}
	return spend.ViaAPI
}

// ---- output conversion ----

func providerBrief(st provider.State) ProviderBrief {
	b := ProviderBrief{GenerationEnabled: st.Enabled, Armed: st.Armed, Tripped: st.Tripped}
	if !st.Enabled {
		b.DisabledReason = nonEmpty(st.Reason)
	}
	return b
}

func potOut(p generation.Pot) Pot {
	return Pot{CapMicroUSD: int64(p.Cap), CapUSD: usd(p.Cap), UsedMicroUSD: int64(p.Used), UsedUSD: usd(p.Used), LeftMicroUSD: int64(p.Left), LeftUSD: usd(p.Left)}
}

func estimateFrom(e generation.Estimate) Estimate {
	out := Estimate{
		EstimateID: e.ID, ConfirmationToken: e.Token, ExpiresAt: store.FormatTime(e.ExpiresAt), Engine: e.Engine, Backend: e.Backend, Takes: e.Takes,
		Cost: EstimateCost{
			ExpectedMicroUSD: int64(e.Cost.Expected), ExpectedUSD: usd(e.Cost.Expected), MaxMicroUSD: int64(e.Cost.Max), MaxUSD: usd(e.Cost.Max),
			RetryAllowanceMicroUSD: int64(e.Cost.RetryAllowance), RetryAllowanceUSD: usd(e.Cost.RetryAllowance),
			CeilingMicroUSD: int64(e.Cost.Ceiling), CeilingUSD: usd(e.Cost.Ceiling), CeilingDisplay: e.Cost.Ceiling.DisplayCeil(),
			AttemptOverheadMicroUSD: int64(e.Cost.AttemptOverhead), AttemptOverheadUSD: usd(e.Cost.AttemptOverhead),
		},
		ETAS:  e.ETAS,
		Price: EstimatePrice{PerHourMicroUSD: int64(e.PricePerHour), PerHourUSD: usd(e.PricePerHour), GPUPools: append([]string{}, e.GPUPools...), ObservedAt: optStr(e.PriceObservedAt)},
		Items: []EstimateItem{}, Warnings: []EstimateWarning{}, RequiresHumanApproval: e.RequiresApproval, ApprovalReasons: append([]string{}, e.ApprovalReasons...),
		Budget: EstimateBudget{Fits: e.Budget.Fits, WouldExceed: append([]string{}, e.Budget.WouldExceed...), Month: potOut(e.Month), Day: potOut(e.Day),
			BatchCapMicroUSD: int64(e.BatchCap), BatchCapUSD: usd(e.BatchCap)},
		Provider: providerBrief(e.Provider),
	}
	for _, it := range e.Items {
		ei := EstimateItem{Index: it.Index, Mode: it.Mode, Takes: []TakePlan{}}
		for _, t := range it.Takes {
			ei.Takes = append(ei.Takes, TakePlan{TakeIndex: t.TakeIndex, RequestID: t.RequestID, Seed: t.Seed, Title: t.Title, Operator: t.Operator,
				ParentTrackID: t.ParentTrackID, DraftID: t.DraftID, ExpectedAudioS: t.ExpectedAudioS, TimeoutS: t.TimeoutS,
				MaxMicroUSD: int64(t.Max), MaxUSD: usd(t.Max)})
		}
		out.Items = append(out.Items, ei)
	}
	for _, w := range e.Warnings {
		out.Warnings = append(out.Warnings, EstimateWarning{TakeIndex: w.TakeIndex, Code: w.Code, Field: w.Field, Severity: w.Severity, Message: w.Text})
	}
	return out
}

func approvalFrom(viewer auth.Principal, v generation.ApprovalView) ApprovalStatus {
	a := ApprovalStatus{BatchID: v.BatchID, State: v.State, Reasons: append([]string{}, v.Reasons...), Via: nonEmpty(v.Via), Reason: nonEmpty(v.Reason),
		DecidedAt: optStr(v.DecidedAt), ExpiresAt: store.FormatTime(v.ExpiresAt), LastRequestedAt: optStr(v.LastRequestedAt), NextRequestAllowedAt: optStr(v.NextRequestAllowedAt)}
	if v.DecidedBy != "" {
		r := PrincipalRefOrNull(refOf(viewer, v.Decider))
		a.DecidedBy = &r
	}
	if v.Notified || v.NextRequestAllowedAt != nil {
		n := v.Notified
		a.Notified = &n
	}
	return a
}

func jobSummaryFrom(j generation.JobView) JobSummary {
	c := JobCost{ExpectedMicroUSD: int64(j.Expected), ExpectedUSD: usd(j.Expected), Basis: j.Basis}
	if j.Display != nil {
		m, s := int64(*j.Display), usd(*j.Display)
		c.DisplayMicroUSD, c.DisplayUSD = &m, &s
	}
	return JobSummary{ID: j.ID, BatchID: j.BatchID, TakeIndex: j.TakeIndex, State: j.State, Outcome: j.Outcome, ErrorCode: j.ErrorCode, Title: j.Title,
		RequestID: j.RequestID, Seed: j.Seed, Operator: j.Operator, ParentTrackID: j.ParentTrackID, DraftID: j.DraftID, TrackID: j.TrackID,
		CreatedAt: store.FormatTime(j.CreatedAt), UpdatedAt: store.FormatTime(j.UpdatedAt), Cost: c}
}

func batchFrom(viewer auth.Principal, c generation.Created) Batch {
	b := Batch{BatchSummary: BatchSummary{
		ID: c.BatchID, State: c.State, Via: c.Via, CreatedBy: refOf(viewer, c.CreatedBy), CreatedAt: store.FormatTime(c.CreatedAt), FinishedAt: optStr(c.FinishedAt),
		Rationale: c.Rationale, Takes: c.Takes,
		Counts: BatchCounts{Awaiting: c.Counts.Awaiting, Queued: c.Counts.Queued, Running: c.Counts.Running, Completed: c.Counts.Completed, Truncated: c.Counts.Truncated,
			Failed: c.Counts.Failed, Cancelled: c.Counts.Cancelled, Expired: c.Counts.Expired},
		Cost: BatchCost{ExpectedMicroUSD: int64(c.Expected), ExpectedUSD: usd(c.Expected), MaxMicroUSD: int64(c.Max), MaxUSD: usd(c.Max),
			CeilingMicroUSD: int64(c.Ceiling), CeilingUSD: usd(c.Ceiling), ReservedMicroUSD: int64(c.Reserved), ReservedUSD: usd(c.Reserved),
			SettledMicroUSD: int64(c.Settled), SettledUSD: usd(c.Settled), OutstandingMicroUSD: int64(c.Outstanding), OutstandingUSD: usd(c.Outstanding), Reconciled: c.Reconciled},
		Approval: approvalFrom(viewer, c.Approval), EstimateID: c.EstimateID,
	}, Jobs: []JobSummary{}}
	for _, j := range c.Jobs {
		b.Jobs = append(b.Jobs, jobSummaryFrom(j))
	}
	return b
}

// encodeBatch is the encoder create hands the generation service: the exact 201 body, built before commit.
func encodeBatch(viewer auth.Principal) func(generation.Created) (int, []byte, error) {
	return func(c generation.Created) (int, []byte, error) {
		b, err := EncodeResponse(batchFrom(viewer, c))
		return 201, b, err
	}
}

func lineageFrom(t lineage.Tree) LineageTree {
	out := LineageTree{RootID: t.RootID, Total: t.Total, Truncated: t.Truncated, Nodes: []LineageNode{}}
	for _, n := range t.Nodes {
		ln := LineageNode{ID: n.ID, ParentID: n.ParentID, Generation: n.Generation, Title: n.Title, Status: n.Status, Seed: n.Seed, RecipeKnown: n.RecipeKnown,
			Lossy: n.Lossy, RequestID: n.RequestID, Origin: n.Origin, Operator: nonEmpty(n.Operator), Inferred: n.Inferred, Rationale: n.Rationale,
			Stars: n.Stars, CriticStars: n.CriticStars, Outcome: n.Outcome, JobID: n.JobID, CreatedAt: n.CreatedAt, ChildrenCount: n.ChildrenCount}
		if n.CostMicroUSD != nil && n.CostBasis != nil {
			ln.Cost = &library.GenerationCost{DisplayMicroUSD: int64(*n.CostMicroUSD), DisplayUSD: usd(*n.CostMicroUSD), Basis: *n.CostBasis}
		}
		out.Nodes = append(out.Nodes, ln)
	}
	return out
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// registerGenerationM3 registers the estimate, create, approval and lineage operations (C81). Every
// handler checks its dependency (503 UNAVAILABLE {component}).
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
			p := auth.FromContext(ctx)
			items := make([]generation.Item, len(in.Body.Items))
			for i, it := range in.Body.Items {
				items[i] = itemIn(it)
			}
			e, err := d.Generation.Estimate(ctx, p, generation.EstimateIn{Engine: strOrEmpty(in.Body.Engine), Items: items, Via: estimateVia(p)})
			if err != nil {
				return nil, genErr(err)
			}
			return &estimateOut{Body: estimateFrom(e)}, nil
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
			return createBatch(ctx, d, in.Body, spend.ViaAPI, nil)
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
			return createBatch(ctx, d, in.Body, spend.ViaHumanCreate, nil)
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
			p := auth.FromContext(ctx)
			it := ItemIn{Mode: in.Body.Mode, ParentTrackID: &in.ID, NSeeds: in.Body.NSeeds, Seeds: in.Body.Seeds, Patch: in.Body.Patch, Title: in.Body.Title}
			e, err := d.Generation.Estimate(ctx, p, generation.EstimateIn{Items: []generation.Item{itemIn(it)}, Via: estimateVia(p)})
			if err != nil {
				return nil, genErr(err)
			}
			return &estimateOut{Body: estimateFrom(e)}, nil
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
			return createBatch(ctx, d, in.Body, spend.ViaAPI, &in.ID)
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
			p := auth.FromContext(ctx)
			v, err := d.Generation.RequestApproval(ctx, p, in.ID)
			if err != nil {
				return nil, genErr(err)
			}
			return &approvalOut{Body: approvalFrom(p, v)}, nil
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
			p := auth.FromContext(ctx)
			v, err := d.Generation.Approval(ctx, p, in.ID)
			if err != nil {
				return nil, genErr(err)
			}
			return &approvalOut{Body: approvalFrom(p, v)}, nil
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
			p := auth.FromContext(ctx)
			c, err := d.Generation.Approve(ctx, p, in.ID, strOrEmpty(in.Body.Reason))
			if err != nil {
				return nil, genErr(err)
			}
			return &batchOut{Body: batchFrom(p, c)}, nil
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
			p := auth.FromContext(ctx)
			c, err := d.Generation.Reject(ctx, p, in.ID, in.Body.Reason)
			if err != nil {
				return nil, genErr(err)
			}
			return &batchOut{Body: batchFrom(p, c)}, nil
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
			t, err := d.Lineage.Tree(ctx, in.ID, in.Limit)
			if err != nil {
				return nil, genErr(err)
			}
			return &lineageOut{Body: lineageFrom(t)}, nil
		})
}

// createBatch runs the shared body of the three spend operations. track is the path track of create_variant.
func createBatch(ctx context.Context, d Deps, body ConfirmBody, via string, track *string) (*batchOut, error) {
	p := auth.FromContext(ctx)
	c, err := d.Generation.Create(ctx, p, generation.CreateIn{Token: body.ConfirmationToken, Rationale: strOrEmpty(body.Rationale), Via: via, VariantTrackID: track}, encodeBatch(p))
	if err != nil {
		return nil, genErr(err)
	}
	audit.NoteCreated(ctx, append([]string{c.BatchID}, c.JobIDs...)...)
	return &batchOut{Body: batchFrom(p, c)}, nil
}
