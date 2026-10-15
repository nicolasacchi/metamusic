package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Trip reasons (C76.3).
const (
	TripStuckRunning        = "stuck_running"
	TripCrashLoop           = "crash_loop"
	TripMeterExceeds        = "meter_exceeds_reserved"
	TripProviderAccess      = "provider_access"
	TripSpendWithoutJobs    = "spend_without_jobs"
	TripBilledExceeds       = "billed_exceeds_reserved"
	TripProvisionalExceeds  = "provisional_exceeds_reserved"
	tripActionNotifyOnly    = "notified"
	tripActionCancelled     = "cancelled jobs"
	tripActionPurged        = "purged the queue"
	tripActionScaledToZero  = "scaled workers.max to 0"
	tripActionScaleFailed   = "scale to zero failed"
	tripActionManualConsole = "asked the operator to set workers.max to 0"
)

// TripActionOps maps the audit ops the reconciler writes while acting on a trip to the names
// get_provider_status lists (actions_json is frozen once the trip row exists, so the audit log is the
// record of what was done).
var TripActionOps = map[string]string{
	"reconciler:purge_queue":   "purge_queue",
	"reconciler:scale_to_zero": "scale_to_zero",
	"reconciler:trip":          "trip",
}

// tripCancelJobs moves every job with a live attempt towards cancelled with PROVIDER_TRIPPED (rows 5b, 11
// and 14) and sends the Cancel POSTs. A transferring job whose verified result already decided the take
// keeps it. It returns the number of jobs moved.
func (r *Runner) tripCancelJobs(ctx context.Context, reason string) (int, error) {
	js, err := queryJobs(ctx, r.o.Store.R, `state IN ('queued','submitting','starting','generating','transferring') ORDER BY created_at, take_index`)
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, c := range js {
		var toCancel []attRec
		err := r.tx(ctx, func(tx *sql.Tx) error {
			f := r.fence()
			j, err := loadJob(ctx, tx, c.ID)
			if err != nil {
				return err
			}
			atts, err := queryAtts(ctx, tx, `job_id = ? ORDER BY n`, j.ID)
			if err != nil {
				return err
			}
			live := liveAttempts(atts)
			if len(live) == 0 {
				return nil // nothing can spend: the job simply waits (no claim while a trip is open)
			}
			switch j.State {
			case StateTransferring:
				if kept, err := r.resultKept(ctx, tx, j.ID); err != nil || kept {
					return err
				}
			case StateSubmitting:
				if live[0].Ref == "" {
					return nil
				}
			case StateQueued, StateStarting, StateGenerating:
			default:
				return nil
			}
			if err := r.moveJob(ctx, tx, f, &j, StateCancelling, Event{Name: EvTrip, Code: CodeProviderTripped, Detail: map[string]any{"trip_reason": reason}},
				set("error_code", CodeProviderTripped), set("state_deadline_at", store.FormatTime(f.Now.Add(cancelUnconfirm)))); err != nil {
				return err
			}
			toCancel = live
			return nil
		})
		if err := skipStale(err); err != nil {
			return moved, err
		}
		if len(toCancel) > 0 {
			moved++
			if err := r.sendCancels(ctx, c.ID, toCancel); err != nil {
				return moved, err
			}
		}
	}
	return moved, nil
}

// tripFirst opens the trip in its own transaction (one open trip per backend).
func (r *Reconciler) tripFirst(ctx context.Context, reason string, details map[string]any) error {
	return r.o.Runner.tripProvider(ctx, reason, details)
}

type openTrip struct{ id, reason string }

