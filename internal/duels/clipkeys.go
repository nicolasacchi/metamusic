package duels

import (
	"context"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
)

// OpenClipKeys returns the clip keys of both slots of every open duel (unexpired, no
// human verdict), computed with the current tool version: these are the clip cache pins.
func (s *Service) OpenClipKeys(ctx context.Context) (map[string]struct{}, error) {
	if s.unconfigured() {
		return nil, errors.ErrUnsupported
	}
	out := map[string]struct{}{}
	if s.o.Clips == nil {
		return out, nil
	}
	tool := s.o.Clips.ToolVersion()
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT `+duelCols+` FROM duels d WHERE d.expires_at > ?
		AND NOT EXISTS (SELECT 1 FROM duel_verdicts h WHERE h.duel_id = d.id AND h.source = 'human')`, rfc(s.now()))
	if err != nil {
		return nil, fmt.Errorf("duels: open clip keys: %w", err)
	}
	var ds []duelRow
	for rows.Next() {
		d, err := scanDuel(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("duels: open clip keys: %w", err)
		}
		ds = append(ds, d)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var ids []string
	for _, d := range ds {
		ids = append(ids, d.SlotA, d.SlotB)
	}
	refs, err := s.o.Library.TrackRefs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		for i, id := range []string{d.SlotA, d.SlotB} {
			r, ok := refs[id]
			if !ok {
				continue
			}
			out[clip.Key(d.spec(r, i == 1), tool)] = struct{}{}
		}
	}
	return out, nil
}
