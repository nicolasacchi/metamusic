package records

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// PublishedRecord is a published, non-hidden Record with its live slots resolved, in slot order.
type PublishedRecord struct {
	Record Record
	Slots  []PublishedSlot
}

// PublishedSlot is a resolved slot plus the lineage the cover is drawn from.
type PublishedSlot struct {
	ResolvedSlot
	// RootID is the Song's primary root Take (a takes-less draft Song contributes its draft id); RootSeed is that root's
	// recipe seed (0 for a draft). Neither changes when a Pick does, so a cover built from them is stable (D-L2-9).
	RootID   string
	RootSeed int64
}

// ResolvedPublished reads every published, non-hidden Record and its live slots with their Takes resolved, ordered by
// (first_published_at, id) with the slots in order_json order. It is the set-based read behind the catalog (C126.1):
// two statements however many Records there are. A slot id that order_json does not list is left out (Verify reports it).
func (s *Service) ResolvedPublished(ctx context.Context) ([]PublishedRecord, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT id, title, subtitle, liner_note, cover_seed, cover_rerolls, status, order_json,
		proposal_note, proposed_by, proposed_at, published_by, published_at, first_published_at, revision,
		created_by, created_at, updated_by, updated_at, hidden_at
		FROM records WHERE status = 'published' AND hidden_at IS NULL ORDER BY first_published_at, id`)
	if err != nil {
		return nil, fmt.Errorf("records: published: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PublishedRecord
	orders := map[string][]string{}
	for rows.Next() {
		var r Record
		var order string
		var proposalNote, proposedBy, proposedAt, publishedBy, publishedAt, firstPub, hidden sql.NullString
		if err := rows.Scan(&r.ID, &r.Title, &r.Subtitle, &r.LinerNote, &r.CoverSeed, &r.CoverRerolls, &r.Status, &order,
			&proposalNote, &proposedBy, &proposedAt, &publishedBy, &publishedAt, &firstPub, &r.Revision,
			&r.CreatedBy, &r.CreatedAt, &r.UpdatedBy, &r.UpdatedAt, &hidden); err != nil {
			return nil, fmt.Errorf("records: published: %w", err)
		}
		r.ProposalNote, r.ProposedBy, r.ProposedAt = pubStr(proposalNote), pubStr(proposedBy), pubStr(proposedAt)
		r.PublishedBy, r.PublishedAt, r.FirstPublishedAt, r.HiddenAt = pubStr(publishedBy), pubStr(publishedAt), pubStr(firstPub), pubStr(hidden)
		var ids []string
		if err := json.Unmarshal([]byte(order), &ids); err != nil {
			return nil, fmt.Errorf("records: order of %s: %w", r.ID, err)
		}
		orders[r.ID] = ids
		out = append(out, PublishedRecord{Record: r})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("records: published: %w", err)
	}
	if len(out) == 0 {
		return out, nil
	}

	sr, err := s.st.R.QueryContext(ctx, `SELECT sl.id, sl.record_id, sl.song_id, sl.side, sl.pinned_take_id, sl.pinned_by, sl.pinned_at,
		sl.synced_seq, sl.added_by, sl.added_at, g.retired_at IS NOT NULL,
		COALESCE(g.root_track_id, g.draft_id, ''), COALESCE(rc.seed, 0),
		sp.take_id, sp.source,
		pt.id, pt.status, pt.duration_s, pt.lossy,
		kt.id, kt.status, kt.duration_s, kt.lossy
		FROM record_slots sl
		JOIN records r ON r.id = sl.record_id AND r.status = 'published' AND r.hidden_at IS NULL
		JOIN songs g ON g.id = sl.song_id
		LEFT JOIN tracks rt ON rt.id = g.root_track_id
		LEFT JOIN recipes rc ON rc.id = rt.recipe_id
		LEFT JOIN song_picks sp ON sp.song_id = sl.song_id
		LEFT JOIN tracks pt ON pt.id = sl.pinned_take_id
		LEFT JOIN tracks kt ON kt.id = sp.take_id
		WHERE sl.removed_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("records: published slots: %w", err)
	}
	defer func() { _ = sr.Close() }()
	slots := map[string]PublishedSlot{}
	for sr.Next() {
		var sl Slot
		var retired bool
		var side, pinnedTake, pinnedBy, pinnedAt, pickTake, pickSource sql.NullString
		var root string
		var seed int64
		var pt, kt pubTake
		if err := sr.Scan(&sl.ID, &sl.RecordID, &sl.SongID, &side, &pinnedTake, &pinnedBy, &pinnedAt, &sl.SyncedSeq, &sl.AddedBy, &sl.AddedAt, &retired,
			&root, &seed, &pickTake, &pickSource,
			&pt.id, &pt.status, &pt.duration, &pt.lossy, &kt.id, &kt.status, &kt.duration, &kt.lossy); err != nil {
			return nil, fmt.Errorf("records: published slots: %w", err)
		}
		sl.Side, sl.PinnedTakeID, sl.PinnedBy, sl.PinnedAt = pubStr(side), pubStr(pinnedTake), pubStr(pinnedBy), pubStr(pinnedAt)
		sl.SongRetired = retired
		pick := PickInput{TakeID: pubStr(pickTake), Source: pickSource.String}
		if !pickSource.Valid {
			pick.Source = "none"
		}
		slots[sl.ID] = PublishedSlot{ResolvedSlot: ResolveSlot(sl, pt.info(), pick, kt.info()), RootID: root, RootSeed: seed}
	}
	if err := sr.Err(); err != nil {
		return nil, fmt.Errorf("records: published slots: %w", err)
	}
	for i := range out {
		for pos, id := range orders[out[i].Record.ID] {
			ps, ok := slots[id]
			if !ok || ps.Slot.RecordID != out[i].Record.ID {
				continue
			}
			ps.Slot.Position = pos + 1
			out[i].Slots = append(out[i].Slots, ps)
		}
	}
	return out, nil
}

// pubTake is a LEFT JOINed tracks row.
type pubTake struct {
	id, status sql.NullString
	duration   sql.NullFloat64
	lossy      sql.NullBool
}

func (t pubTake) info() *TakeInfo {
	if !t.id.Valid {
		return nil
	}
	return &TakeInfo{ID: t.id.String, Status: t.status.String, DurationS: t.duration.Float64, Lossy: t.lossy.Bool}
}

func pubStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}
