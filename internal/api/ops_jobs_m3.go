package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// maxPageBytesJobs is the page cap of the job and batch lists (M1 C17).
const maxPageBytesJobs = 90000

// jobsErr maps jobs-service errors to the problem model.
func jobsErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jobs.ErrNotBatchOwner):
		return Err(http.StatusForbidden, "NOT_BATCH_OWNER", "only the principal that created this batch may cancel it", nil)
	case errors.Is(err, jobs.ErrBadCursor):
		return Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "cursor is not one this service issued", map[string]any{"field": "cursor"})
	case errors.Is(err, sql.ErrNoRows):
		return Err(http.StatusNotFound, "NOT_FOUND", "no such resource", nil)
	case errors.Is(err, errors.ErrUnsupported):
		return Err(http.StatusNotImplemented, "NOT_IMPLEMENTED", "this operation is not implemented yet", map[string]any{"task": "M3-B6"})
	}
	return libErr(err)
}

// parseWhenParam reads an RFC 3339 query parameter; empty is the zero time.
func parseWhenParam(field, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", field+" must be an RFC 3339 date-time", map[string]any{"field": field})
	}
	return t, nil
}

func tsOut(t time.Time) string { return store.FormatTime(t) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := tsOut(*t)
	return &s
}

func usd(m money.MicroUSD) string { return m.USD() }

// keyKind splits a principal key ("human:nik", "agent:tok") into its kind and id.
func keyKind(key string) (kind, id string) {
	kind, id, _ = strings.Cut(key, ":")
	return kind, id
}

func jobsJobCost(c jobs.JobCostInfo) JobCost {
	out := JobCost{ExpectedMicroUSD: int64(c.Expected), ExpectedUSD: usd(c.Expected), Basis: c.Basis}
	if c.Display != nil {
		v, s := int64(*c.Display), usd(*c.Display)
		out.DisplayMicroUSD, out.DisplayUSD = &v, &s
	}
	return out
}

func jobsSummary(j jobs.Job) JobSummary {
	return JobSummary{ID: j.ID, BatchID: j.BatchID, TakeIndex: j.TakeIndex, State: j.State, Outcome: j.Outcome, ErrorCode: j.ErrorCode, Title: j.Title,
		RequestID: j.RequestID, Seed: j.Seed, Operator: j.Operator, ParentTrackID: j.ParentTrackID, DraftID: j.DraftID, TrackID: j.TrackID,
		CreatedAt: tsOut(j.CreatedAt), UpdatedAt: tsOut(j.UpdatedAt), Cost: jobsJobCost(j.Cost)}
}

func jobsFull(j jobs.Job) Job {
	out := Job{JobSummary: jobsSummary(j), Engine: j.Engine, Backend: j.Backend, Mode: j.Mode, Attempts: j.Attempts, Resubmits: j.Resubmits,
		StartedAt: tsPtr(j.StartedAt), FinishedAt: tsPtr(j.FinishedAt), DeadlineAt: tsPtr(j.DeadlineAt), TimeoutS: j.TimeoutS, ExpectedAudioS: j.ExpectedAudioS,
		AudioS: j.AudioS, Env: j.Env, CancelRequestedAt: tsPtr(j.CancelReqAt), ErrorMessage: j.ErrorMessage,
		Timings:   JobTimings{QueueDelayMS: j.Timings.QueueDelayMS, ExecutionMS: j.Timings.ExecutionMS, ColdStartS: j.Timings.ColdStartS},
		Artifacts: []JobArtifact{}, Events: []JobEvent{}}
	if j.TruncatedABC != nil && j.TruncSem != nil {
		t := JobTruncatedOrNull{ABC: *j.TruncatedABC, Semantic: *j.TruncSem}
		out.Truncated = &t
	}
	for _, a := range j.Artifacts {
		out.Artifacts = append(out.Artifacts, JobArtifact{Name: a.Name, Bytes: a.Bytes, SHA256: a.SHA256, Verified: a.Verified})
	}
	for _, e := range j.Events {
		out.Events = append(out.Events, JobEvent{At: tsOut(e.At), From: e.From, To: e.To, Event: e.Event, Code: e.Code})
	}
	return out
}

func jobsApproval(viewer auth.Principal, b jobs.Batch) ApprovalStatus {
	a := b.Approval
	out := ApprovalStatus{BatchID: a.BatchID, State: a.State, Reasons: a.Reasons, Via: a.Via, DecidedAt: tsPtr(a.DecidedAt), Reason: a.Reason,
		ExpiresAt: tsOut(a.ExpiresAt), LastRequestedAt: tsPtr(a.LastRequestedAt), NextRequestAllowedAt: tsPtr(a.NextRequestAllowedAt)}
	if a.DecidedByKey != nil {
		ref := PrincipalRefOrNull(RefFor(viewer, *a.DecidedByKey, a.DecidedByKind, a.DecidedByName))
		out.DecidedBy = &ref
	}
	return out
}

