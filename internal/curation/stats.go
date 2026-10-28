package curation

import (
	"context"
	"fmt"
)

// Stats counts Records, Crates, tapes, pending slots and the review queue for get_stats (C130.5, C127.5): plain COUNTs over
// the curation tables, two statements and no Muse text. Records and Crates include hidden ones; Pending counts live floating
// slots whose Song has Pick events the slot has not consumed (a pinned slot's synced_seq is never advanced, so it is left
// out); Review counts live slots of published Records whose floating Take came from a Pick no human judged (newest or
// stars) and those pinned to a Take that was discarded or is gone.
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	if err := s.st.R.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM records),
		(SELECT COUNT(*) FROM records WHERE status = 'draft'),
		(SELECT COUNT(*) FROM records WHERE status = 'proposed'),
		(SELECT COUNT(*) FROM records WHERE status = 'published'),
		(SELECT COUNT(*) FROM crates WHERE kind = 'manual'),
		(SELECT COUNT(*) FROM crates WHERE kind = 'smart'),
		(SELECT COUNT(*) FROM tapes WHERE state = 'queued'),
		(SELECT COUNT(*) FROM tapes WHERE state = 'running'),
		(SELECT COUNT(*) FROM tapes WHERE state = 'ready'),
		(SELECT COUNT(*) FROM tapes WHERE state = 'failed'),
		(SELECT COUNT(*) FROM record_slots sl WHERE sl.removed_at IS NULL AND sl.pinned_take_id IS NULL
			AND EXISTS (SELECT 1 FROM pick_events e WHERE e.song_id = sl.song_id AND e.seq > sl.synced_seq))`).
		Scan(&st.Records.Total, &st.Records.Draft, &st.Records.Proposed, &st.Records.Published,
			&st.Crates.Manual, &st.Crates.Smart,
			&st.Tapes.Queued, &st.Tapes.Running, &st.Tapes.Ready, &st.Tapes.Failed, &st.Pending); err != nil {
		return Stats{}, fmt.Errorf("curation: stats: %w", err)
	}
	if err := s.st.R.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(sl.pinned_take_id IS NULL AND sp.source IN ('newest', 'stars') AND sp.take_id IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM tracks k WHERE k.id = sp.take_id AND k.status = 'discarded')), 0),
		COALESCE(SUM(sl.pinned_take_id IS NOT NULL AND COALESCE((SELECT k.status FROM tracks k WHERE k.id = sl.pinned_take_id), 'discarded') = 'discarded'), 0)
		FROM record_slots sl JOIN records r ON r.id = sl.record_id AND r.status = 'published'
		LEFT JOIN song_picks sp ON sp.song_id = sl.song_id
		WHERE sl.removed_at IS NULL`).Scan(&st.Review.Undecided, &st.Review.PinIneligible); err != nil {
		return Stats{}, fmt.Errorf("curation: stats review: %w", err)
	}
	set, err := s.Get(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("curation: stats settings: %w", err)
	}
	st.OptIns = OptInStats{ExportMuseNames: set.ExportMuseNames, SubsonicMuseNames: set.SubsonicMuseNames}
	return st, nil
}
