package jobs

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Cancel timing (C75.4).
const (
	cancelReissue   = 30 * time.Second
	cancelUnconfirm = 5 * time.Minute
)

// phaseApprovals moves the jobs of decided batches: approved ones are queued (row 1), rejected and
// expired ones end (rows 2 and 3), and a cancel intent ends an awaiting job (row 2).
func (r *Runner) phaseApprovals(ctx context.Context, _ *StepReport) error {
	js, err := queryJobs(ctx, r.o.Store.R, `state = 'awaiting_confirmation' ORDER BY created_at, take_index`)
	if err != nil {
		return err
	}
	for _, c := range js {
		err := r.tx(ctx, func(tx *sql.Tx) error {
			f := r.fence()
			j, err := loadJob(ctx, tx, c.ID)
			if err != nil {
				return err
			}
			if j.State != StateAwaiting {
				return nil
			}
			b, err := loadBatch(ctx, tx, j.BatchID)
			if err != nil {
				return err
			}
			switch {
			case j.CancelRequestedAt != nil:
				return r.cancelUnleased(ctx, tx, f, &j, b)
			case b.State == "active":
				ts := store.FormatTime(f.Now)
				return r.moveJob(ctx, tx, f, &j, StateQueued, Event{Name: EvStatus, Code: "APPROVED"}, set("queued_at", ts), set("next_attempt_at", ts))
			case b.State == "rejected":
				return r.moveJob(ctx, tx, f, &j, StateCancelled, Event{Name: EvStatus, Code: CodeRejected}, set("error_code", CodeRejected))
			case b.State == "expired":
				return r.moveJob(ctx, tx, f, &j, StateExpired, Event{Name: EvStatus, Code: CodeApprovalExpired}, set("error_code", CodeApprovalExpired))
			}
			return nil
		})
		if err := skipStale(err); err != nil {
			return err
		}
	}
	return nil
}

// cancelUnleased ends an awaiting or queued job that has no live attempt (rows 2 and 5) and releases its
// remaining execution pot when nothing can have run.
func (r *Runner) cancelUnleased(ctx context.Context, tx *sql.Tx, f store.LeaseFence, j *jobRec, b batchRec) error {
	from := j.State
	_ = from
	if err := r.moveJob(ctx, tx, f, j, StateCancelled, Event{Name: EvCancelConfirmed, Code: CodeCancelledByUser, Detail: map[string]any{"basis": "no_attempt"}}, set("error_code", CodeCancelledByUser)); err != nil {
		return err
	}
	return r.releaseNeverRan(ctx, tx, f, j, b)
}

