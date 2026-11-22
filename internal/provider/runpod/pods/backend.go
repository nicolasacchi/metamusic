package pods

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// namePrefix is the prefix of every pod this server creates (C390.1).
const namePrefix = "mm-"

// Codes of a pod row that end a batch's pod supply: a refused overhead reserve (C396.3), an insufficient balance (C392.3) and a batch past
// its window. A take submitted after one of them could never be handed out.
var supplyEndedCodes = []string{"BUDGET_EXCEEDED", "BALANCE", "POD_DEADLINE"}

// SubmitError is a refusal of Submit that never reached a pod. Code "BAD_REQUEST" is read by the runner (the errShape contract of
// C77.4) and ends the attempt SUBMIT_REJECTED without charge: nothing ran.
type SubmitError struct {
	Code   string
	Reason string // a fixed phrase; never a URL, an id or a value from the take
}

func (e *SubmitError) Error() string { return "pods: submit refused: " + e.Reason }

func refuse(reason string) error { return &SubmitError{Code: "BAD_REQUEST", Reason: reason} }

// BackendOptions builds the pod backend.
type BackendOptions struct {
	// Name is the backend name: provider.BackendRunPodPod (default) or provider.BackendFakePod.
	Name  string
	API   API
	Store *store.Store
	// Queue is the hand-out queue shared with the poll handler (NewPoller); nil makes a private one.
	Queue *Queue
	Clock clock.Clock
	Log   *slog.Logger
	// Image is MM_POD_IMAGE (<ref>@sha256:<64 hex>); its digest is the quote's ImageDigest.
	Image string
	// GPUTypes is MM_POD_GPU_TYPES, in order; the quote prices the dearest.
	GPUTypes []string
	// MaxRuntime is MM_JOB_MAX_RUNTIME: the cap of one take's timeout_s.
	MaxRuntime time.Duration
	// KillSlackS is K, the worker's kill slack, in seconds.
	KillSlackS int
	// KeyPresent reports whether the pod key file is in place now (read per call, never cached); nil means absent.
	KeyPresent func() bool
	// BillingReadable overrides the probe lookup: whether the probe found billing readable with the pod key; nil reads the latest probe row of pod_safety_log.
	BillingReadable func(ctx context.Context) bool
}

// Backend is the pod backend (C396.1). It implements provider.Backend over the pod tables and the shared hand-out queue: Submit queues a
// take for a pod to fetch over the poll route, Poll reads the assignment row, and the pods themselves are created, watched and deleted by
// the lifecycle loop, not here. Safe for concurrent use.
type Backend struct {
	o       BackendOptions
	started time.Time // takes queued before this moment and not in memory were lost by a restart (D-BULK-5)
}

var (
	_ provider.Backend     = (*Backend)(nil)
	_ provider.ShapePinner = (*Backend)(nil)
)

