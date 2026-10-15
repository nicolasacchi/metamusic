package provenance

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Key is one provenance_dirty row as read: At is remembered so the dequeue can tell a re-marked row from the one it
// processed (C95.1).
type Key struct {
	Kind, ID, At string
	Tries        int
}

// kindRank orders the queue: the seed gives every key almost the same `at`, so ordering by `at` alone would
// interleave tracks and drafts across batches (C95.1).
const kindRankSQL = `CASE kind WHEN 'run' THEN 0 WHEN 'collection' THEN 1 WHEN 'batch' THEN 2 WHEN 'track' THEN 3 ELSE 4 END`

// readKeys reads up to limit keys in selection order: kind rank, then at, then id.
func readKeys(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, limit int) ([]Key, error) {
	rows, err := q.QueryContext(ctx, `SELECT kind, id, at, tries FROM provenance_dirty ORDER BY `+kindRankSQL+`, at, id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("provenance: read queue: %w", err)
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		if err := rows.Scan(&k.Kind, &k.ID, &k.At, &k.Tries); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Pending is the number of queued keys (one indexed count).
func (s *Service) Pending(ctx context.Context) (int, error) {
	var n int
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM provenance_dirty`).Scan(&n); err != nil {
		return 0, fmt.Errorf("provenance: pending: %w", err)
	}
	return n, nil
}

// hasPending is the cheap poll of Run: a single indexed SELECT 1.
func (s *Service) hasPending(ctx context.Context) (bool, error) {
	var one int
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT 1 FROM provenance_dirty LIMIT 1`).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// PendingByKind counts the queue by kind (the CLI prints it; dry-run reports it).
func (s *Service) PendingByKind(ctx context.Context) (map[string]int, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT kind, COUNT(*) FROM provenance_dirty GROUP BY kind`)
	if err != nil {
		return nil, fmt.Errorf("provenance: pending by kind: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// dequeueTx deletes the clean keys whose row still carries the `at` that was processed: a row re-marked meanwhile
// has a newer `at` and survives (C95.1).
func dequeueTx(ctx context.Context, tx *sql.Tx, keys []Key) error {
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM provenance_dirty WHERE kind = ? AND id = ? AND at = ?`, k.Kind, k.ID, k.At); err != nil {
			return fmt.Errorf("provenance: dequeue: %w", err)
		}
	}
	return nil
}

// requeueTx counts one more try on keys that reported a transient anomaly (never dequeued, C95.1).
func requeueTx(ctx context.Context, tx *sql.Tx, keys []Key) error {
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `UPDATE provenance_dirty SET tries = tries + 1 WHERE kind = ? AND id = ? AND at = ?`, k.Kind, k.ID, k.At); err != nil {
			return fmt.Errorf("provenance: requeue: %w", err)
		}
	}
	return nil
}

// FormatQueue renders the by-kind counts as "kind=n ..." in a stable order (CLI output).
func FormatQueue(m map[string]int) string {
	var parts []string
	for _, k := range []string{"run", "collection", "batch", "track", "draft"} {
		if n := m[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, n))
		}
	}
	if len(parts) == 0 {
		return "empty"
	}
	return strings.Join(parts, " ")
}