func (r *Reconciler) openTrips(ctx context.Context) ([]openTrip, error) {
	rows, err := r.o.Store.R.QueryContext(ctx, `SELECT id, reason FROM provider_trips WHERE backend = ? AND reset_at IS NULL ORDER BY tripped_at`, r.o.Backend.Name())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []openTrip
	for rows.Next() {
		var t openTrip
		if err := rows.Scan(&t.id, &t.reason); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Trip opens a provider trip for reason and acts on it (C76.3).
func (r *Reconciler) Trip(ctx context.Context, reason string, details map[string]any) error {
	if err := r.tripFirst(ctx, reason, details); err != nil {
		return err
	}
	return r.actOnTrips(ctx)
}

// actOnTrips performs the trip actions for every open trip not yet actioned by this process: Cancel for
// every live attempt, PurgeQueue, ScaleToZero (or the manual fallback) and the ntfy. Every step is
// idempotent, so a restart that repeats them is harmless.
func (r *Reconciler) actOnTrips(ctx context.Context) error {
	trips, err := r.openTrips(ctx)
	if err != nil {
		return err
	}
	for _, t := range trips {
		r.mu.Lock()
		done := r.actioned[t.id]
		r.mu.Unlock()
		if done {
			continue
		}
		ok, err := r.act(ctx, t)
		if err != nil {
			return err
		}
		if ok {
			r.mu.Lock()
			r.actioned[t.id] = true
			r.mu.Unlock()
		}
	}
	return nil
}

func (r *Reconciler) act(ctx context.Context, t openTrip) (bool, error) {
	f := r.o.Runner.fence()
	if err := f.Confirm(ctx, r.o.Store.R, store.LeaseMargin); err != nil {
		if errors.Is(err, store.ErrLeaseShort) {
			return false, nil // the heartbeat renews first; the next pass acts
		}
		return false, err
	}
	var actions []string
	n, err := r.o.Runner.tripCancelJobs(ctx, t.reason)
	if err != nil {
		return false, err
	}
	if n > 0 {
		actions = append(actions, fmt.Sprintf("%s: %d", tripActionCancelled, n))
	}
	if err := r.o.Backend.PurgeQueue(ctx); err == nil {
		actions = append(actions, tripActionPurged)
		r.auditAfter(ctx, "reconciler:purge_queue", map[string]any{"trip": t.id}, false)
	} else {
		r.o.Log.Warn("trip: purge queue", "reason", t.reason, "err", redactErr(err))
	}
	manual := false
	if r.o.Backend.Capabilities().ScaleToZero {
		if err := r.o.Backend.ScaleToZero(ctx); err != nil {
			manual = true
			actions = append(actions, tripActionScaleFailed)
			r.auditAfter(ctx, "reconciler:scale_to_zero", map[string]any{"trip": t.id}, true)
		} else {
			actions = append(actions, tripActionScaledToZero)
			r.auditAfter(ctx, "reconciler:scale_to_zero", map[string]any{"trip": t.id}, false)
		}
	} else {
		manual = true
	}
	if manual {
		actions = append(actions, tripActionManualConsole)
		r.mu.Lock()
		var maxWorkers *int
		if r.endpoint != nil {
			maxWorkers = &r.endpoint.WorkersMax
		}
		live := 0
		if r.health != nil {
			live = r.health.Live()
		}
		for _, w := range r.workers {
			if workerLive(w.Status) {
				live = max(live, 1)
			}
		}
		note := NoteManualScaleToZero(r.o.ConsoleURL, maxWorkers, live)
		r.mu.Unlock()
		r.notify(ctx, note)
	}
	switch t.reason {
	case TripProviderAccess:
		r.notify(ctx, NoteProviderAccessLost(r.o.PublicURL))
	case TripMeterExceeds:
		r.notify(ctx, NoteMeterExceeds(r.o.PublicURL))
	}
	// actions is never empty: either the endpoint was scaled to zero or the operator was asked to do it
	r.notify(ctx, NoteProviderTripped(r.o.PublicURL, t.reason, strings.Join(actions, "; ")))
	r.auditAfter(ctx, "reconciler:trip", map[string]any{"trip": t.id, "reason": t.reason, "actions": actions}, false)
	return true, nil
}

func redactErr(err error) string { return redact.Error(err) }

// auditAfter writes a reconciler:* row for a network action after its effect, retrying inside the audit
// budget (the effect already happened; the row must not be lost to a busy database).
func (r *Reconciler) auditAfter(ctx context.Context, op string, details map[string]any, failed bool) {
	if r.o.Audit == nil {
		return
	}
	e, err := audit.SystemEntry(op, r.o.Clock.Now(), details, 0, 0, failed)
	if err != nil {
		return
	}
	audit.InsertRetrying(ctx, r.o.Audit, e, 2*time.Second, 10*time.Millisecond)
}
