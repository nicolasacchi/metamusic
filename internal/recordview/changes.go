package recordview

import (
	"context"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/records"
)

// changeViews splices the Take titles (read now, never stored) and the text into changelog rows.
func changeViews(cs []records.Change, title func(takeID string) string) []ChangeView {
	out := make([]ChangeView, 0, len(cs))
	ref := func(id *string) *TakeRef {
		if id == nil {
			return nil
		}
		return &TakeRef{ID: *id, Title: title(*id)}
	}
	for _, c := range cs {
		out = append(out, ChangeView{Change: c, From: ref(c.FromTakeID), To: ref(c.ToTakeID), Text: records.ChangeText(c, title)})
	}
	return out
}

// takeIDsOf lists the Takes a page of rows names.
func takeIDsOf(cs []records.Change) []string {
	var ids []string
	for _, c := range cs {
		if c.FromTakeID != nil {
			ids = append(ids, *c.FromTakeID)
		}
		if c.ToTakeID != nil {
			ids = append(ids, *c.ToTakeID)
		}
	}
	return ids
}

// ChangeViews splices the Take titles (read now, never stored) and the text into changelog rows read by records.Service.Changes
// (get_record_changes); one title lookup however many rows.
func (s *Service) ChangeViews(ctx context.Context, cs []records.Change) ([]ChangeView, error) {
	titles := map[string]string{}
	if s.o.Library != nil {
		if ids := distinct(takeIDsOf(cs)); len(ids) > 0 {
			m, err := s.o.Library.TrackRefs(ctx, ids)
			if err != nil {
				return nil, fmt.Errorf("recordview: takes: %w", err)
			}
			for id, r := range m {
				titles[id] = r.Title
			}
		}
	}
	return changeViews(cs, func(id string) string { return titles[id] }), nil
}

// pending reports whether the changelog may lag (C122.6): a floating slot has a Pick event beyond its synced_seq, or a Take of
// a slot Song is still in the provenance queue. The resolved Takes themselves are never stale.
func (s *Service) pending(ctx context.Context, recordID string) (bool, error) {
	if s.o.Store == nil {
		return false, nil
	}
	var p bool
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM record_slots s WHERE s.record_id = ?1 AND s.removed_at IS NULL AND s.pinned_take_id IS NULL
		          AND EXISTS (SELECT 1 FROM pick_events e WHERE e.song_id = s.song_id AND e.seq > s.synced_seq))
		OR EXISTS (SELECT 1 FROM provenance_dirty d WHERE d.kind = 'track' AND d.id IN
		          (SELECT ts.track_id FROM take_songs ts WHERE ts.song_id IN (SELECT song_id FROM record_slots WHERE record_id = ?1 AND removed_at IS NULL)))`,
		recordID).Scan(&p)
	if err != nil {
		return false, fmt.Errorf("recordview: pending: %w", err)
	}
	return p, nil
}
