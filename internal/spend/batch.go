package spend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// BatchTransition is one legal batch move and its single writer (C72.6).
type BatchTransition struct{ From, To, Writer string }

// BatchTransitions is the table of batch states and who writes them. cancel_batch changes no state.
var BatchTransitions = []BatchTransition{
	{"", "active", "create"},
	{"", "awaiting_confirmation", "create"},
	{"awaiting_confirmation", "active", "ApproveBatch"},
	{"awaiting_confirmation", "rejected", "RejectBatch"},
	{"awaiting_confirmation", "expired", "ExpireApproval"},
	{"active", "finished", "FinishBatch"},
	{"finished", "reconciled", "ReconcileBatch"}, // reconciled_at set; the state stays finished
}

// FinishInput closes an active batch (runner, lease predicate).
type FinishInput struct {
	BatchID string
	// OverheadMicro is the meter-attributed overhead; it is capped at the batch's remaining overhead
	// pot, never more than reserved (C76.6).
	OverheadMicro money.MicroUSD
	Lease         store.LeaseFence
}

// FinishResult reports what FinishBatch settled and left outstanding.
type FinishResult struct{ Overhead, Held money.MicroUSD }

// FinishBatch moves an active batch to finished, settles the overhead and records the residual 'held'
// (the outstanding at finish, informational).
func (s *Service) FinishBatch(ctx context.Context, tx *sql.Tx, in FinishInput) (FinishResult, error) {
	var r FinishResult
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT principal_id FROM batches WHERE id = ?`, in.BatchID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	} else if err != nil {
		return r, err
	}
	//nolint:gosec // G202: lease.And() is the fixed C69 predicate; every value is bound
	res, err := tx.ExecContext(ctx, `UPDATE batches SET state='finished', finished_at=? WHERE id=? AND state='active'`+in.Lease.And(),
		append([]any{store.FormatTime(s.now()), in.BatchID}, in.Lease.Args()...)...)
	n, err := in.Lease.Fenced(res, err)
	if err != nil {
		return r, fmt.Errorf("spend: finish batch: %w", err)
	}
	if n == 0 {
		if err := in.Lease.Confirm(ctx, tx, 0); err != nil {
			return r, err
		}
		return r, fmt.Errorf("%w: batch is not active", ErrInvalidTransition)
	}
	rem, err := overheadRemaining(ctx, tx, in.BatchID)
	if err != nil {
		return r, err
	}
	r.Overhead = min(max(in.OverheadMicro, 0), max(rem, 0))
	if err := s.ledger.append(ctx, tx, &in.Lease, []Entry{{Kind: KindSettle, MicroUSD: r.Overhead, BatchID: &in.BatchID, PrincipalID: owner, Reason: "overhead"}}); err != nil {
		return r, err
	}
	if r.Held, err = BatchOutstanding(ctx, tx, in.BatchID); err != nil {
		return r, err
	}
	return r, s.ledger.append(ctx, tx, &in.Lease, []Entry{{Kind: KindResidual, MicroUSD: r.Held, BatchID: &in.BatchID, PrincipalID: owner, Reason: "held"}})
}

// ReconcileBatch releases a finished batch's outstanding with reason ('reconciled' or 'hold_elapsed') and
// sets reconciled_at (reconciler, lease predicate).
func (s *Service) ReconcileBatch(ctx context.Context, tx *sql.Tx, batchID, reason string, lease store.LeaseFence) (money.MicroUSD, error) {
	if reason != "reconciled" && reason != "hold_elapsed" {
		return 0, fmt.Errorf("%w: reconcile reason %q", ErrBadEntry, reason)
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT principal_id FROM batches WHERE id = ?`, batchID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, err
	}
	//nolint:gosec // G202: fixed C69 predicate, values are bound
	res, err := tx.ExecContext(ctx, `UPDATE batches SET reconciled_at=? WHERE id=? AND state='finished' AND reconciled_at IS NULL`+lease.And(),
		append([]any{store.FormatTime(s.now()), batchID}, lease.Args()...)...)
	n, err := lease.Fenced(res, err)
	if err != nil {
		return 0, fmt.Errorf("spend: reconcile batch: %w", err)
	}
	if n == 0 {
		if err := lease.Confirm(ctx, tx, 0); err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("%w: batch is not finished and open", ErrInvalidTransition)
	}
	// reconciled_at is set first, so the outstanding is read from the ledger directly
	var v int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE kind WHEN 'reserve' THEN micro_usd WHEN 'settle' THEN -micro_usd
		WHEN 'release' THEN -micro_usd ELSE 0 END), 0) FROM ledger WHERE batch_id = ?`, batchID).Scan(&v); err != nil {
		return 0, err
	}
	out := money.MicroUSD(max(v, 0))
	if out > 0 {
		if err := s.ledger.append(ctx, tx, &lease, []Entry{{Kind: KindRelease, MicroUSD: out, BatchID: &batchID, PrincipalID: owner, Reason: reason}}); err != nil {
			return 0, err
		}
	}
	return out, nil
}
