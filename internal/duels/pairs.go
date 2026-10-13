package duels

import (
	"context"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/ladder"
)

// PairStats counts verdicts on duels between exactly a and b, oriented so AWins counts wins of a (C48.7).
func (s *Service) PairStats(ctx context.Context, a, b string) (ladder.PairStats, error) {
	if s.unconfigured() {
		return ladder.PairStats{}, errors.ErrUnsupported
	}
	var out ladder.PairStats
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT v.source, d.blind, v.verdict, v.winner_track_id FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id
		WHERE (d.slot_a_track_id = ?1 AND d.slot_b_track_id = ?2) OR (d.slot_a_track_id = ?2 AND d.slot_b_track_id = ?1)`, a, b)
	if err != nil {
		return out, fmt.Errorf("duels: pair stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var src, verdict string
		var blind bool
		var winner *string
		if err := rows.Scan(&src, &blind, &verdict, &winner); err != nil {
			return out, fmt.Errorf("duels: pair stats: %w", err)
		}
		c := &out.Critic
		if src == "human" {
			c = &out.Human
			if blind {
				out.HumanBlindN++
			}
		}
		c.N++
		switch {
		case verdict == "tie":
			c.Ties++
		case verdict == "both_bad":
			c.BothBad++
		case winner != nil && *winner == a:
			c.AWins++
		default:
			c.BWins++
		}
	}
	return out, rows.Err()
}
