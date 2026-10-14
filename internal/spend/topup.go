package spend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TopUp is one later reserve (C72.3): an attempt overhead or an execution top-up.
type TopUp struct {
	BatchID, JobID string
	AttemptID      *string
	Reason         string
	Amount         money.MicroUSD
	Lease          store.LeaseFence
}

// ErrNotFound means the batch, job or approval does not exist.
var ErrNotFound = errors.New("spend: not found")

var topUpReasons = map[string]bool{"attempt_overhead": true, "topup_ambiguous": true, "topup_requeue": true, "topup_price": true}

// ReserveTopUp checks the batch ceiling, the month and the batch owner's day, in that order, and
// writes the reserve with the lease predicate. A refusal returns the CapError and writes nothing;
// everything is read through tx.
func (s *Service) ReserveTopUp(ctx context.Context, tx *sql.Tx, t TopUp) (*CapError, error) {
	if !topUpReasons[t.Reason] {
		return nil, fmt.Errorf("%w: %q is not a top-up reason", ErrBadEntry, t.Reason)
	}
	if t.Amount <= 0 {
		return nil, fmt.Errorf("%w: top-up amount", ErrBadEntry)
	}
	var owner string
	var ceiling, reserved int64
	err := tx.QueryRowContext(ctx, `SELECT b.principal_id, b.ceiling_micro_usd,
		COALESCE((SELECT SUM(micro_usd) FROM ledger l WHERE l.batch_id = b.id AND l.kind = 'reserve'), 0)
		FROM batches b WHERE b.id = ?`, t.BatchID).Scan(&owner, &ceiling, &reserved)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if money.MicroUSD(reserved)+t.Amount > money.MicroUSD(ceiling) {
		return &CapError{Cap: CapBatchCeiling, Limit: money.MicroUSD(ceiling), Used: money.MicroUSD(reserved), Requested: t.Amount}, nil
	}
	caps, err := s.LoadCaps(ctx, tx)
	if err != nil {
		return nil, err
	}
	u, err := s.Usage(ctx, tx, owner, s.now())
	if err != nil {
		return nil, err
	}
	tok, err := s.TokenLimitsFor(ctx, tx, owner)
	if err != nil {
		return nil, err
	}
	daily := caps.HumanDailyMicro
	if strings.HasPrefix(owner, "agent:") {
		daily = agentDaily(tok, caps)
	}
	if ce := checkMonthDay(u, caps, daily, t.Amount); ce != nil {
		return ce, nil
	}
	e := Entry{Kind: KindReserve, MicroUSD: t.Amount, BatchID: &t.BatchID, JobID: &t.JobID, AttemptID: t.AttemptID, PrincipalID: owner, Reason: t.Reason}
	if err := s.ledger.append(ctx, tx, &t.Lease, []Entry{e}); err != nil {
		return nil, err
	}
	return nil, nil
}