func jobsBatchSummary(viewer auth.Principal, b jobs.Batch) BatchSummary {
	c, k := b.Counts, b.Cost
	return BatchSummary{ID: b.ID, State: b.State, Via: b.Via, CreatedBy: RefFor(viewer, b.CreatedByKey, b.CreatedByKind, b.CreatedByName),
		CreatedAt: tsOut(b.CreatedAt), FinishedAt: tsPtr(b.FinishedAt), Rationale: b.Rationale, Takes: b.Takes,
		Counts: BatchCounts{Awaiting: c.Awaiting, Queued: c.Queued, Running: c.Running, Completed: c.Completed, Truncated: c.Truncated, Failed: c.Failed, Cancelled: c.Cancelled, Expired: c.Expired},
		Cost: BatchCost{ExpectedMicroUSD: int64(k.Expected), ExpectedUSD: usd(k.Expected), MaxMicroUSD: int64(k.Max), MaxUSD: usd(k.Max),
			CeilingMicroUSD: int64(k.Ceiling), CeilingUSD: usd(k.Ceiling), ReservedMicroUSD: int64(k.Reserved), ReservedUSD: usd(k.Reserved),
			SettledMicroUSD: int64(k.Settled), SettledUSD: usd(k.Settled), OutstandingMicroUSD: int64(k.Outstanding), OutstandingUSD: usd(k.Outstanding), Reconciled: k.Reconciled},
		Approval: jobsApproval(viewer, b), EstimateID: b.EstimateID}
}

func jobsBatch(viewer auth.Principal, b jobs.Batch) Batch {
	out := Batch{BatchSummary: jobsBatchSummary(viewer, b), Jobs: []JobSummary{}}
	for _, j := range b.Jobs {
		out.Jobs = append(out.Jobs, jobsSummary(j))
	}
	return out
}

// capJobPage cuts items at the 90 000-byte page cap and returns the cursor of the last kept item when it did.
func capJobPage(items []JobSummary, next *string) ([]JobSummary, *string) {
	size := 0
	var last JobSummary
	for i, it := range items {
		b, _ := json.Marshal(it)
		if size += len(b) + 1; size > maxPageBytesJobs && i > 0 {
			c := jobs.CursorFor(mustParseTS(last.CreatedAt), last.ID)
			return items[:i], &c
		}
		last = it
	}
	return items, next
}

func capBatchPage(items []BatchSummary, next *string) ([]BatchSummary, *string) {
	size := 0
	var last BatchSummary
	for i, it := range items {
		b, _ := json.Marshal(it)
		if size += len(b) + 1; size > maxPageBytesJobs && i > 0 {
			c := jobs.CursorFor(mustParseTS(last.CreatedAt), last.ID)
			return items[:i], &c
		}
		last = it
	}
	return items, next
}

func mustParseTS(s string) time.Time {
	t, _ := store.ParseTime(s)
	return t
}

func nextCursor(c string) *string {
	if c == "" {
		return nil
	}
	return &c
}

