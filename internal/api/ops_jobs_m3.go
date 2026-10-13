package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// registerJobsM3 registers the batch and job operations (C81). Every handler is a STUB(M3-B6) except
// wait_for_job (STUB(M3-B5)).
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
			return nil, notImplemented("M3-B6") // STUB(M3-B6)
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
			return nil, notImplemented("M3-B6") // STUB(M3-B6)
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
			return nil, notImplemented("M3-B6") // STUB(M3-B6)
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
			return nil, notImplemented("M3-B6") // STUB(M3-B6)
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
			return nil, notImplemented("M3-B6") // STUB(M3-B6)
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
			return nil, notImplemented("M3-B5") // STUB(M3-B5)
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
			return nil, notImplemented("M3-B6") // STUB(M3-B6)
		})
}
