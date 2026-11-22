package nightshift

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// HaltCancelReason is the reason text of the cancel a halt sends (C376.1).
const HaltCancelReason = "night shift halted"

// Halt stops the night shift (halt_night_shift, C376.1): it records the halt, and cancels the batch of a night that is still running.
// Later nights are skipped `halted` until the Owner enables the shift again in the app (UpdateSettings {Enabled: true} clears it).
// It only reduces spend, so every Owner-side principal may (the Owner, a paired phone, an agent with the write scope the operation
// checks); an Editor or Listener may not. Idempotent: halting a halted shift keeps the first halt and re-sends no cancel that is not needed.
func (r *Runner) Halt(ctx context.Context, p auth.Principal, reason string) (Settings, error) {
	if !p.IsOwnerSide() {
		return Settings{}, problem.OwnerOnly("night shift")
	}
	if n := len([]rune(reason)); n < 1 || n > MaxReason {
		return Settings{}, invalid("reason", "length")
	}
	r.mu.Lock() // a night in this process finishes (or fails to start) before the halt cancels what it created
	defer r.mu.Unlock()
	return r.haltNoLock(ctx, p, reason)
}

// haltNoLock is Halt for a caller that already holds the night lock or needs none (the guards were checked by Halt).
func (r *Runner) haltNoLock(ctx context.Context, p auth.Principal, reason string) (Settings, error) {
	var out Settings
	err := r.writeTx(ctx, func(tx *sql.Tx) error {
		s, err := readSettings(ctx, tx)
		if err != nil {
			return err
		}
		if !s.Halted() {
			now := r.o.Now()
			by := p.Key()
			if _, err := tx.ExecContext(ctx, `UPDATE night_shift_settings SET halted_at = ?, halted_by = ?, halt_reason = ?, updated_by = ?, updated_at = ? WHERE id = 1`,
				store.FormatTime(now), by, reason, by, store.FormatTime(now)); err != nil {
				return err
			}
			s.HaltedAt, s.HaltedBy, s.HaltReason = &now, &by, &reason
			s.UpdatedBy, s.UpdatedAt = by, now
		}
		out = s
		return nil
	})
	if err != nil {
		return Settings{}, err
	}
	running, err := r.unfinished(ctx, r.o.Store.R, "")
	if err != nil {
		return out, err
	}
	for _, run := range running {
		if run.State != StateCreated { // already halted: its cancel was sent
			continue
		}
		if r.o.Cancel == nil {
			return out, errors.New("nightshift: no way to cancel the running batch")
		}
		if err := r.o.Cancel(ctx, auth.System("nightshift"), run.BatchID, HaltCancelReason); err != nil {
			return out, fmt.Errorf("nightshift: cancel batch: %w", err)
		}
		if _, err := r.o.Store.W.ExecContext(ctx, `UPDATE night_shift_runs SET state = 'halted', reason = 'halted', updated_at = ? WHERE id = ? AND state = 'created'`,
			store.FormatTime(r.o.Now()), run.ID); err != nil {
			return out, err
		}
		r.audit(ctx, OpHaltCancel, map[string]any{"night": run.Night, "batch_id": run.BatchID}, false)
	}
	return out, nil
}
