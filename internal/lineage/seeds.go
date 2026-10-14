package lineage

import (
	"context"
	"database/sql"
	"fmt"
)

// Querier is the read side the lineage SQL needs; *sql.Tx, *sql.DB and the store pools satisfy it. Inside a
// write transaction every read goes through that transaction (C72.3).
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// UsedSeeds lists the seeds of every recipe with this content hash (the song without id and seed).
// Reference stubs have no recipe_keys row, so their placeholder seed 0 never counts (D-M3-34).
func UsedSeeds(ctx context.Context, q Querier, contentHash string) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.seed FROM recipes r JOIN recipe_keys k ON k.recipe_id = r.id WHERE k.content_hash = ? ORDER BY r.seed`, contentHash)
	if err != nil {
		return nil, fmt.Errorf("lineage: used seeds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// NextSeeds returns n seeds not in used, deterministically (C71.3): start = max(base, max(used)) + 1
// ascending to MaxSeed, then 1000 ascending up to start - 1, then 0 ascending to 999. Fewer than n only
// when the whole range is used.
func NextSeeds(base int64, used []int64, n int) []int64 {
	if n <= 0 {
		return nil
	}
	taken := make(map[int64]bool, len(used))
	start := base
	for _, u := range used {
		taken[u] = true
		start = max(start, u)
	}
	start++
	var out []int64
	take := func(from, to int64) {
		for s := from; s <= to && len(out) < n; s++ {
			if !taken[s] {
				out = append(out, s)
				taken[s] = true
			}
		}
	}
	if start <= MaxSeed {
		take(start, MaxSeed)
	}
	if len(out) < n {
		take(1000, min(start-1, MaxSeed))
	}
	if len(out) < n {
		take(0, min(999, start-1))
	}
	return out
}
