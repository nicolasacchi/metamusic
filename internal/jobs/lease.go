package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Runner lease timing (C69): 90 s lease, renewed every 30 s by the heartbeat goroutine.
const (
	LeaseLength    = 90 * time.Second
	HeartbeatEvery = 30 * time.Second
)

// ErrNotHolder means another instance holds the runner lease; Step does nothing.
var ErrNotHolder = errors.New("jobs: another runner holds the lease")

// fence is the lease predicate carrier for a write that starts now.
func (r *Runner) fence() store.LeaseFence {
	return store.LeaseFence{Owner: r.o.Owner, Now: r.o.Clock.Now()}
}

// acquire takes or renews the runner lease (one statement; a returned row means acquired).
func (r *Runner) acquire(ctx context.Context) (bool, error) {
	now := r.o.Clock.Now()
	ts := store.FormatTime(now)
	exp := now.Add(LeaseLength)
	var owner string
	err := r.o.Store.W.QueryRowContext(ctx, `INSERT INTO runner_lease(id, owner, acquired_at, heartbeat_at, expires_at) VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET owner=excluded.owner, acquired_at=excluded.acquired_at,
		  heartbeat_at=excluded.heartbeat_at, expires_at=excluded.expires_at
		WHERE runner_lease.expires_at <= excluded.heartbeat_at OR runner_lease.owner = excluded.owner
		RETURNING owner`, r.o.Owner, ts, ts, store.FormatTime(exp)).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		r.setHeld(false, time.Time{})
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("jobs: acquire lease: %w", err)
	}
	r.setHeld(true, exp)
	return true, nil
}

func (r *Runner) setHeld(held bool, until time.Time) {
	r.mu.Lock()
	r.held, r.leaseUntil = held, until
	r.mu.Unlock()
}

// leaseExpiry is the expiry the runner lease was last renewed to; new job leases end with it.
func (r *Runner) leaseExpiry() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leaseUntil
}

// Holding reports whether the lease is held with at least 30 s left.
func (r *Runner) Holding() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.held && r.leaseUntil.Sub(r.o.Clock.Now()) >= store.LeaseMargin
}

