package fallback

import (
	"context"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The audit operations of this package (registered once; the actor prefix names who acts).
const (
	OpDelete = "fallback:object-delete"
	OpDrill  = "cli:upload-drill"
)

func init() { audit.AddSystemOps(OpDelete, OpDrill) }

// DeleteEvery is how often the deletion loop runs; RescueEvery how often it looks for results the app has not received.
const (
	DeleteEvery = 5 * time.Minute
	RescueEvery = 30 * time.Second
)

// maxDeletesPerPass bounds one pass so a large backlog never holds the loop for long.
const maxDeletesPerPass = 200

type pending struct {
	uploadID, attemptID, mintedAt, attState, jobState, upState string
}

// terminalJob reports whether a job state is final.
func terminalJob(s string) bool {
	switch s {
	case "completed", "failed", "cancelled", "expired":
		return true
	}
	return false
}

// reason says why an object may go, or "" when it must stay: its attempt is terminal and nothing can still need the object (the job
// has ended, or the upload was received), or it is older than the retention. A live or unknown attempt is never deleted, and neither
// is an object of a finished attempt whose upload is still open on a job that waits for it.
func (p pending) reason(now time.Time) string {
	if t, err := store.ParseTime(p.mintedAt); err != nil || !now.Before(t.Add(Retention)) {
		return "expired" // an unparsable stamp is treated as old: never keep an object for ever
	}
	if (p.attState == "done" || p.attState == "rejected") && (terminalJob(p.jobState) || p.upState != "open") {
		return "terminal"
	}
	return ""
}

// DeleteDue deletes the bucket objects that may go (see pending.reason), marks each row deleted and writes one audit row per object, in
// one transaction. It is idempotent: a delete that already happened (404) is success, a failed delete leaves the row for the next pass,
// and a row already marked is never selected. It returns how many objects were deleted by this call.
func (f *Fallback) DeleteDue(ctx context.Context) (int, error) {
	rows, err := f.o.Store.R.QueryContext(ctx, `SELECT o.upload_id, o.attempt_id, o.minted_at, a.state, j.state, u.state
		FROM fallback_objects o JOIN uploads u ON u.id = o.upload_id JOIN job_attempts a ON a.id = o.attempt_id JOIN jobs j ON j.id = a.job_id
		WHERE o.deleted_at IS NULL ORDER BY o.minted_at, o.upload_id`)
	if err != nil {
		return 0, fmt.Errorf("fallback: %w", err)
	}
	now := f.o.Clock.Now()
	var due []struct {
		p      pending
		reason string
	}
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.uploadID, &p.attemptID, &p.mintedAt, &p.attState, &p.jobState, &p.upState); err != nil {
			rows.Close()
			return 0, err
		}
		if r := p.reason(now); r != "" && len(due) < maxDeletesPerPass {
			due = append(due, struct {
				p      pending
				reason string
			}{p, r})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	deleted := 0
	for _, d := range due {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if err := f.o.Objects.Delete(ctx, f.key(d.p.uploadID)); err != nil {
			f.o.Log.Warn("fallback: object delete failed; retrying next pass", "err", redact.Error(err))
			continue
		}
		if err := f.markDeleted(ctx, d.p, d.reason); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func (f *Fallback) markDeleted(ctx context.Context, p pending, reason string) error {
	now := f.o.Clock.Now()
	tx, err := f.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fallback: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed
	res, err := tx.ExecContext(ctx, `UPDATE fallback_objects SET deleted_at = ? WHERE upload_id = ? AND deleted_at IS NULL`, store.FormatTime(now), p.uploadID)
	if err != nil {
		return fmt.Errorf("fallback: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // another pass marked it first: nothing more to record
	}
	e, err := audit.SystemEntry(OpDelete, now, map[string]any{"upload_id": p.uploadID, "attempt_id": p.attemptID, "reason": reason}, 0, 0, false)
	if err != nil {
		return err
	}
	if err := audit.InsertTx(ctx, tx, e); err != nil {
		return fmt.Errorf("fallback: audit: %w", err)
	}
	return tx.Commit()
}

// RescueSweep runs Rescue for every live attempt that has bucket objects; a fault on one attempt is logged and does not stop the rest.
func (f *Fallback) RescueSweep(ctx context.Context) (found int) {
	ids, err := f.LiveAttempts(ctx)
	if err != nil {
		f.o.Log.Warn("fallback: listing attempts failed", "err", redact.Error(err))
		return 0
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return found
		}
		ok, err := f.Rescue(ctx, id)
		if err != nil {
			f.o.Log.Warn("fallback: rescue failed", "err", redact.Error(err))
			continue
		}
		if ok {
			found++
			f.o.Log.Info("fallback: result completed from the bucket")
		}
	}
	return found
}

// Run is the background loop: a rescue sweep and a deletion pass at startup, then a sweep every RescueEvery and a deletion pass every
// DeleteEvery, until ctx ends.
func (f *Fallback) Run(ctx context.Context) {
	f.RescueSweep(ctx)
	f.deletePass(ctx)
	sweeps := 0
	for {
		t := f.o.Clock.NewTimer(RescueEvery)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		f.RescueSweep(ctx)
		if sweeps++; sweeps%int(DeleteEvery/RescueEvery) == 0 {
			f.deletePass(ctx)
		}
	}
}

func (f *Fallback) deletePass(ctx context.Context) {
	if n, err := f.DeleteDue(ctx); err != nil && ctx.Err() == nil {
		f.o.Log.Warn("fallback: deletion pass failed", "err", redact.Error(err))
	} else if n > 0 {
		f.o.Log.Info("fallback: objects deleted", "count", n)
	}
}
