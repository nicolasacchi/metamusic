package duels

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// Next returns the oldest duel waiting for p's source (C48.5). It writes nothing.
func (s *Service) Next(ctx context.Context, p auth.Principal, exclude []string) (NextResult, error) {
	if s.unconfigured() {
		return NextResult{}, errors.ErrUnsupported
	}
	if s.o.Clips == nil {
		return NextResult{}, unavailable("clips")
	}
	src, err := canJudge(p)
	if err != nil {
		return NextResult{}, err
	}
	if len(exclude) > maxExclude {
		return NextResult{}, invalid("exclude", fmt.Sprintf("at most %d duel ids can be excluded", maxExclude))
	}
	now := rfc(s.now())
	where := `d.expires_at > ? AND NOT EXISTS (SELECT 1 FROM duel_verdicts h WHERE h.duel_id = d.id AND h.source = 'human')`
	args := []any{now}
	if src == "critic" {
		where += ` AND NOT EXISTS (SELECT 1 FROM duel_verdicts c WHERE c.duel_id = d.id AND c.source = 'critic' AND c.principal_id = ?)`
		args = append(args, p.Key())
	}
	var res NextResult
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT count(*) FROM duels d WHERE `+where, args...).Scan(&res.OpenTotal); err != nil {
		return NextResult{}, fmt.Errorf("duels: next count: %w", err)
	}
	q := `SELECT ` + duelCols + ` FROM duels d WHERE ` + where
	if len(exclude) > 0 {
		q += ` AND d.id NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(exclude)), ",") + `)` //nolint:gosec // G202: only "?" placeholders are concatenated
		for _, e := range exclude {
			args = append(args, e)
		}
	}
	q += ` ORDER BY d.created_at, d.id LIMIT 1`
	rows, err := s.o.Store.R.QueryContext(ctx, q, args...)
	if err != nil {
		return NextResult{}, fmt.Errorf("duels: next: %w", err)
	}
	if !rows.Next() {
		err := rows.Err()
		_ = rows.Close()
		return res, err
	}
	d, err := scanDuel(rows)
	_ = rows.Close()
	if err != nil {
		return NextResult{}, fmt.Errorf("duels: next: %w", err)
	}
	vs, err := s.loadVerdicts(ctx, d.ID)
	if err != nil {
		return NextResult{}, err
	}
	view, err := s.view(ctx, p, d, vs[d.ID], nil, false)
	if err != nil {
		return NextResult{}, err
	}
	o := DuelOrNull(view)
	res.Duel = &o
	return res, nil
}