// heartbeat renews the runner lease and the leases of every leased job. Zero rows means the lease is lost.
func (r *Runner) heartbeat(ctx context.Context) error {
	now := r.o.Clock.Now()
	exp := now.Add(LeaseLength)
	res, err := r.o.Store.W.ExecContext(ctx, `UPDATE runner_lease SET heartbeat_at=?, expires_at=? WHERE id=1 AND owner=? AND expires_at > ?`,
		store.FormatTime(now), store.FormatTime(exp), r.o.Owner, store.FormatTime(now))
	if err != nil {
		return fmt.Errorf("jobs: heartbeat: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		r.setHeld(false, time.Time{})
		return store.ErrLeaseLost
	}
	r.setHeld(true, exp)
	f := store.LeaseFence{Owner: r.o.Owner, Now: now}
	_, err = r.o.Store.W.ExecContext(ctx, `UPDATE jobs SET lease_expires_at=? WHERE lease_owner=? AND state NOT IN ('awaiting_confirmation','queued','completed','failed','cancelled','expired')`+f.And(), //nolint:gosec // G202: fixed C69 predicate
		append([]any{store.FormatTime(exp), r.o.Owner}, f.Args()...)...)
	if err != nil {
		return fmt.Errorf("jobs: renew job leases: %w", err)
	}
	return nil
}

// releaseLease gives the lease up on a graceful stop.
func (r *Runner) releaseLease(ctx context.Context) {
	now := store.FormatTime(r.o.Clock.Now())
	_, _ = r.o.Store.W.ExecContext(ctx, `UPDATE runner_lease SET expires_at=? WHERE id=1 AND owner=?`, now, r.o.Owner)
	r.setHeld(false, time.Time{})
}

// ensureLease acquires the lease at the start of a tick, or renews it when less than 60 s are left (the
// heartbeat goroutine of Run normally keeps it above that).
func (r *Runner) ensureLease(ctx context.Context) error {
	r.mu.Lock()
	fresh := r.held && r.leaseUntil.Sub(r.o.Clock.Now()) >= 2*store.LeaseMargin
	r.mu.Unlock()
	if fresh {
		return nil
	}
	ok, err := r.acquire(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotHolder
	}
	return nil
}

// adoptSQL is the C69 adoption statement without its lease predicate.
const adoptSQL = `UPDATE jobs SET lease_owner=?, lease_expires_at=?, updated_at=?
	WHERE state IN ('submitting','starting','generating','transferring','ingesting','cancelling','failing')
	  AND (lease_owner IS NULL OR lease_owner <> ?) AND (lease_expires_at IS NULL OR lease_expires_at <= ?)`

// adopt takes over the leased jobs of a runner whose lease expired (C69 adoption SQL) and repairs what a
// crash between claim and submit outcome left: a `pending` attempt (the /run may have gone out) becomes an
// `unknown` ghost and its job goes back to the queue behind an ambiguous top-up (C76.1, row 9).
func (r *Runner) adopt(ctx context.Context, rep *StepReport) error {
	_, err := r.adoptJobs(ctx)
	return err
}

// Adopted is one job taken over from a dead runner.
type Adopted struct {
	JobID  string
	Ghosts int
}

// adoptJobs is adopt returning what it took over (the reconciler audits it, C76.1).
func (r *Runner) adoptJobs(ctx context.Context) ([]Adopted, error) {
	f := r.fence()
	exp := r.leaseExpiry()
	var adopted []string
	err := r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
		q := adoptSQL + f.And() + ` RETURNING id` //nolint:gosec // G202: fixed C69 predicate
		rows, err := tx.QueryContext(ctx, q,
			append([]any{r.o.Owner, store.FormatTime(exp), store.FormatTime(f.Now), r.o.Owner, store.FormatTime(f.Now)}, f.Args()...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			adopted = append(adopted, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: adopt: %w", err)
	}
	var out []Adopted
	for _, id := range adopted {
		g, err := r.adoptJob(ctx, id)
		if err != nil {
			return out, err
		}
		out = append(out, Adopted{JobID: id, Ghosts: g})
	}
	return out, nil
}

func (r *Runner) adoptJob(ctx context.Context, id string) (ghosts int, err error) {
	err = r.o.Spend.RunTx(ctx, func(tx *sql.Tx) error {
		f := r.fence()
		j, err := loadJob(ctx, tx, id)
		if err != nil {
			return err
		}
		atts, err := queryAtts(ctx, tx, `job_id = ? ORDER BY n`, id)
		if err != nil {
			return err
		}
		ghosts = 0
		for _, a := range atts {
			if a.State != "pending" {
				continue
			}
			if err := r.setAttempt(ctx, tx, f, a.ID, "state = 'unknown'"); err != nil {
				return err
			}
			ghosts++
			if err := insertEvent(ctx, tx, f, Event{JobID: id, AttemptID: a.ID, Name: EvGhost}); err != nil {
				return err
			}
		}
		if err := insertEvent(ctx, tx, f, Event{JobID: id, From: j.State, To: j.State, Name: EvAdopted, Detail: map[string]any{"attempts": len(atts), "ghosts": ghosts}}); err != nil {
			return err
		}
		if j.State != StateSubmitting {
			return nil
		}
		// Crash before the submit outcome: no ref was persisted. Back through row 9 behind an ambiguous top-up.
		b, err := loadBatch(ctx, tx, j.BatchID)
		if err != nil {
			return err
		}
		return r.requeueAfterSubmit(ctx, tx, f, &j, b, nil, "adopted")
	})
	return ghosts, err
}

// setAttempt updates observation columns of a live attempt under the lease predicate. set is a fixed SQL
// assignment list written by this package.
func (r *Runner) setAttempt(ctx context.Context, tx *sql.Tx, f store.LeaseFence, id, set string, args ...any) error {
	res, err := tx.ExecContext(ctx, `UPDATE job_attempts SET `+set+` WHERE id = ?`+f.And(), //nolint:gosec // G202: set is a constant assignment list, values bound
		append(append(args, id), f.Args()...)...)
	n, err := f.Fenced(res, err)
	if err != nil {
		return fmt.Errorf("jobs: update attempt: %w", err)
	}
	if n == 0 {
		if cerr := f.Confirm(ctx, tx, 0); cerr != nil {
			return cerr
		}
		return errStale
	}
	return nil
}

// errStale: the row changed under us (another writer, or an earlier statement of the same tick); the
// caller re-reads and re-plans.
var errStale = errors.New("jobs: row changed concurrently")
