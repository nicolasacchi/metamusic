package provenance

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// acquire takes the settle lease (D-L1-35): it is free (owner ”), expired, or already ours; the epoch is bumped on
// every acquisition. ErrSettleBusy when another live settler holds it.
func (s *Service) acquire(ctx context.Context) (int64, error) {
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("provenance: lease: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var owner, exp string
	var epoch int64
	if err := tx.QueryRowContext(ctx, `SELECT owner, epoch, expires_at FROM provenance_lease WHERE id = 1`).Scan(&owner, &epoch, &exp); err != nil {
		return 0, fmt.Errorf("provenance: lease: %w", err)
	}
	now := s.o.Now()
	if owner != "" && owner != s.o.Owner && exp > store.FormatTime(now) {
		return 0, ErrSettleBusy
	}
	epoch++
	if _, err := tx.ExecContext(ctx, `UPDATE provenance_lease SET owner = ?, epoch = ?, expires_at = ? WHERE id = 1`,
		s.o.Owner, epoch, store.FormatTime(now.Add(picks.SettleLeaseTTL))); err != nil {
		return 0, fmt.Errorf("provenance: lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("provenance: lease: %w", err)
	}
	return epoch, nil
}

// renew extends the lease while it is still ours at epoch.
func (s *Service) renew(ctx context.Context, epoch int64) error {
	res, err := s.o.Store.W.ExecContext(ctx, `UPDATE provenance_lease SET expires_at = ? WHERE id = 1 AND owner = ? AND epoch = ?`,
		store.FormatTime(s.o.Now().Add(picks.SettleLeaseTTL)), s.o.Owner, epoch)
	if err != nil {
		return fmt.Errorf("provenance: renew lease: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLeaseLost
	}
	return nil
}

// release frees the lease when it is still ours at epoch.
func (s *Service) release(epoch int64) {
	_, _ = s.o.Store.W.ExecContext(context.Background(), `UPDATE provenance_lease SET owner = '', expires_at = '1970-01-01T00:00:00.000Z' WHERE id = 1 AND owner = ? AND epoch = ?`,
		s.o.Owner, epoch)
}

// checkEpochTx runs first inside both phase transactions: our epoch, else ErrLeaseLost and nothing is written.
func (s *Service) checkEpochTx(ctx context.Context, tx *sql.Tx, epoch int64) error {
	var owner string
	var cur int64
	if err := tx.QueryRowContext(ctx, `SELECT owner, epoch FROM provenance_lease WHERE id = 1`).Scan(&owner, &cur); err != nil {
		return fmt.Errorf("provenance: check lease: %w", err)
	}
	if owner != s.o.Owner || cur != epoch {
		return ErrLeaseLost
	}
	return nil
}

// LeaseHeld reports whether a live settler other than this service holds the lease (the CLI uses it to choose between
// settling and only enqueueing, D-L1-35).
func (s *Service) LeaseHeld(ctx context.Context) (bool, error) {
	var owner, exp string
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT owner, expires_at FROM provenance_lease WHERE id = 1`).Scan(&owner, &exp); err != nil {
		return false, fmt.Errorf("provenance: lease state: %w", err)
	}
	return owner != "" && owner != s.o.Owner && exp > store.FormatTime(s.o.Now()), nil
}
