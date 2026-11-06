package transform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The runner (C210.2, D-T1-17): the L2 tape pattern. One loop on Wake or a 5 s tick claims the oldest queued row under a row
// lease (owner, lease_expires_at on the row itself; no shared singleton lease, so a deployment with generation disabled runs
// transforms unchanged), runs the attempt, renews the lease every LeaseTTL/6 while it runs, and repeats `owner = ?` in every
// write of the running row. A renew that affects no row means the row is no longer ours: the child is killed and nothing is
// written. A shutdown leaves the row running; Recover re-queues it once its lease expires.

const (
	idleEvery    = 5 * time.Second
	cancelPoll   = CancelGrace / 4 // a cancel is noticed well inside CancelGrace
	maxErrorText = 500
)

// pathRE matches a path of two or more segments, so no stored message names a file even when a tool prints a relative or
// foreign one.
var pathRE = regexp.MustCompile(`(?:/[A-Za-z0-9._@%+=:~-]+){2,}/?`)

var (
	errLeaseLost = errors.New("transform: lease lost")
	errCancelled = errors.New("transform: cancel requested")
)

// runState is the runner's private state; zero value ready.
type runState struct {
	once sync.Once
	wake chan struct{}
}

func (s *Service) wakeCh() chan struct{} {
	s.run.once.Do(func() { s.run.wake = make(chan struct{}, 1) })
	return s.run.wake
}

// Kick nudges the runner (capacity one: a pending nudge is enough). It is what the app wires into Options.Wake.
func (s *Service) Kick() {
	if s == nil {
		return
	}
	select {
	case s.wakeCh() <- struct{}{}:
	default:
	}
}

func (s *Service) owner() string {
	if s.o.Owner != "" {
		return s.o.Owner
	}
	return "transform-runner"
}

func (s *Service) log() *slog.Logger {
	if s.o.Log != nil {
		return s.o.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (s *Service) changed(id string) {
	if s.o.Changed != nil {
		s.o.Changed(id)
	}
}

// Runnable reports whether the service has what an attempt needs: the data root, the M1 pipeline and the library. Without them
// (no ffmpeg) the runner idles and create answers 503.
func (s *Service) Runnable() bool { return s.runnable() }

// runnable reports whether the service has what an attempt needs.
func (s *Service) runnable() bool {
	return s != nil && s.o.Store != nil && s.o.Data != nil && s.o.Pipeline != nil && s.o.Library != nil
}

// tRow is a claimed transform as an attempt sees it.
type tRow struct {
	ID, RecipeID, SpecHash, Title, PrincipalID, PrincipalKind, PrincipalName string
	Kind                                                                     Kind
	Rationale                                                                sql.NullString
	Attempts, MemMBEst                                                       int
	ExpectedCPUMS, MaxCPUMS                                                  int64
	OutSecondsEst                                                            float64
	CancelRequestedAt, StartedAt                                             sql.NullString
}

const rowCols = `id, kind, recipe_id, spec_hash, title, rationale, principal_id, principal_kind, principal_name, attempts,
	expected_cpu_ms, max_cpu_ms, out_seconds_est, mem_mb_est, cancel_requested_at, started_at`

// scan reads rowCols (and then extra destinations the query appended).
func (r *tRow) scan(sc interface{ Scan(...any) error }, extra ...any) error {
	var k string
	dest := append([]any{&r.ID, &k, &r.RecipeID, &r.SpecHash, &r.Title, &r.Rationale, &r.PrincipalID, &r.PrincipalKind, &r.PrincipalName,
		&r.Attempts, &r.ExpectedCPUMS, &r.MaxCPUMS, &r.OutSecondsEst, &r.MemMBEst, &r.CancelRequestedAt, &r.StartedAt}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return err
	}
	r.Kind = Kind(k)
	return nil
}

// Run is the runner loop (serve only): recover and sweep once, then claim and run queued transforms one at a time until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if !s.runnable() {
		<-ctx.Done()
		return
	}
	wake := s.wakeCh()
	if err := s.Recover(ctx); err != nil && ctx.Err() == nil {
		s.log().Error("transform recover", "err", redact.Error(err))
	}
	if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
		s.log().Error("transform sweep", "err", redact.Error(err))
	}
	for ctx.Err() == nil {
		ran, err := s.RunOne(ctx)
		if err != nil && ctx.Err() == nil {
			s.log().Error("transform runner", "err", redact.Error(err))
		}
		if ran && err == nil {
			continue
		}
		if err := s.Recover(ctx); err != nil && ctx.Err() == nil {
			s.log().Error("transform recover", "err", redact.Error(err))
		}
		if err := s.sweepWork(ctx); err != nil && ctx.Err() == nil {
			s.log().Error("transform sweep", "err", redact.Error(err))
		}
		t := time.NewTimer(idleEvery)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// RunOne claims and runs one queued transform; false means there was none (tests drive the runner with it).
func (s *Service) RunOne(ctx context.Context) (bool, error) {
	if !s.runnable() {
		return false, nil
	}
	row, ok, err := s.claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	s.changed(row.ID)
	s.process(ctx, row)
	return true, ctx.Err()
}

// claim is ClaimNextTransform (C201.3): the oldest queued row becomes running with a lease, unless ConcurrentRenders rows
// already run. The statement is one atomic UPDATE on the single writer connection.
func (s *Service) claim(ctx context.Context) (tRow, bool, error) {
	now := s.o.Now()
	var r tRow
	err := r.scan(s.o.Store.W.QueryRowContext(ctx, `UPDATE transforms SET state = 'running', attempts = attempts + 1, started_at = ?, phase = 'preparing',
		progress_permille = 0, owner = ?, lease_expires_at = ?
		WHERE id = (SELECT id FROM transforms WHERE state = 'queued' ORDER BY created_at, id LIMIT 1)
		  AND (SELECT COUNT(*) FROM transforms WHERE state = 'running') < ?
		RETURNING `+rowCols, store.FormatTime(now), s.owner(), store.FormatTime(now.Add(LeaseTTL)), ConcurrentRenders))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return tRow{}, false, nil
	case err != nil:
		return tRow{}, false, fmt.Errorf("transform: claim: %w", err)
	}
	return r, true, nil
}

