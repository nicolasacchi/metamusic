package spend

import (
	"context"
	"database/sql"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Querier is the read side the spend SQL needs; *sql.Tx and *sql.DB satisfy it. Inside a write
// transaction every read goes through that transaction (C72.3).
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Usage is what has been spent and reserved, for the caps (C72.3).
type Usage struct {
	MonthSettled, Outstanding, PrincipalDaySettled, PrincipalOutstanding money.MicroUSD
	MonthStart, DayStart                                                 time.Time
}

// outstandingExpr sums a batch's ledger into its outstanding amount.
const outstandingSQL = `SELECT l.batch_id, b.principal_id,
    SUM(CASE l.kind WHEN 'reserve' THEN l.micro_usd WHEN 'settle' THEN -l.micro_usd WHEN 'release' THEN -l.micro_usd ELSE 0 END)
  FROM ledger l JOIN batches b ON b.id = l.batch_id
  WHERE b.reconciled_at IS NULL GROUP BY l.batch_id, b.principal_id`

func (s *Service) monthBounds(now time.Time) (time.Time, time.Time) {
	n := now.In(s.loc)
	m0 := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, s.loc)
	return m0, m0.AddDate(0, 1, 0)
}

func (s *Service) dayBounds(now time.Time) (time.Time, time.Time) {
	n := now.In(s.loc)
	d0 := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, s.loc)
	return d0, d0.AddDate(0, 0, 1)
}

// Usage computes the month and day figures through q. Outstanding is floored per batch at zero.
func (s *Service) Usage(ctx context.Context, q Querier, principalKey string, now time.Time) (Usage, error) {
	m0, m1 := s.monthBounds(now)
	d0, d1 := s.dayBounds(now)
	u := Usage{MonthStart: m0, DayStart: d0}
	rows, err := q.QueryContext(ctx, outstandingSQL)
	if err != nil {
		return u, err
	}
	for rows.Next() {
		var batch, owner string
		var v int64
		if err := rows.Scan(&batch, &owner, &v); err != nil {
			rows.Close()
			return u, err
		}
		if v <= 0 {
			continue
		}
		u.Outstanding += money.MicroUSD(v)
		if owner == principalKey {
			u.PrincipalOutstanding += money.MicroUSD(v)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return u, err
	}
	rows.Close()
	var v int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(micro_usd), 0) FROM ledger
		WHERE (kind = 'settle' AND at >= ?1 AND at < ?2) OR (kind = 'bill' AND window_start >= ?1 AND window_start < ?2)`,
		store.FormatTime(m0), store.FormatTime(m1)).Scan(&v); err != nil {
		return u, err
	}
	u.MonthSettled = money.MicroUSD(v)
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(micro_usd), 0) FROM ledger
		WHERE kind = 'settle' AND principal_id = ? AND at >= ? AND at < ?`,
		principalKey, store.FormatTime(d0), store.FormatTime(d1)).Scan(&v); err != nil {
		return u, err
	}
	u.PrincipalDaySettled = money.MicroUSD(v)
	return u, nil
}

// BatchOutstanding is one batch's outstanding amount (C72.5), floored at zero.
func BatchOutstanding(ctx context.Context, q Querier, batchID string) (money.MicroUSD, error) {
	var v int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE kind WHEN 'reserve' THEN micro_usd WHEN 'settle' THEN -micro_usd
		WHEN 'release' THEN -micro_usd ELSE 0 END), 0) FROM ledger WHERE batch_id = ?`, batchID).Scan(&v)
	return money.MicroUSD(max(v, 0)), err
}
