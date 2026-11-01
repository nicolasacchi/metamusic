package features

import (
	"context"
	"database/sql"
	"fmt"
)

// SweepStaleTx rewrites up to limit rows whose definition, digest or subject changed (the startup and hourly sweeps, in
// batches of BatchSongs), and reports how many rows it wrote or deleted; the caller repeats until it answers 0.
//
// The order is cheap before exhaustive: rows of retired Songs, takes and Songs without a row of the current definition
// (found by SQL), then a digest scan of every take and every live Song for the rest of the budget. Takes come before Songs
// so a Song copies a take row of this batch.
func (s *Service) SweepStaleTx(ctx context.Context, tx *sql.Tx, env Env, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	r := newRun(ctx, tx, env)
	defer r.close()
	n := 0
	res, err := tx.ExecContext(ctx, `DELETE FROM song_features WHERE song_id IN (SELECT f.song_id FROM song_features f
		JOIN songs s ON s.id = f.song_id WHERE s.retired_at IS NOT NULL LIMIT ?)`, limit)
	if err != nil {
		return 0, fmt.Errorf("features: sweep retired: %w", err)
	}
	if d, _ := res.RowsAffected(); d > 0 {
		n += int(d)
	}
	takeStep := func(id string) error {
		t, err := r.take(id)
		if err != nil {
			return err
		}
		if t.changed {
			n++
		}
		return nil
	}
	songStep := func(id string) error {
		w, err := r.song(id)
		if err != nil {
			return err
		}
		if w {
			n++
		}
		return nil
	}
	ids := func(q string, args ...any) ([]string, error) {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("features: sweep: %w", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, rows.Err()
	}
	phases := []struct {
		query string
		step  func(string) error
		args  []any
	}{
		{`SELECT t.id FROM tracks t WHERE NOT EXISTS (SELECT 1 FROM take_features f WHERE f.track_id = t.id AND f.feature_def = ?) ORDER BY t.id LIMIT ?`, takeStep, []any{Def, limit}},
		{`SELECT s.id FROM songs s WHERE s.retired_at IS NULL AND NOT EXISTS (SELECT 1 FROM song_features f WHERE f.song_id = s.id AND f.feature_def = ?)
			AND (s.draft_id IS NOT NULL OR EXISTS (SELECT 1 FROM song_picks p WHERE p.song_id = s.id AND p.take_id IS NOT NULL)
			  OR EXISTS (SELECT 1 FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = s.id AND t.status <> 'discarded'))
			ORDER BY s.id LIMIT ?`, songStep, []any{Def, limit}},
		{`SELECT id FROM tracks ORDER BY id`, takeStep, nil},
		{`SELECT id FROM songs WHERE retired_at IS NULL ORDER BY id`, songStep, nil},
	}
	for _, p := range phases {
		list, err := ids(p.query, p.args...)
		if err != nil {
			return n, err
		}
		for _, id := range list {
			if n >= limit {
				return n, nil
			}
			if err := p.step(id); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}