// phasePromote turns start announcements into refs (C74.3): a `pending` or ref-less `unknown` attempt gets
// ref = announced_ref and becomes `submitted`; a job still `queued` because of that ghost moves to
// `starting` (row 8b). An announcement that disagrees with the attempt's own ref is a provider requeue.
func (r *Runner) phasePromote(ctx context.Context, _ *StepReport) error {
	atts, err := queryAtts(ctx, r.o.Store.R, `announced_at IS NOT NULL AND state IN ('pending','unknown','submitted')`)
	if err != nil {
		return err
	}
	for _, c := range atts {
		err := r.tx(ctx, func(tx *sql.Tx) error { return r.promoteAttempt(ctx, tx, c.ID) })
		if err := skipStale(err); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) promoteAttempt(ctx context.Context, tx *sql.Tx, attemptID string) error {
	f := r.fence()
	as, err := queryAtts(ctx, tx, `id = ?`, attemptID)
	if err != nil || len(as) == 0 {
		return err
	}
	a := as[0]
	if a.AnnouncedAt == nil || a.AnnouncedRef == "" || !a.live() {
		return nil
	}
	j, err := loadJob(ctx, tx, a.JobID)
	if err != nil {
		return err
	}
	if IsTerminal(j.State) {
		return nil
	}
	if a.Ref != "" {
		if a.Ref == a.AnnouncedRef || a.RequeueSeen {
			return nil
		}
		// announced from a different provider job than /run returned: the provider requeued it elsewhere
		return r.onRequeue(ctx, tx, f, &j, a, "announced")
	}
	if err := r.setAttempt(ctx, tx, f, a.ID, "state = 'submitted', ref = ?", a.AnnouncedRef); err != nil {
		return err
	}
	r.mu.Lock()
	r.promoted[a.ID] = true
	r.mu.Unlock()
	switch j.State {
	case StateQueued:
		b, err := loadBatch(ctx, tx, j.BatchID)
		if err != nil {
			return err
		}
		_ = b
		return r.moveJob(ctx, tx, f, &j, StateStarting, Event{AttemptID: a.ID, Name: EvGhostStarted},
			set("state_deadline_at", store.FormatTime(f.Now.Add(r.o.TTL+startingQueueSlack))))
	case StateSubmitting:
		return r.moveJob(ctx, tx, f, &j, StateStarting, Event{AttemptID: a.ID, Name: EvGhostStarted},
			set("state_deadline_at", store.FormatTime(f.Now.Add(r.o.TTL+startingQueueSlack))))
	}
	return insertEvent(ctx, tx, f, Event{JobID: j.ID, AttemptID: a.ID, Name: EvGhostStarted})
}

// phaseCancel applies cancel intents to leased and queued jobs (rows 5, 5b, 11 and 14).
func (r *Runner) phaseCancel(ctx context.Context, _ *StepReport) error {
	js, err := queryJobs(ctx, r.o.Store.R, `cancel_requested_at IS NOT NULL AND state IN ('queued','submitting','starting','generating','transferring') ORDER BY created_at`)
	if err != nil {
		return err
	}
	for _, c := range js {
		var toCancel []attRec
		err := r.tx(ctx, func(tx *sql.Tx) error {
			f := r.fence()
			j, err := loadJob(ctx, tx, c.ID)
			if err != nil {
				return err
			}
			if j.CancelRequestedAt == nil || !(j.State == StateQueued || j.State == StateSubmitting || j.State == StateStarting || j.State == StateGenerating || j.State == StateTransferring) {
				return nil
			}
			b, err := loadBatch(ctx, tx, j.BatchID)
			if err != nil {
				return err
			}
			atts, err := queryAtts(ctx, tx, `job_id = ? ORDER BY n`, j.ID)
			if err != nil {
				return err
			}
			live := liveAttempts(atts)
			switch j.State {
			case StateQueued:
				if len(live) == 0 {
					return r.cancelUnleased(ctx, tx, f, &j, b)
				}
			case StateTransferring:
				kept, err := r.resultKept(ctx, tx, j.ID)
				if err != nil {
					return err
				}
				if kept {
					return nil // the take is kept (row 23 ran or a verified result arrived)
				}
			case StateSubmitting:
				if len(live) == 0 || live[0].Ref == "" {
					return nil // the submit outcome decides; a ref-less attempt cannot be cancelled yet
				}
			}
			if err := r.moveJob(ctx, tx, f, &j, StateCancelling, Event{Name: EvCancelRequested, Code: CodeCancelledByUser}, set("error_code", CodeCancelledByUser),
				set("state_deadline_at", store.FormatTime(f.Now.Add(cancelUnconfirm)))); err != nil {
				return err
			}
			toCancel = live
			return nil
		})
		if err := skipStale(err); err != nil {
			return err
		}
		if len(toCancel) > 0 {
			if err := r.sendCancels(ctx, c.ID, toCancel); err != nil {
				return err
			}
		}
	}
	return nil
}

// resultKept reports whether a verified result already decided the take: a result_received event or a
// cancel_lost_race event exists.
func (r *Runner) resultKept(ctx context.Context, x q, jobID string) (bool, error) {
	var n int
	err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event IN ('result_received','cancel_lost_race')`, jobID).Scan(&n)
	return n > 0, err
}

// sendCancels issues one Cancel POST per live attempt that has a ref and was not cancelled in the last
// 30 s (re-issued every 30 s while the provider does not show a terminal status). The POST only goes out
// with at least 30 s of lease left.
func (r *Runner) sendCancels(ctx context.Context, jobID string, atts []attRec) error {
	now := r.o.Clock.Now()
	var due []attRec
	r.mu.Lock()
	for _, a := range atts {
		if a.Ref == "" {
			continue
		}
		if last, ok := r.lastCancel[a.ID]; ok && now.Sub(last) < cancelReissue && !now.Before(last) {
			continue
		}
		due = append(due, a)
		if a.AnnouncedRef != "" && a.AnnouncedRef != a.Ref {
			// a requeue onto another provider job: both ids are live and both are cancelled
			second := a
			second.Ref = a.AnnouncedRef
			second.ID = a.ID + "#announced"
			due = append(due, second)
		}
	}
	r.mu.Unlock()
	if len(due) == 0 {
		return nil
	}
	f := r.fence()
	if err := f.Confirm(ctx, r.o.Store.R, store.LeaseMargin); err != nil {
		if errors.Is(err, store.ErrLeaseShort) {
			return nil
		}
		return err
	}
	sent, failed := 0, ""
	for _, a := range due {
		err := r.o.Backend.Cancel(ctx, provider.Ref{Backend: r.o.Backend.Name(), ID: a.Ref})
		r.mu.Lock()
		r.lastCancel[a.ID] = now
		r.mu.Unlock()
		switch {
		case err == nil:
			sent++
		case errors.Is(err, provider.ErrAccess), errors.Is(err, provider.ErrEndpointNotFound):
			failed = "access"
			if terr := r.tripProvider(ctx, "provider_access", map[string]any{"op": "cancel"}); terr != nil {
				return terr
			}
		default:
			failed = "error"
			r.o.Log.Warn("cancel", "job", jobID, "err", err.Error())
		}
		if ferr := r.fault(FaultAfterCancelPost); ferr != nil {
			return ferr
		}
	}
	d := map[string]any{"count": sent}
	if failed != "" {
		d["error"] = failed
	}
	err := r.tx(ctx, func(tx *sql.Tx) error {
		return insertEvent(ctx, tx, r.fence(), Event{JobID: jobID, Name: EvCancelSent, Detail: d})
	})
	if err != nil {
		return err
	}
	if r.o.Audit != nil && sent > 0 {
		if e, aerr := audit.SystemEntry("runner:cancel", now, map[string]any{"job": jobID, "count": sent}, 0, 0, false); aerr == nil {
			_ = r.o.Audit.Insert(ctx, e)
		}
	}
	return nil
}

// warnUnconfirmed sends the high-priority ntfy once when a cancel has gone unconfirmed for 5 minutes.
func (r *Runner) warnUnconfirmed(j jobRec) {
	r.mu.Lock()
	done := r.cancelWarned[j.ID]
	r.cancelWarned[j.ID] = true
	r.mu.Unlock()
	if done {
		return
	}
	r.queueNote(notify.Message{Title: "Cancel not confirmed yet", Priority: 4,
		Body: "The provider has not confirmed a cancel for five minutes.", Click: r.o.PublicURL + "/jobs/" + j.ID})
}