// registerJobsM3 registers the batch and job operations (C81).
func registerJobsM3(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_batches", Method: "GET", Path: "/api/batches",
		Summary:     "List generation batches",
		Description: "Batches newest first with their counts and costs, filtered by state, creator kind or time.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/jobs"},
		func(ctx context.Context, in *listBatchesIn) (*batchPageOut, error) {
			if d.Jobs == nil {
				return nil, unavailable("jobs")
			}
			since, err := parseWhenParam("since", in.Since)
			if err != nil {
				return nil, err
			}
			until, err := parseWhenParam("until", in.Until)
			if err != nil {
				return nil, err
			}
			p := auth.FromContext(ctx)
			page, err := d.Jobs.ListBatches(ctx, p, jobs.BatchQuery{States: in.State, PrincipalKinds: in.PrincipalKind, Since: since, Until: until, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, jobsErr(err)
			}
			out := &batchPageOut{}
			out.Body.Items = []BatchSummary{}
			for _, b := range page.Items {
				out.Body.Items = append(out.Body.Items, jobsBatchSummary(p, b))
			}
			out.Body.Items, out.Body.NextCursor = capBatchPage(out.Body.Items, nextCursor(page.NextCursor))
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_batch", Method: "GET", Path: "/api/batches/{id}",
		Summary:     "Get a generation batch",
		Description: "One batch with its approval, counts, costs and jobs in take order.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/batches/:id"},
		func(ctx context.Context, in *batchIDIn) (*batchOut, error) {
			if d.Jobs == nil {
				return nil, unavailable("jobs")
			}
			p := auth.FromContext(ctx)
			b, err := d.Jobs.Batch(ctx, p, in.ID)
			if err != nil {
				return nil, jobsErr(err)
			}
			return &batchOut{Body: jobsBatch(p, b)}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "cancel_batch", Method: "POST", Path: "/api/batches/{id}/cancel",
		Summary:     "Cancel a generation batch",
		Description: "Records a cancel intent on every unfinished job; the runner stops them and releases what was not used. Finished batches are unchanged. An agent can cancel only its own batches.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/jobs"},
		func(ctx context.Context, in *cancelIn) (*batchOut, error) {
			if d.Jobs == nil {
				return nil, unavailable("jobs")
			}
			p := auth.FromContext(ctx)
			b, err := d.Jobs.CancelBatch(ctx, p, in.ID, reasonOf(in.Body.Reason))
			if err != nil {
				return nil, jobsErr(err)
			}
			return &batchOut{Body: jobsBatch(p, b)}, nil
		})

	Register(a, huma.Operation{
		OperationID: "list_jobs", Method: "GET", Path: "/api/jobs",
		Summary:     "List generation jobs",
		Description: "Jobs newest first, filtered by state, batch or track (as parent or child).",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/jobs"},
		func(ctx context.Context, in *listJobsIn) (*jobPageOut, error) {
			if d.Jobs == nil {
				return nil, unavailable("jobs")
			}
			since, err := parseWhenParam("since", in.Since)
			if err != nil {
				return nil, err
			}
			p := auth.FromContext(ctx)
			page, err := d.Jobs.ListJobs(ctx, p, jobs.JobQuery{States: in.State, BatchID: in.BatchID, TrackID: in.TrackID, Since: since, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, jobsErr(err)
			}
			out := &jobPageOut{}
			out.Body.Items = []JobSummary{}
			for _, j := range page.Items {
				out.Body.Items = append(out.Body.Items, jobsSummary(j))
			}
			out.Body.Items, out.Body.NextCursor = capJobPage(out.Body.Items, nextCursor(page.NextCursor))
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_job", Method: "GET", Path: "/api/jobs/{id}",
		Summary:     "Get a generation job",
		Description: "One job: state, outcome, cost, timings, environment, artifacts and its newest events. The job id is the take id.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/jobs/:id"},
		func(ctx context.Context, in *jobIDIn) (*jobOut, error) {
			if d.Jobs == nil {
				return nil, unavailable("jobs")
			}
			j, err := d.Jobs.Job(ctx, auth.FromContext(ctx), in.ID)
			if err != nil {
				return nil, jobsErr(err)
			}
			return &jobOut{Body: jobsFull(j)}, nil
		})

	Register(a, huma.Operation{
		OperationID: "wait_for_job", Method: "GET", Path: "/api/jobs/{id}/wait",
		Summary:     "Wait for a job to change",
		Description: "Long-poll: returns as soon as the job's state differs from since_state (default: now) or is terminal, else after timeout_s (0-55, default 25) with timed_out true.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/jobs/:id"},
		func(ctx context.Context, in *waitForJobIn) (*waitJobOut, error) {
			if d.Jobs == nil {
				return nil, unavailable("jobs")
			}
			p := auth.FromContext(ctx)
			since := in.SinceState
			if since == "" {
				cur, err := d.Jobs.Job(ctx, p, in.ID)
				if err != nil {
					return nil, jobsErr(err)
				}
				since = cur.State
			}
			res, err := d.Jobs.Wait(ctx, p, in.ID, since, time.Duration(in.TimeoutS)*time.Second)
			if err != nil {
				return nil, jobsErr(err)
			}
			return &waitJobOut{Body: WaitJobResult{Job: jobsFull(res.Job), Changed: res.Changed, TimedOut: res.TimedOut}}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "cancel_job", Method: "POST", Path: "/api/jobs/{id}/cancel",
		Summary:     "Cancel a generation job",
		Description: "Records a cancel intent; the runner cancels the provider job and settles what ran. Terminal jobs are unchanged. An agent can cancel only its own batches' jobs.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/jobs"},
		func(ctx context.Context, in *cancelIn) (*jobOut, error) {
			if d.Jobs == nil {
				return nil, unavailable("jobs")
			}
			j, err := d.Jobs.CancelJob(ctx, auth.FromContext(ctx), in.ID, reasonOf(in.Body.Reason))
			if err != nil {
				return nil, jobsErr(err)
			}
			return &jobOut{Body: jobsFull(j)}, nil
		})
}

func reasonOf(r *string) string {
	if r == nil {
		return ""
	}
	return *r
}
