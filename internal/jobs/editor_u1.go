package jobs

import (
	"context"
	"fmt"
	"strings"
)

// BatchOwners maps batch ids to the principal key that created them; ids with no batch are left out. The API uses it to
// show an Editor the cost of its own batches and jobs and to hide it on everyone else's (C305, C310.5).
func (s *Service) BatchOwners(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	if err := s.wired(); err != nil {
		return nil, err
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT id, principal_id FROM batches WHERE id IN (`+ph+`)`, args...) //nolint:gosec // G202: only "?" placeholders are concatenated
	if err != nil {
		return nil, fmt.Errorf("jobs: batch owners: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, owner string
		if err := rows.Scan(&id, &owner); err != nil {
			return nil, err
		}
		out[id] = owner
	}
	return out, rows.Err()
}
