package recordview

import (
	"context"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/records"
)

// partOf is the mosaic tile of a Song: its lineage root and seed, never its current Take, so a Pick flip cannot move a
// cover (C123). A takes-less draft Song contributes its draft id; an unknown Song its own id.
func partOf(songID string, b SongBrief, known bool) cover.Part {
	switch {
	case known && b.RootTrackID != "":
		return cover.Part{RootID: b.RootTrackID, Seed: b.RootSeed}
	case known && b.DraftID != nil:
		return cover.Part{RootID: *b.DraftID}
	}
	return cover.Part{RootID: songID}
}

// coverInput builds the mosaic input from the Record row and its first CoverParts Songs, in slot order.
func coverInput(rec records.Record, songIDs []string, briefs map[string]SongBrief) cover.Input {
	in := cover.Input{Kind: cover.KindRecord, ID: rec.ID, Seed: rec.CoverSeed}
	for _, id := range songIDs[:min(len(songIDs), records.CoverParts)] {
		b, ok := briefs[id]
		in.Parts = append(in.Parts, partOf(id, b, ok))
	}
	return in
}

// firstSongs returns the Song ids of the first CoverParts slots.
func firstSongs(rs []records.ResolvedSlot) []string {
	var out []string
	for _, r := range rs[:min(len(rs), records.CoverParts)] {
		out = append(out, r.Slot.SongID)
	}
	return out
}

// CoverInput builds the mosaic input of a Record: its id and seed, and the lineage of the first CoverParts slot Songs.
func (s *Service) CoverInput(ctx context.Context, recordID string) (cover.Input, error) {
	rec, err := s.o.Records.Get(ctx, recordID)
	if err != nil {
		return cover.Input{}, err
	}
	slots, err := s.o.Records.Slots(ctx, recordID)
	if err != nil {
		return cover.Input{}, err
	}
	var ids []string
	for _, sl := range slots[:min(len(slots), records.CoverParts)] {
		ids = append(ids, sl.SongID)
	}
	briefs := map[string]SongBrief{}
	if s.o.Songs != nil && len(ids) > 0 {
		if briefs, err = s.o.Songs.Briefs(ctx, distinct(ids)); err != nil {
			return cover.Input{}, fmt.Errorf("recordview: songs: %w", err)
		}
	}
	return coverInput(rec, ids, briefs), nil
}

// links renders and signs the cover; a cover failure never fails a View (the zero CoverRef means "no cover yet"), only a
// cancelled context does.
func (s *Service) links(ctx context.Context, in cover.Input) (CoverRef, error) {
	if s.o.Covers == nil {
		return CoverRef{}, nil
	}
	ref, err := s.o.Covers.Links(ctx, in)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return CoverRef{}, cerr
		}
		return CoverRef{}, nil
	}
	return ref, nil
}

// CoverRefs returns the signed cover links of Records for the list rows, in one batch: one statement reads every Record's
// seed and first Songs, one reads the Songs' lineage.
func (s *Service) CoverRefs(ctx context.Context, viewer auth.Principal, ids []string) (map[string]CoverRef, error) {
	out := make(map[string]CoverRef, len(ids))
	ids = distinct(ids)
	if len(ids) == 0 || s.o.Store == nil || s.o.Covers == nil {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	//nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT r.id, r.cover_seed, s.song_id FROM records r, json_each(r.order_json) j
		JOIN record_slots s ON s.id = j.value WHERE r.id IN (`+placeholders(len(ids))+`) AND j.key < ? ORDER BY r.id, j.key`, append(args, records.CoverParts)...)
	if err != nil {
		return nil, fmt.Errorf("recordview: cover rows: %w", err)
	}
	recs := map[string]*records.Record{}
	songsOf := map[string][]string{}
	var all []string
	for rows.Next() {
		var id, song string
		var seed int64
		if err := rows.Scan(&id, &seed, &song); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if recs[id] == nil {
			recs[id] = &records.Record{ID: id, CoverSeed: seed}
		}
		songsOf[id] = append(songsOf[id], song)
		all = append(all, song)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// a Record with no slots has no row above: its seed is read from the Record row
	var missing []any
	for _, id := range ids {
		if recs[id] == nil {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		//nolint:gosec // G202: only "?" placeholders are concatenated
		mrows, err := s.o.Store.R.QueryContext(ctx, `SELECT id, cover_seed FROM records WHERE id IN (`+placeholders(len(missing))+`)`, missing...)
		if err != nil {
			return nil, fmt.Errorf("recordview: cover rows: %w", err)
		}
		for mrows.Next() {
			var id string
			var seed int64
			if err := mrows.Scan(&id, &seed); err != nil {
				_ = mrows.Close()
				return nil, err
			}
			recs[id] = &records.Record{ID: id, CoverSeed: seed}
		}
		if err := mrows.Close(); err != nil {
			return nil, err
		}
	}
	briefs := map[string]SongBrief{}
	if s.o.Songs != nil && len(all) > 0 {
		if briefs, err = s.o.Songs.Briefs(ctx, distinct(all)); err != nil {
			return nil, fmt.Errorf("recordview: songs: %w", err)
		}
	}
	for _, id := range ids {
		rec := recs[id]
		if rec == nil {
			continue
		}
		ref, err := s.links(ctx, coverInput(*rec, songsOf[id], briefs))
		if err != nil {
			return nil, err
		}
		out[id] = ref
	}
	return out, nil
}
