package callbacks

import (
	"context"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

var _ generation.BacklogGate = (*Service)(nil)

// Backlog counts the callbacks of the lineage root that no human has answered yet (C255.5): the callbacks whose child take shares
// the root. The human's answers pace agent spend; generation asks through the injected BacklogGate and never imports this package.
func (s *Service) Backlog(ctx context.Context, q lineage.Querier, rootTrackID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM callbacks c JOIN tracks t ON t.id = c.child_track_id
		WHERE t.root_track_id = ? AND NOT EXISTS (SELECT 1 FROM callback_verdicts v WHERE v.callback_id = c.id AND v.source = 'human')`, rootTrackID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("callbacks: backlog: %w", err)
	}
	return n, nil
}

// Limit is MM_CALLBACK_BACKLOG_MAX; 0 switches the gate off.
func (s *Service) Limit() int {
	if s == nil {
		return 0
	}
	return s.o.BacklogMax
}