// renew extends the lease of a running row; false means the row is no longer ours.
func (s *Service) renew(ctx context.Context, id string) bool {
	res, err := s.o.Store.W.ExecContext(ctx, `UPDATE transforms SET lease_expires_at = ? WHERE id = ? AND state = 'running' AND owner = ?`,
		store.FormatTime(s.o.Now().Add(LeaseTTL)), id, s.owner())
	if err != nil {
		return ctx.Err() != nil // a failed write is retried at the next tick
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// cancelRequested reads the intent flag of a running row.
func (s *Service) cancelRequested(ctx context.Context, id string) bool {
	var at sql.NullString
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT cancel_requested_at FROM transforms WHERE id = ?`, id).Scan(&at); err != nil {
		return false
	}
	return at.Valid
}

var phases = map[string]bool{"preparing": true, "analyzing": true, "rendering": true, "finishing": true, "encoding": true, "ingesting": true}

// setProgress writes the phase and progress of a running row under the owner predicate. A write that affects no row is
// ignored here: the renew tick finds out that the lease is gone.
func (s *Service) setProgress(ctx context.Context, id string, permille int, phase string) {
	if !phases[phase] {
		phase = "rendering"
	}
	_, _ = s.o.Store.W.ExecContext(ctx, `UPDATE transforms SET progress_permille = ?, phase = ? WHERE id = ? AND state = 'running' AND owner = ?`,
		min(max(permille, 0), 1000), phase, id, s.owner())
	s.changed(id)
}

// setPhase moves a running row to a phase without touching progress.
func (s *Service) setPhase(ctx context.Context, id, phase string) {
	_, _ = s.o.Store.W.ExecContext(ctx, `UPDATE transforms SET phase = ? WHERE id = ? AND state = 'running' AND owner = ?`, phase, id, s.owner())
	s.changed(id)
}

// runFailure is why an attempt ended: a stable code and a message with ids only.
type runFailure struct {
	code, msg string
	crash     bool // an injected fault: end the attempt with no cleanup and no row write, as a dead process would
	lost      bool // the row is no longer ours: write nothing
	requeue   bool // an interruption from outside (a signal): back to the queue, or INTERRUPTED once MaxAttempts is spent
}

func (f *runFailure) Error() string { return f.code + ": " + f.msg }

// clean makes a stored message safe: redacted, the data root masked, no newline, at most maxErrorText runes.
func (s *Service) clean(msg string) string {
	if s.o.DataDir != "" {
		// everything below the data root goes with it: the layout (incoming/<id>/out.flac, audio/masters/...) is not for storage
		msg = regexp.MustCompile(regexp.QuoteMeta(s.o.DataDir)+`[^\s"'`+"`"+`:;,()<>]*`).ReplaceAllString(msg, "<data>")
	}
	if s.o.Self != "" {
		msg = strings.ReplaceAll(msg, s.o.Self, "<binary>")
	}
	msg = pathRE.ReplaceAllString(redact.String(msg), "<path>")
	msg = strings.Join(strings.Fields(msg), " ")
	if r := []rune(msg); len(r) > maxErrorText {
		msg = string(r[:maxErrorText])
	}
	return msg
}

// process runs one claimed row to a terminal state, or leaves it for Recover on shutdown.
func (s *Service) process(ctx context.Context, row tRow) {
	rctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // lease renewal and the cancel poll
		defer wg.Done()
		renew := time.NewTicker(LeaseTTL / 6)
		poll := time.NewTicker(cancelPoll)
		defer renew.Stop()
		defer poll.Stop()
		for {
			select {
			case <-stop:
				return
			case <-rctx.Done():
				return
			case <-renew.C:
				if !s.renew(rctx, row.ID) {
					s.log().Warn("transform lease lost", "transform_id", row.ID)
					cancel(errLeaseLost)
					return
				}
			case <-poll.C:
				if s.cancelRequested(rctx, row.ID) {
					cancel(errCancelled)
					return
				}
			}
		}
	}()
	var acct cpuAcct
	fail := s.attempt(rctx, row, &acct)
	close(stop)
	wg.Wait()
	if fail == nil {
		return // finished: the bridge committed the row
	}
	fin := context.WithoutCancel(ctx)
	switch cause := context.Cause(rctx); {
	case fail.crash:
		// an injected crash: nothing is cleaned and nothing written
	case fail.lost, errors.Is(cause, errLeaseLost):
		s.removeWork(row.ID) // nothing is written: the row is not ours
	case errors.Is(cause, errCancelled):
		s.removeWork(row.ID)
		s.finishTerminal(fin, row, StateCancelled, ErrCancelledByUser, "cancelled by the user", acct.ms())
	case ctx.Err() != nil:
		s.removeWork(row.ID) // shutdown: the row stays running and Recover re-queues it after the lease
	case fail.requeue && row.Attempts < MaxAttempts:
		s.removeWork(row.ID)
		s.requeue(fin, row)
	case fail.requeue:
		s.removeWork(row.ID)
		s.finishTerminal(fin, row, StateFailed, ErrInterrupted, "the render was interrupted twice and is not retried", acct.ms())
	default:
		s.removeWork(row.ID)
		s.finishTerminal(fin, row, StateFailed, fail.code, s.clean(fail.msg), acct.ms())
	}
}

// requeue puts a running row we own back to queued (the claim already counted the attempt), as Recover does for an expired lease
// but at once: the render was interrupted from outside while this runner is alive.
func (s *Service) requeue(ctx context.Context, row tRow) {
	res, err := s.o.Store.W.ExecContext(ctx, `UPDATE transforms SET state = 'queued', owner = NULL, lease_expires_at = NULL, phase = NULL,
		progress_permille = 0 WHERE id = ? AND state = 'running' AND owner = ?`, row.ID, s.owner())
	if err != nil {
		s.log().Error("transform requeue", "transform_id", row.ID, "err", redact.Error(err))
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		s.log().Warn("transform requeue: lease lost", "transform_id", row.ID)
		return
	}
	s.changed(row.ID)
	s.Kick()
}

// finishTerminal moves a running row we own to failed or cancelled and writes the audit row in the same transaction.
func (s *Service) finishTerminal(ctx context.Context, row tRow, state, code, text string, cpuMS int64) {
	op := AuditFailed
	if state == StateCancelled {
		op = AuditCancelled
	}
	var lost bool
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		now := s.o.Now()
		lost = false
		res, err := tx.ExecContext(ctx, `UPDATE transforms SET state = ?, error_code = ?, error_text = ?, cpu_ms = ?, finished_at = ?, owner = NULL,
			lease_expires_at = NULL, phase = NULL WHERE id = ? AND state = 'running' AND owner = ?`,
			state, code, nullText(text), cpuMS, store.FormatTime(now), row.ID, s.owner())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			lost = true
			return nil
		}
		return auditTerminal(tx, op, now, auditDetails{ID: row.ID, Kind: string(row.Kind), CPUMS: cpuMS, Code: code}, s.sinceStart(row, now))
	})
	if err != nil {
		s.log().Error("transform finish", "transform_id", row.ID, "err", redact.Error(err))
		return
	}
	if lost {
		s.log().Warn("transform finish: lease lost", "transform_id", row.ID)
		return
	}
	s.changed(row.ID)
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// sinceStart is the wall time since the row started (an audit duration).
func (s *Service) sinceStart(row tRow, now time.Time) time.Duration {
	if row.StartedAt.Valid {
		if t, err := store.ParseTime(row.StartedAt.String); err == nil {
			return max(now.Sub(t), 0)
		}
	}
	return 0
}

// writeTx runs fn in one write transaction.
func (s *Service) writeTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Cancel records the intent to cancel a transform (C210.2, C210.8). A queued row is cancelled right here, in one transaction
// with its audit row: it never ran, so cpu_ms is 0 and its quota reservation is released. A running row gets
// cancel_requested_at and the runner kills the child within CancelGrace. A terminal row is left as it is. An agent may cancel
// only a transform it owns (403 FORBIDDEN, details.owner; a principal that was handed another's live spec through reuse is not
// its owner); a human cancels any. B4's operation reads the view afterwards.
func (s *Service) Cancel(ctx context.Context, p auth.Principal, id string) error {
	if s == nil || s.o.Store == nil {
		return problem.E(503, "UNAVAILABLE", "transforms are not available", map[string]any{"component": "transform"})
	}
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		var ownerID, ownerName, state, kind string
		var started sql.NullString
		switch err := tx.QueryRowContext(ctx, `SELECT principal_id, principal_name, state, kind, started_at FROM transforms WHERE id = ?`, id).
			Scan(&ownerID, &ownerName, &state, &kind, &started); {
		case errors.Is(err, sql.ErrNoRows):
			return problem.E(404, "NOT_FOUND", "no such transform", nil)
		case err != nil:
			return err
		}
		if p.Kind == auth.KindAgent && ownerID != p.Key() {
			return problem.E(403, "FORBIDDEN", "an agent cancels only its own transforms", map[string]any{"owner": ownerName})
		}
		now := s.o.Now()
		switch state {
		case StateQueued:
			if _, err := tx.ExecContext(ctx, `UPDATE transforms SET state = 'cancelled', error_code = ?, error_text = ?, cancel_requested_at = ?, cpu_ms = 0,
				finished_at = ? WHERE id = ? AND state = 'queued'`, ErrCancelledByUser, "cancelled by the user before it ran", store.FormatTime(now), store.FormatTime(now), id); err != nil {
				return err
			}
			return auditTerminal(tx, AuditCancelled, now, auditDetails{ID: id, Kind: kind, CPUMS: 0, Code: ErrCancelledByUser}, 0)
		case StateRunning:
			_, err := tx.ExecContext(ctx, `UPDATE transforms SET cancel_requested_at = COALESCE(cancel_requested_at, ?) WHERE id = ? AND state = 'running'`,
				store.FormatTime(now), id)
			return err
		}
		return nil // already terminal
	})
	if err != nil {
		return err
	}
	s.changed(id)
	return nil
}
