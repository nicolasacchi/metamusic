package spend

import (
	"context"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// MandateUsage is what a spending mandate has consumed (M6N C367). Used is what the operator calls "settled + reserved"; Committed
// also counts the unspent headroom of a batch that is still running, so Used <= Committed <= budget at every ledger commit. Excess is
// provider overshoot above the execution pot: shown, never counted.
type MandateUsage struct {
	Settled, Reserved, Used, Committed, Excess money.MicroUSD // Used = Settled + Reserved
	Batches, Takes                             int
	ByRoot                                     map[string]int // takes per lineage root of the job's parent, over non-rejected, non-expired batches
	DayBatches                                 map[string]int // batches per local day (the spend Location) of created_at
}

// MandateFits reports whether a batch of the given ceiling still fits the budget on top of what is committed.
func MandateFits(budget, committed, ceiling money.MicroUSD) bool { return committed+ceiling <= budget }

// mandateBatchSQL reads one row per batch of the mandate with its ledger sums. settle rows of reason 'excess' are provider overshoot
// and are kept apart from what was settled.
const mandateBatchSQL = `SELECT b.id, b.state, b.ceiling_micro_usd, b.reconciled_at IS NOT NULL, b.created_at,
    COALESCE(SUM(CASE WHEN l.kind = 'reserve' THEN l.micro_usd END), 0),
    COALESCE(SUM(CASE WHEN l.kind = 'settle' AND l.reason <> 'excess' THEN l.micro_usd END), 0),
    COALESCE(SUM(CASE WHEN l.kind = 'release' THEN l.micro_usd END), 0),
    COALESCE(SUM(CASE WHEN l.kind = 'settle' AND l.reason = 'excess' THEN l.micro_usd END), 0)
  FROM batches b LEFT JOIN ledger l ON l.batch_id = b.id
  WHERE b.mandate_id = ? GROUP BY b.id`

// mandateTakesSQL counts the takes of one mandate per lineage root of the job's parent, over non-rejected, non-expired batches. A take
// without a parent has no lineage and is counted in Takes only.
const mandateTakesSQL = `SELECT COALESCE(t.root_track_id, j.source_track_id, ''), COUNT(*)
  FROM jobs j JOIN batches b ON b.id = j.batch_id LEFT JOIN tracks t ON t.id = j.source_track_id
  WHERE b.mandate_id = ? AND b.state NOT IN ('rejected','expired') GROUP BY 1`

// MandateUsage computes one mandate's usage through q (the caller's transaction on a write path). Per batch: rejected or expired
// contributes nothing; a reconciled batch its settled amount; a finished one settled plus what is still outstanding; a running one
// the larger of its ceiling and settled plus outstanding.
func (s *Service) MandateUsage(ctx context.Context, q Querier, mandateID string) (MandateUsage, error) {
	u := MandateUsage{ByRoot: map[string]int{}, DayBatches: map[string]int{}}
	rows, err := q.QueryContext(ctx, mandateBatchSQL, mandateID)
	if err != nil {
		return u, err
	}
	for rows.Next() {
		var id, state, created string
		var ceiling, reserve, settled, release, excess int64
		var reconciled bool
		if err := rows.Scan(&id, &state, &ceiling, &reconciled, &created, &reserve, &settled, &release, &excess); err != nil {
			rows.Close()
			return u, err
		}
		u.Excess += money.MicroUSD(excess)
		if state == "rejected" || state == "expired" {
			continue
		}
		out := max(0, reserve-settled-release)
		if reconciled {
			out = 0
		}
		contribution := settled + out
		switch {
		case reconciled, state == "finished":
		default: // active (or awaiting): the whole ceiling stays committed until the batch ends
			contribution = max(ceiling, settled+out)
		}
		u.Settled += money.MicroUSD(settled)
		u.Reserved += money.MicroUSD(out)
		u.Committed += money.MicroUSD(contribution)
		u.Batches++
		at, err := store.ParseTime(created)
		if err != nil {
			rows.Close()
			return u, fmt.Errorf("spend: mandate batch %s: %w", id, err)
		}
		u.DayBatches[s.localDay(at)]++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return u, err
	}
	rows.Close()
	u.Used = u.Settled + u.Reserved
	tr, err := q.QueryContext(ctx, mandateTakesSQL, mandateID)
	if err != nil {
		return u, err
	}
	defer tr.Close()
	for tr.Next() {
		var root string
		var n int
		if err := tr.Scan(&root, &n); err != nil {
			return u, err
		}
		u.Takes += n
		if root != "" {
			u.ByRoot[root] += n
		}
	}
	return u, tr.Err()
}

// DayKey is the local calendar day (the spend Location) of t, the key of MandateUsage.DayBatches.
func (s *Service) DayKey(t time.Time) string { return s.localDay(t) }

// Location is the zone of the caps' calendar days and of MandateUsage.DayBatches; coverage must key "per day" in the same zone.
func (s *Service) Location() *time.Location { return s.loc }

func (s *Service) localDay(t time.Time) string { return t.In(s.loc).Format(time.DateOnly) }
