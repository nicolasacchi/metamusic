package records

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// maxChangesBytes caps one page of the changelog (C122.4); a page always holds at least one row.
const maxChangesBytes = 90_000

const defaultChangesLimit = 50

// Changes returns one page of a Record's changelog (C122.4). Cursor is the seq of the last row of the previous page,
// exclusive; the direction follows Order (desc by default). NextCursor is set when more rows remain.
func (s *Service) Changes(ctx context.Context, f ChangeFilter) (ChangePage, error) {
	switch f.Order {
	case "", "desc", "asc":
	default:
		return ChangePage{}, errInvalid("order", "order must be asc or desc")
	}
	if f.Limit < 0 {
		return ChangePage{}, errInvalid("limit", "limit must be 1 to 200")
	}
	if f.Cursor < 0 {
		return ChangePage{}, errInvalid("cursor", "cursor is a changelog seq")
	}
	limit := f.Limit
	if limit == 0 {
		limit = defaultChangesLimit
	}
	limit = min(limit, MaxChangesPage)
	for _, k := range f.Kinds {
		if !slices.Contains(Kinds, k) {
			return ChangePage{}, errInvalid("kinds", "unknown change kind "+k)
		}
	}
	if err := recordExists(ctx, s.st.R, f.RecordID); err != nil {
		return ChangePage{}, err
	}
	asc := f.Order == "asc"
	q := `SELECT id, seq, at, kind, slot_id, position, from_take_id, to_take_id, reason, pick_event_id, evidence_json, detail_json, actor, actor_kind, actor_name
		FROM record_changes WHERE record_id = ?`
	args := []any{f.RecordID}
	if f.Cursor > 0 {
		if asc {
			q += ` AND seq > ?`
		} else {
			q += ` AND seq < ?`
		}
		args = append(args, f.Cursor)
	}
	if len(f.Kinds) > 0 {
		q += ` AND kind IN (` + placeholders(len(f.Kinds)) + `)` //nolint:gosec // placeholders only
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if asc {
		q += ` ORDER BY seq ASC LIMIT ?`
	} else {
		q += ` ORDER BY seq DESC LIMIT ?`
	}
	rows, err := s.st.R.QueryContext(ctx, q, append(args, limit+1)...)
	if err != nil {
		return ChangePage{}, fmt.Errorf("records: read changes: %w", err)
	}
	defer rows.Close()
	var page ChangePage
	size, more := 0, false
	for rows.Next() {
		var c Change
		var ev, det sql.NullString
		if err := rows.Scan(&c.ID, &c.Seq, &c.At, &c.Kind, &c.SlotID, &c.Position, &c.FromTakeID, &c.ToTakeID, &c.Reason, &c.PickEventID, &ev, &det,
			&c.Actor, &c.ActorKind, &c.ActorName); err != nil {
			return ChangePage{}, err
		}
		if ev.Valid {
			c.Evidence = []byte(ev.String)
		}
		if det.Valid {
			c.Detail = []byte(det.String)
		}
		n := rowSize(c)
		if len(page.Items) >= limit || (len(page.Items) > 0 && size+n > maxChangesBytes) {
			more = true
			break
		}
		size += n
		page.Items = append(page.Items, c)
	}
	if err := rows.Err(); err != nil {
		return ChangePage{}, err
	}
	if more {
		next := page.Items[len(page.Items)-1].Seq
		page.NextCursor = &next
	}
	return page, nil
}

// rowSize is the wire size estimate of one row: its text columns plus a constant for the keys and ids.
func rowSize(c Change) int {
	n := 400 + len(c.Evidence) + len(c.Detail) + len(c.ActorName) + len(c.Actor)
	if c.SlotID != nil {
		n += 40
	}
	return n + len(strings.TrimSpace(c.Kind))
}
