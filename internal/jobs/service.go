// Package jobs is the job state machine, the runner, the ingest bridge, the reconciler and the job reads
// (M3 C75, C76). M3-B2 freezes constructors and method sets; M3-B5 (runner, states, Wait) and M3-B6
// (ingest, reconciler, reads, provider status) replace the STUB bodies.
package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// ErrNotImplemented marks a STUB(M3-B5/B6) body.
var ErrNotImplemented = errors.New("jobs: not implemented (STUB(M3-B5/B6))")

// Options builds the read service.
type Options struct {
	Store *store.Store
	Clock clock.Clock // nil => clock.Real
	Hub   *Hub        // nil => a private hub
	Gate  *provider.Gate
}

// Service answers the job and batch reads and Wait (C75.8).
type Service struct {
	o Options
}

// New builds the service.
func New(o Options) *Service {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.Hub == nil {
		o.Hub = NewHub()
	}
	return &Service{o: o}
}

// Hub returns the in-process change notifier shared with the runner.
func (s *Service) Hub() *Hub { return s.o.Hub }

// JobQuery filters ListJobs.
type JobQuery struct {
	States           []string
	BatchID, TrackID string
	Since            time.Time
	Limit            int
	Cursor           string
}

// BatchQuery filters ListBatches.
type BatchQuery struct {
	States, PrincipalKinds []string
	Since, Until           time.Time
	Limit                  int
	Cursor                 string
}

// Job is the read model of one job (C81 Job); the lane that fills it adds fields.
type Job struct {
	ID, BatchID, State, Title string
	TakeIndex                 int
	Outcome, ErrorCode        *string
	TrackID                   *string
	CreatedAt, UpdatedAt      time.Time
}

// Batch is the read model of one batch (C81 Batch).
type Batch struct {
	ID, State, Via, CreatedBy string
	Takes                     int
	Expected, Max, Ceiling    money.MicroUSD
	CreatedAt                 time.Time
	Jobs                      []Job
}

// JobPage and BatchPage carry a next cursor ("" at the end).
type (
	JobPage struct {
		Items      []Job
		NextCursor string
	}
	BatchPage struct {
		Items      []Batch
		NextCursor string
	}
)

// ListJobs lists jobs newest first. STUB(M3-B6).
func (s *Service) ListJobs(ctx context.Context, p auth.Principal, q JobQuery) (JobPage, error) {
	return JobPage{}, ErrNotImplemented
}

// Job reads one job. STUB(M3-B6).
func (s *Service) Job(ctx context.Context, p auth.Principal, id string) (Job, error) {
	return Job{}, ErrNotImplemented
}

// ListBatches lists batches newest first. STUB(M3-B6).
func (s *Service) ListBatches(ctx context.Context, p auth.Principal, q BatchQuery) (BatchPage, error) {
	return BatchPage{}, ErrNotImplemented
}

// Batch reads one batch with its jobs. STUB(M3-B6).
func (s *Service) Batch(ctx context.Context, p auth.Principal, id string) (Batch, error) {
	return Batch{}, ErrNotImplemented
}

// CancelJob records a cancel intent (terminal jobs are unchanged; an agent on another principal's batch
// gets NOT_BATCH_OWNER). STUB(M3-B6).
func (s *Service) CancelJob(ctx context.Context, p auth.Principal, id, reason string) (Job, error) {
	return Job{}, ErrNotImplemented
}

// CancelBatch records a cancel intent on every non-terminal job of a batch. STUB(M3-B6).
func (s *Service) CancelBatch(ctx context.Context, p auth.Principal, id, reason string) (Batch, error) {
	return Batch{}, ErrNotImplemented
}

// WaitResult is the outcome of Wait.
type WaitResult struct {
	Job               Job
	Changed, TimedOut bool
}
