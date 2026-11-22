package pods

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/money"
)

// settle settles the pod time of every finished batch whose pods are all terminated (C393.5). The runner's FinishBatch already settled
// min(F(overhead seconds), pot) through the meter; what remains is the lifetime beyond it:
//
//	excess = F(Σ lifetime_s) − Σ settle rows of the batch
//
// When positive it is written as a batch-level `settle excess` and trips pod_excess (the pot did not cover the pods). The pods are marked
// settled in the same transaction, so a batch settles once.
func (l *Lifecycle) settle(ctx context.Context) error {
	rows, err := l.o.Store.R.QueryContext(ctx, `SELECT DISTINCT p.batch_id FROM pods p JOIN batches b ON b.id = p.batch_id
		WHERE p.state = 'terminated' AND p.settled = 0 AND b.state = 'finished'
		AND NOT EXISTS (SELECT 1 FROM pods q WHERE q.batch_id = p.batch_id AND q.state <> 'terminated')`)
	if err != nil {
		return err
	}
	var batches []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		batches = append(batches, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var firstErr error
	for _, id := range batches {
		if err := l.settleBatch(ctx, id); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("pods: settle: %w", err)
		}
	}
	return firstErr
}

func (l *Lifecycle) settleBatch(ctx context.Context, batchID string) error {
	var life, price, settled int64
	var lastPod string
	err := l.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(lifetime_s), 0), COALESCE(MAX(price_pph_micro_usd), 0), COALESCE(MAX(id), '')
		FROM pods WHERE batch_id = ?`, batchID).Scan(&life, &price, &lastPod)
	if err != nil {
		return err
	}
	if err := l.o.Store.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE batch_id = ? AND kind = 'settle'`, batchID).Scan(&settled); err != nil {
		return err
	}
	excess := money.ForSeconds(life, money.MicroUSD(price)) - money.MicroUSD(settled)
	if excess > 0 {
		// the trip first: if it fails the pods stay unsettled and the next tick tries again; it is one open row per backend
		if err := l.trip(ctx, batchID, TripExcess, map[string]any{"excess_micro_usd": int64(excess), "lifetime_s": life}); err != nil {
			return err
		}
	}
	now := l.now()
	done := false
	err = l.fenced(ctx, func(tx *sql.Tx) error {
		n, err := MarkBatchSettled(ctx, tx, batchID, now)
		if err != nil || n == 0 {
			return err
		}
		done = true
		if excess > 0 {
			if err := l.o.Spend.SettlePodExcess(ctx, tx, batchID, excess, l.o.Fence()); err != nil {
				return err
			}
		}
		return AppendEvent(ctx, tx, lastPod, Event{At: now, Event: "settled", Detail: map[string]any{"lifetime_s": life, "excess_micro_usd": int64(max(excess, 0))}})
	})
	if err == nil && done {
		l.audit(ctx, OpPodSettle, map[string]any{"batch": batchID, "lifetime_s": life, "excess_micro_usd": int64(max(excess, 0))}, max(excess, 0), false)
	}
	return err
}
