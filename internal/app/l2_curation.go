package app

import (
	"context"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/songview"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// l2Curation reads the Records and manual Crates a Song or Take belongs to, for get_song (songview.Curation) and /context
// (trackctx.CurationReader). songview and trackctx cannot import records or crates (C118), so the adapter reads the tables
// through the reader pool: live slots of non-hidden Records and live items of non-hidden manual Crates. It depends only on the
// migrated tables, not on the Crates service, so it works the same before and after lane K.
type l2Curation struct{ st *store.Store }

// wireL2Curation hands the adapter to the two readers; it is the one line app.Build adds for lane V2.
func wireL2Curation(st *store.Store, sv *songview.Service, ctxb *trackctx.Builder) {
	c := l2Curation{st: st}
	sv.UseCuration(c)
	ctxb.UseCuration(c)
}

// RecordsOf lists the non-hidden Records with a live slot of the Song, newest first.
func (c l2Curation) RecordsOf(ctx context.Context, songID string, limit int) ([]songview.RecordRef, error) {
	rows, err := c.st.R.QueryContext(ctx, `SELECT r.id, r.title, r.status, CASE WHEN s.pinned_take_id IS NULL THEN 'floating' ELSE 'pinned' END
		FROM record_slots s JOIN records r ON r.id = s.record_id
		WHERE s.song_id = ? AND s.removed_at IS NULL AND r.hidden_at IS NULL ORDER BY r.created_at DESC, r.id DESC LIMIT ?`, songID, limit)
	if err != nil {
		return nil, fmt.Errorf("records of song: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []songview.RecordRef{}
	for rows.Next() {
		var r songview.RecordRef
		if err := rows.Scan(&r.ID, &r.Title, &r.Status, &r.Mode); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CratesOf lists the non-hidden manual Crates holding the Song, or a Song retired into it, newest first.
func (c l2Curation) CratesOf(ctx context.Context, songID string, limit int) ([]songview.CrateRef, error) {
	rows, err := c.st.R.QueryContext(ctx, `SELECT c.id, c.name FROM crates c
		WHERE c.kind = 'manual' AND c.hidden_at IS NULL AND EXISTS (SELECT 1 FROM crate_items ci WHERE ci.crate_id = c.id AND ci.removed_at IS NULL
		  AND (ci.song_id = ?1 OR ci.song_id IN (SELECT id FROM songs WHERE retired_into = ?1)))
		ORDER BY c.updated_at DESC, c.id DESC LIMIT ?2`, songID, limit)
	if err != nil {
		return nil, fmt.Errorf("crates of song: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []songview.CrateRef{}
	for rows.Next() {
		var r songview.CrateRef
		if err := rows.Scan(&r.ID, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// maxContextRefs bounds curation.records and curation.crates in /context (C130.4).
const maxContextRefs = 6

// ForTrack builds the `curation` section of a Take's /context: the Records whose slot resolves to this Take (position, mode)
// and the manual Crates holding the Take or its Song. nil when both are empty.
func (c l2Curation) ForTrack(ctx context.Context, trackID string) (map[string]any, error) {
	rrows, err := c.st.R.QueryContext(ctx, `SELECT r.id, r.title, r.status, CASE WHEN s.pinned_take_id IS NULL THEN 'floating' ELSE 'pinned' END,
		(SELECT j.key + 1 FROM json_each(r.order_json) j WHERE j.value = s.id)
		FROM record_slots s JOIN records r ON r.id = s.record_id LEFT JOIN song_picks sp ON sp.song_id = s.song_id
		WHERE s.removed_at IS NULL AND r.hidden_at IS NULL AND COALESCE(s.pinned_take_id, sp.take_id) = ?
		ORDER BY r.created_at DESC, r.id DESC LIMIT ?`, trackID, maxContextRefs)
	if err != nil {
		return nil, fmt.Errorf("records of take: %w", err)
	}
	recs := []map[string]any{}
	for rrows.Next() {
		var id, title, status, mode string
		var pos *int
		if err := rrows.Scan(&id, &title, &status, &mode, &pos); err != nil {
			_ = rrows.Close()
			return nil, err
		}
		recs = append(recs, map[string]any{"id": id, "title": title, "status": status, "position": pos, "mode": mode})
	}
	if err := rrows.Close(); err != nil {
		return nil, err
	}
	crows, err := c.st.R.QueryContext(ctx, `SELECT c.id, c.name FROM crates c
		WHERE c.kind = 'manual' AND c.hidden_at IS NULL AND EXISTS (SELECT 1 FROM crate_items ci WHERE ci.crate_id = c.id AND ci.removed_at IS NULL
		  AND (ci.track_id = ?1 OR ci.song_id IN (SELECT ts.song_id FROM take_songs ts WHERE ts.track_id = ?1)
		       OR ci.song_id IN (SELECT so.id FROM songs so WHERE so.retired_into IN (SELECT ts.song_id FROM take_songs ts WHERE ts.track_id = ?1))))
		ORDER BY c.updated_at DESC, c.id DESC LIMIT ?2`, trackID, maxContextRefs)
	if err != nil {
		return nil, fmt.Errorf("crates of take: %w", err)
	}
	crates := []map[string]any{}
	for crows.Next() {
		var id, name string
		if err := crows.Scan(&id, &name); err != nil {
			_ = crows.Close()
			return nil, err
		}
		crates = append(crates, map[string]any{"id": id, "name": name})
	}
	if err := crows.Close(); err != nil {
		return nil, err
	}
	if len(recs) == 0 && len(crates) == 0 {
		return nil, nil
	}
	return map[string]any{"records": recs, "crates": crates}, nil
}
