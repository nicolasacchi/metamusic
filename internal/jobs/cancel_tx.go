package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// CancelIntentsTx records a cancel intent on every non-terminal job of the given batches, in the caller's transaction, and withdraws
// a batch that is still awaiting confirmation (approval and batch end 'rejected', the reservation is released). It is the body of
// CancelBatch factored out so another transaction (the revoke of a spending mandate, M6N C369.4) can cancel in its own commit. by is
// the decider recorded on the jobs and on the withdrawn approval. The runner applies the intents; callers Wake it after the commit.
func (s *Service) CancelIntentsTx(ctx context.Context, tx *sql.Tx, batchIDs []string, by string) error {
	if err := s.wired(); err != nil {
		return err
	}
	return s.cancelIntentsTx(ctx, tx, batchIDs, by, "")
}

func (s *Service) cancelIntentsTx(ctx context.Context, tx *sql.Tx, batchIDs []string, by, reason string) error {
	at := store.FormatTime(s.o.Clock.Now())
	for _, id := range batchIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET cancel_requested_at = ?, cancel_requested_by = ? WHERE batch_id = ? AND cancel_requested_at IS NULL AND state NOT IN ('completed','failed','cancelled','expired')`,
			at, by, id); err != nil {
			return fmt.Errorf("jobs: cancel intents: %w", err)
		}
		// a batch still awaiting confirmation never started: withdraw its approval, end the batch and release
		// the reservation in this transaction, so it leaves the approvals hub and list_batches
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM batches WHERE id = ?`, id).Scan(&state); err != nil {
			return err
		}
		if state == StateAwaiting {
			if s.o.Spend == nil {
				return errors.ErrUnsupported
			}
			if _, err := s.o.Spend.WithdrawBatch(ctx, tx, id, by, reason, nil); err != nil {
				return fmt.Errorf("jobs: withdraw batch: %w", err)
			}
		}
	}
	return nil
}
