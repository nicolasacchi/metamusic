package transform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Recover and the startup sweep (C210.3). Recover runs before the first claim and whenever the runner is idle; Sweep once at
// startup (the work-dir half also when idle).

// Recover handles every running row whose lease has expired: a requested cancel finishes it cancelled; otherwise it goes back to
// queued with attempts counted (the claim already added one), and once MaxAttempts is reached it fails INTERRUPTED. Every
// terminal row records cpu_ms 0 (the killed child's time is unknown) and so releases its quota reservation.
func (s *Service) Recover(ctx context.Context) error {
	if s == nil || s.o.Store == nil {
		return nil
	}
	now := s.o.Now()
	var touched []string
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		touched = touched[:0]
		rows, err := tx.QueryContext(ctx, `SELECT id, kind, attempts, cancel_requested_at, started_at FROM transforms
			WHERE state = 'running' AND lease_expires_at < ?`, store.FormatTime(now))
		if err != nil {
			return err
		}
		type exp struct {
			id, kind string
			attempts int
			cancel   bool
			started  sql.NullString
		}
		var due []exp
		for rows.Next() {
			var e exp
			var c sql.NullString
			if err := rows.Scan(&e.id, &e.kind, &e.attempts, &c, &e.started); err != nil {
				_ = rows.Close()
				return err
			}
			e.cancel = c.Valid
			due = append(due, e)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, e := range due {
			row := tRow{ID: e.id, Kind: Kind(e.kind), StartedAt: e.started}
			fence := `id = ? AND state = 'running' AND lease_expires_at < ?`
			args := []any{e.id, store.FormatTime(now)}
			var res sql.Result
			switch {
			case e.cancel:
				res, err = tx.ExecContext(ctx, `UPDATE transforms SET state = 'cancelled', error_code = ?, error_text = ?, cpu_ms = 0, finished_at = ?,
					owner = NULL, lease_expires_at = NULL, phase = NULL WHERE `+fence, append([]any{ErrCancelledByUser, "cancelled by the user", store.FormatTime(now)}, args...)...)
				if err == nil {
					err = s.auditIfChanged(tx, res, AuditCancelled, now, row, ErrCancelledByUser)
				}
			case e.attempts < MaxAttempts:
				res, err = tx.ExecContext(ctx, `UPDATE transforms SET state = 'queued', owner = NULL, lease_expires_at = NULL, phase = NULL, progress_permille = 0
					WHERE `+fence, args...)
				_ = res
			default:
				res, err = tx.ExecContext(ctx, `UPDATE transforms SET state = 'failed', error_code = ?, error_text = ?, cpu_ms = 0, finished_at = ?,
					owner = NULL, lease_expires_at = NULL, phase = NULL WHERE `+fence,
					append([]any{ErrInterrupted, "the render was interrupted twice and is not retried", store.FormatTime(now)}, args...)...)
				if err == nil {
					err = s.auditIfChanged(tx, res, AuditFailed, now, row, ErrInterrupted)
				}
			}
			if err != nil {
				return err
			}
			touched = append(touched, e.id)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("transform: recover: %w", err)
	}
	for _, id := range touched {
		s.changed(id)
	}
	if len(touched) > 0 {
		s.Kick()
	}
	return nil
}

func (s *Service) auditIfChanged(tx *sql.Tx, res sql.Result, op string, now time.Time, row tRow, code string) error {
	if n, _ := res.RowsAffected(); n != 1 {
		return nil
	}
	return auditTerminal(tx, op, now, auditDetails{ID: row.ID, Kind: string(row.Kind), CPUMS: 0, Code: code}, s.sinceStart(row, now))
}

// Sweep is the startup sweep: stale work dirs and the takes/<id> directory of a transform that crashed between ingest and
// commit. A work dir `incoming/transform-<id>` is removed when its row is not running or it is older than WorkSweepAge, after
// whatever its journal says the freeze claimed has been discarded; `incoming/verify-*` scratch dirs older than WorkSweepAge go
// too. A `takes/<id>` directory is removed only when `id` is a transform that is not done and has no tracks row.
func (s *Service) Sweep(ctx context.Context) error {
	if s == nil || s.o.Store == nil || s.o.Data == nil {
		return nil
	}
	if err := s.sweepWork(ctx); err != nil {
		return err
	}
	entries, err := fs.ReadDir(s.o.Data.FS(), "takes")
	if errors.Is(err, fs.ErrNotExist) {
		return nil // no takes directory yet
	}
	if err != nil {
		return fmt.Errorf("transform: sweep: %w", err)
	}
	now := s.o.Now()
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		var state string
		var lease sql.NullString
		switch err := s.o.Store.R.QueryRowContext(ctx, `SELECT state, lease_expires_at FROM transforms WHERE id = ?`, id).Scan(&state, &lease); {
		case errors.Is(err, sql.ErrNoRows):
			continue // not a transform's directory
		case err != nil:
			return fmt.Errorf("transform: sweep: %w", err)
		}
		if state == StateDone || (state == StateRunning && !leaseExpired(lease, now)) {
			continue
		}
		var n int
		if err := s.o.Store.R.QueryRowContext(ctx, `SELECT count(*) FROM tracks WHERE id = ?`, id).Scan(&n); err != nil {
			return fmt.Errorf("transform: sweep: %w", err)
		}
		if n == 0 {
			s.discardOrphan(id)
		}
	}
	return nil
}

func leaseExpired(lease sql.NullString, now time.Time) bool {
	if !lease.Valid {
		return true
	}
	t, err := store.ParseTime(lease.String)
	return err != nil || t.Before(now)
}

// sweepWork removes stale work and verify scratch dirs under incoming/.
func (s *Service) sweepWork(ctx context.Context) error {
	entries, err := fs.ReadDir(s.o.Data.FS(), "incoming")
	if errors.Is(err, fs.ErrNotExist) {
		return nil // nothing has run yet
	}
	if err != nil {
		return fmt.Errorf("transform: sweep: %w", err)
	}
	now := s.o.Now()
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := e.Name()
		old := false
		if info, err := e.Info(); err == nil {
			old = now.Sub(info.ModTime()) > WorkSweepAge
		}
		switch {
		case strings.HasPrefix(name, "verify-") && e.IsDir():
			if old {
				_ = s.o.Data.RemoveAll("incoming/" + name)
			}
		case strings.HasPrefix(name, "transform-") && e.IsDir():
			id := strings.TrimPrefix(name, "transform-")
			var state string
			err := s.o.Store.R.QueryRowContext(ctx, `SELECT state FROM transforms WHERE id = ?`, id).Scan(&state)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("transform: sweep: %w", err)
			}
			if err == nil && state == StateRunning && !old {
				continue // a live attempt
			}
			var n int
			if err := s.o.Store.R.QueryRowContext(ctx, `SELECT count(*) FROM tracks WHERE id = ?`, id).Scan(&n); err != nil {
				return fmt.Errorf("transform: sweep: %w", err)
			}
			if n == 0 { // a crash between ingest and commit: discard what the journal names
				s.discardOrphan(id)
			}
			_ = s.o.Data.RemoveAll("incoming/" + name)
		}
	}
	return nil
}
