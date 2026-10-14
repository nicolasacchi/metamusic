package lineage

import (
	"context"
	"fmt"
)

// BackfillRecipeKeys fills recipe_keys for yue2 recipes inserted before M3 and never for stubs
// (D-M3-34); idempotent (C71.7). It returns the number of rows written.
func (s *Service) BackfillRecipeKeys(ctx context.Context) (int, error) {
	if s.st == nil {
		return 0, ErrUnconfigured
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT r.id, r.request_json FROM recipes r
		LEFT JOIN recipe_keys k ON k.recipe_id = r.id WHERE r.engine = 'yue2' AND k.recipe_id IS NULL ORDER BY r.id`)
	if err != nil {
		return 0, fmt.Errorf("lineage: backfill: %w", err)
	}
	type row struct{ id, content, render string }
	var todo []row
	for rows.Next() {
		var id, req string
		if err := rows.Scan(&id, &req); err != nil {
			_ = rows.Close()
			return 0, err
		}
		c, r := Keys([]byte(req))
		if c == "" || r == "" {
			_ = rows.Close()
			return 0, fmt.Errorf("lineage: backfill: recipe %s is not a request object", id)
		}
		todo = append(todo, row{id, c, r})
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(todo) == 0 {
		return 0, nil
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	n := 0
	for _, r := range todo {
		res, err := tx.ExecContext(ctx, `INSERT INTO recipe_keys(recipe_id, content_hash, render_hash) VALUES (?,?,?) ON CONFLICT(recipe_id) DO NOTHING`,
			r.id, r.content, r.render)
		if err != nil {
			return 0, fmt.Errorf("lineage: backfill: %w", err)
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	return n, tx.Commit()
}
