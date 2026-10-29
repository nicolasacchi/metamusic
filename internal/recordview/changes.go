package recordview

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/records"
)

// readChanges reads one page of a Record's changelog straight from record_changes (a read of the append-only table, which
// is the same data records.Service.Changes pages over). The page holds at most f.Limit rows; next is the seq to continue from.
func (s *Service) readChanges(ctx context.Context, f records.ChangeFilter) (items []records.Change, next *int, err error) {
	if s.o.Store == nil {
		return nil, nil, nil
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, records.MaxChangesPage)
	desc := f.Order != "asc"
	q := `SELECT id, seq, at, kind, slot_id, position, from_take_id, to_take_id, reason, pick_event_id, evidence_json, detail_json,
		actor, actor_kind, actor_name FROM record_changes WHERE record_id = ?`
	args := []any{f.RecordID}
	if f.Cursor > 0 {
		if desc {
			q += ` AND seq < ?`
		} else {
			q += ` AND seq > ?`
		}
		args = append(args, f.Cursor)
	}
	if len(f.Kinds) > 0 {
		q += ` AND kind IN (` + placeholders(len(f.Kinds)) + `)` //nolint:gosec // G202: only "?" placeholders are concatenated
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if desc {
		q += ` ORDER BY seq DESC`
	} else {
		q += ` ORDER BY seq ASC`
	}
	q += ` LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.o.Store.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("recordview: changes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c records.Change
		var slot, from, to, reason, event, ev, det sql.NullString
		var pos sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Seq, &c.At, &c.Kind, &slot, &pos, &from, &to, &reason, &event, &ev, &det, &c.Actor, &c.ActorKind, &c.ActorName); err != nil {
			return nil, nil, err
		}
		c.SlotID, c.FromTakeID, c.ToTakeID, c.Reason, c.PickEventID = nstr(slot), nstr(from), nstr(to), nstr(reason), nstr(event)
		if pos.Valid {
			n := int(pos.Int64)
			c.Position = &n
		}
		if ev.Valid {
			c.Evidence = json.RawMessage(ev.String)
		}
		if det.Valid {
			c.Detail = json.RawMessage(det.String)
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(items) > limit {
		items = items[:limit]
		n := items[limit-1].Seq
		next = &n
	}
	return items, next, nil
}

func nstr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.String
	return &s
}

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

// Changes returns one page of a Record's changelog with the titles at read time and the text of each row (get_record_changes).
// An unknown Record is the 404 of records.Service.Get. next is the seq the following page continues from.
func (s *Service) Changes(ctx context.Context, f records.ChangeFilter) (items []ChangeView, next *int, err error) {
	if _, err := s.o.Records.Get(ctx, f.RecordID); err != nil {
		return nil, nil, err
	}
	cs, next, err := s.readChanges(ctx, f)
	if err != nil {
		return nil, nil, err
	}
	var refs = map[string]string{}
	if s.o.Library != nil {
		if ids := distinct(takeIDsOf(cs)); len(ids) > 0 {
			m, err := s.o.Library.TrackRefs(ctx, ids)
			if err != nil {
				return nil, nil, fmt.Errorf("recordview: takes: %w", err)
			}
			for id, r := range m {
				refs[id] = r.Title
			}
		}
	}
	return changeViews(cs, func(id string) string { return refs[id] }), next, nil
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
