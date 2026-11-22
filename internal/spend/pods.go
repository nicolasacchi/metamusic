package spend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// PodOverhead is one pod's overhead reserve (C393.3), taken when the pod is created.
type PodOverhead struct {
	BatchID, PodID string
	Amount         money.MicroUSD
	Lease          store.LeaseFence
}

// ReservePodOverhead reserves a pod's overhead under the same checks as ReserveTopUp (batch ceiling, month, owner's day, in
// that order) and writes a `pod_overhead` reserve with the batch and no job. A refusal returns the CapError and writes nothing.
func (s *Service) ReservePodOverhead(ctx context.Context, tx *sql.Tx, o PodOverhead) (*CapError, error) {
	if o.Amount <= 0 {
		return nil, fmt.Errorf("%w: pod overhead amount", ErrBadEntry)
	}
	owner, ce, err := s.checkReserve(ctx, tx, o.BatchID, o.Amount)
	if err != nil || ce != nil {
		return ce, err
	}
	e := Entry{Kind: KindReserve, MicroUSD: o.Amount, BatchID: &o.BatchID, PrincipalID: owner, Reason: "pod_overhead"}
	if err := s.ledger.append(ctx, tx, &o.Lease, []Entry{e}); err != nil {
		return nil, err
	}
	return nil, nil
}

// SettlePodExcess writes the batch-level `settle excess` for pod time beyond the pot (C393.5). Like every excess it is not
// counted against the reserve. An amount <= 0 writes nothing.
func (s *Service) SettlePodExcess(ctx context.Context, tx *sql.Tx, batchID string, amount money.MicroUSD, lease store.LeaseFence) error {
	if amount <= 0 {
		return nil
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT principal_id FROM batches WHERE id = ?`, batchID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return s.ledger.append(ctx, tx, &lease, []Entry{{Kind: KindSettle, MicroUSD: amount, BatchID: &batchID, PrincipalID: owner, Reason: "excess"}})
}