// NewBackend builds the backend. It makes no network call.
func NewBackend(o BackendOptions) *Backend {
	if o.Name == "" {
		o.Name = provider.BackendRunPodPod
	}
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.Queue == nil {
		o.Queue = NewQueue()
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Backend{o: o, started: o.Clock.Now()}
}

// Queue is the hand-out queue the backend fills; the poll handler and the lifecycle loop share it.
func (b *Backend) Queue() *Queue { return b.o.Queue }

// Name implements provider.Backend.
func (b *Backend) Name() string { return b.o.Name }

// ColdStartS is 0: a pod is created for the batch and boots outside any job's timeout (D-BULK-2).
func (b *Backend) ColdStartS() int { return 0 }

// Engine is yue2.
func (b *Backend) Engine() string { return provider.EngineYuE2 }

// MaxRuntime is the cap of one take's timeout_s.
func (b *Backend) MaxRuntime() time.Duration { return b.o.MaxRuntime }

// CancelledWhileQueuedIsNeverRan reports that a take cancelled before any pod fetched it never ran, whatever the serverless probe
// pinned (provider.ShapePinner).
func (b *Backend) CancelledWhileQueuedIsNeverRan() bool { return true }

func (b *Backend) keyPresent() bool { return b.o.KeyPresent != nil && b.o.KeyPresent() }

// Capabilities implements provider.Backend: scale to zero needs the key; billing needs the key and a probe that found it readable; a pod has no drift to revert.
func (b *Backend) Capabilities() provider.Capabilities {
	c := provider.Capabilities{Billing: "none"}
	if !b.keyPresent() {
		return c
	}
	c.ScaleToZero = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if b.billingReadable(ctx) {
		c.Billing = "hour"
	}
	return c
}

// billingReadable: the latest probe row says the pod key reads billing (detail_json.billing_readable, written by `runpod probe --pods`).
func (b *Backend) billingReadable(ctx context.Context) bool {
	if b.o.BillingReadable != nil {
		return b.o.BillingReadable(ctx)
	}
	if b.o.Store == nil {
		return false
	}
	var ok sql.NullInt64
	err := b.o.Store.R.QueryRowContext(ctx, `SELECT json_extract(detail_json, '$.billing_readable') FROM pod_safety_log
		WHERE kind = 'probe' AND verdict IN ('ok','too_broad') ORDER BY id DESC LIMIT 1`).Scan(&ok)
	return err == nil && ok.Valid && ok.Int64 == 1
}

// batchOf is the batch of an attempt.
func (b *Backend) batchOf(ctx context.Context, attemptID string) (string, error) {
	var batch string
	err := b.o.Store.R.QueryRowContext(ctx, `SELECT j.batch_id FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE a.id = ?`, attemptID).Scan(&batch)
	return batch, err
}

// Submit implements provider.Backend: it records the assignment, keeps the TakeSpec (with its URLs) in memory and wakes pollers. It
// creates no pod and spends nothing; a take that no pod could ever be handed is refused BAD_REQUEST (never ran, SUBMIT_REJECTED) rather than
// queued: the batch's deadline is too near, its one re-creation is spent, or its pod overhead was refused.
func (b *Backend) Submit(ctx context.Context, t provider.TakeSpec) (provider.Ref, error) {
	switch {
	case t.AttemptID == "" || t.TimeoutS <= 0:
		return provider.Ref{}, refuse("take has no attempt or timeout")
	case t.Engine != provider.EngineYuE2:
		return provider.Ref{}, refuse("the pod backend renders yue2 only")
	case len(t.Inputs) > 0:
		return provider.Ref{}, refuse("the pod backend takes no fetched inputs")
	}
	if _, err := provider.BuildWorkerInput(t); err != nil {
		return provider.Ref{}, refuse("the take does not build a worker input")
	}
	batch, err := b.batchOf(ctx, t.AttemptID)
	if err != nil {
		return provider.Ref{}, err
	}
	if reason, err := b.supplyRefusal(ctx, batch, t); err != nil || reason != "" {
		if err != nil {
			return provider.Ref{}, err
		}
		return provider.Ref{}, refuse(reason)
	}
	if err := QueueAssignment(ctx, b.o.Store.W, t.AttemptID, batch, b.o.Clock.Now()); err != nil {
		return provider.Ref{}, err
	}
	b.o.Queue.Add(batch, t)
	return provider.Ref{Backend: b.o.Name, ID: t.AttemptID}, nil
}

// supplyRefusal says why no pod of the batch could take t ("" when one might).
func (b *Backend) supplyRefusal(ctx context.Context, batch string, t provider.TakeSpec) (string, error) {
	var deadline sql.NullString
	var spent, ended int
	// The supply-ended codes count over every pod of the batch, a canary's too (its verdict ends the one-take batch, BULK-P5); the deadline
	// and the re-creation are the batch pods' alone.
	err := b.o.Store.R.QueryRowContext(ctx, `SELECT MIN(CASE WHEN kind = 'batch' THEN deadline_at END),
			COALESCE(SUM(kind = 'batch' AND n = 2 AND state IN ('orphaned','terminating','terminated')), 0),
			COALESCE(SUM(error_code IN (`+placeholders(len(supplyEndedCodes))+`)), 0)
		FROM pods WHERE batch_id = ?`, append(anyStrings(supplyEndedCodes), batch)...).Scan(&deadline, &spent, &ended)
	if err != nil {
		return "", err
	}
	switch {
	case ended > 0:
		return "the batch's pod supply ended", nil
	case spent > 0:
		return "the batch used its pod re-creation", nil
	case deadline.Valid:
		d, err := store.ParseTime(deadline.String)
		if err != nil {
			return "", err
		}
		if b.o.Clock.Now().Add(time.Duration(t.TimeoutS+b.o.KillSlackS+handoutMarginS) * time.Second).After(d) {
			return "the batch deadline is too near for this take", nil
		}
	}
	return "", nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anyStrings(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// Poll implements provider.Backend from the assignment row and the in-memory queue only; it never calls RunPod.
func (b *Backend) Poll(ctx context.Context, r provider.Ref) (provider.RemoteStatus, error) {
	a, err := GetAssignment(ctx, b.o.Store.R, r.ID)
	if errors.Is(err, ErrNotFound) {
		return provider.RemoteStatus{State: provider.NotFound, Raw: "NOT_FOUND"}, nil
	}
	if err != nil {
		return provider.RemoteStatus{}, err
	}
	switch a.Status {
	case "":
	case AssignOK, AssignError:
		st := provider.Completed
		if a.Status == AssignError {
			st = provider.Failed
		}
		return provider.RemoteStatus{State: st, Raw: strings.ToUpper(a.Status), ExecutionMS: a.ExecutionMS, ErrorText: a.ErrorCode}, nil
	case AssignLost:
		return provider.RemoteStatus{State: provider.Failed, Raw: "LOST", ErrorText: a.ErrorCode}, nil // no execution time: settled at its time bound
	default: // not_handed, cancelled: never reached a pod
		return provider.RemoteStatus{State: provider.Cancelled, Raw: strings.ToUpper(a.Status), ErrorText: a.ErrorCode}, nil
	}
	if a.PodID != "" {
		return provider.RemoteStatus{State: provider.Running, Raw: "RUNNING"}, nil
	}
	// Unhanded. Queued in this process, or lost by a restart: the take (with its URLs) lived in memory only (D-BULK-5).
	if a.QueuedAt.Before(b.started) && !b.o.Queue.Has(a.AttemptID) {
		now := b.o.Clock.Now()
		if ok, err := CloseAssignment(ctx, b.o.Store.W, a.AttemptID, AssignNotHanded, "RESTARTED", now); err != nil {
			return provider.RemoteStatus{}, err
		} else if ok {
			return provider.RemoteStatus{State: provider.Cancelled, Raw: "NOT_HANDED", ErrorText: "RESTARTED"}, nil
		}
		return b.Poll(ctx, r) // handed or closed in the meantime: read it again
	}
	return provider.RemoteStatus{State: provider.Queued, Raw: "QUEUED"}, nil
}

// Cancel implements provider.Backend: an unhanded take is dropped and its assignment closed `cancelled`; a handed one is listed in its
// pod's next poll answer. A finished or unknown assignment is already gone, which is what the caller wants.
func (b *Backend) Cancel(ctx context.Context, r provider.Ref) error {
	for range 2 {
		a, err := GetAssignment(ctx, b.o.Store.R, r.ID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if a.Status != "" {
			return nil
		}
		if a.PodID != "" {
			b.o.Queue.RequestCancel(a.AttemptID)
			return nil
		}
		b.o.Queue.Drop(a.AttemptID)
		ok, err := CloseAssignment(ctx, b.o.Store.W, a.AttemptID, AssignCancelled, "", b.o.Clock.Now())
		if err != nil || ok {
			return err
		}
		// handed between the read and the close: read the row again and list the cancel
	}
	b.o.Queue.RequestCancel(r.ID)
	return nil
}

// PurgeQueue implements provider.Backend: every unhanded take is dropped and its assignment closed `cancelled`.
func (b *Backend) PurgeQueue(ctx context.Context) error {
	var errs []error
	for _, id := range b.o.Queue.Purge() {
		if _, err := CloseAssignment(ctx, b.o.Store.W, id, AssignCancelled, "", b.o.Clock.Now()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Sweep implements provider.Backend: a pod backend has no workers and no endpoint, so the M3 sweep trips stay silent; pod trips come
// from the lifecycle loop (C396.4).
func (b *Backend) Sweep(context.Context) (provider.SweepReport, error) {
	return provider.SweepReport{ObservedAt: b.o.Clock.Now()}, nil
}

// ScaleToZero implements provider.Backend: DELETE every live mm-* pod (ErrNotPermitted without the key). Every pod is tried; the errors
// are joined.
func (b *Backend) ScaleToZero(ctx context.Context) error {
	if b.o.API == nil || !b.keyPresent() {
		return provider.ErrNotPermitted
	}
	ps, err := b.o.API.ListPods(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range ps {
		if !strings.HasPrefix(p.Name, namePrefix) || p.Status == StatusTerminated {
			continue
		}
		if err := b.o.API.DeletePod(ctx, p.ID); err != nil {
			errs = append(errs, errors.New(redact.Error(err)))
		}
	}
	return errors.Join(errs...)
}

// RevertDrift implements provider.Backend: a pod has no endpoint to drift.
func (b *Backend) RevertDrift(context.Context, []provider.Drift) error {
	return provider.ErrNotPermitted
}

// Billing implements provider.Backend: hourly buckets of this server's own pods, when the probe found billing readable.
func (b *Backend) Billing(ctx context.Context, from, to time.Time) ([]provider.BillBucket, error) {
	if b.o.API == nil || !b.keyPresent() || !b.billingReadable(ctx) {
		return nil, provider.ErrNotPermitted
	}
	refs, err := b.ownRefs(ctx, from, to)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}
	return b.o.API.PodBilling(ctx, from, to, refs)
}

// ownRefs are the RunPod ids of the pods that existed in [from, to]. Billing is account-wide; only these are ever summed.
func (b *Backend) ownRefs(ctx context.Context, from, to time.Time) ([]string, error) {
	rows, err := b.o.Store.R.QueryContext(ctx, `SELECT provider_ref FROM pods WHERE provider_ref IS NOT NULL AND created_at <= ?
		AND (terminated_at IS NULL OR terminated_at >= ?) ORDER BY created_at`, store.FormatTime(to), store.FormatTime(from))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}
