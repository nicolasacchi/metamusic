package records

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// likeEscape escapes the LIKE wildcards of a user string (ESCAPE '\').
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func encodeCursor(updatedAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(updatedAt + "|" + id))
}

func decodeCursor(c string) (updatedAt, id string, err error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", "", errInvalid("cursor", "not a cursor of this list")
	}
	updatedAt, id, ok := strings.Cut(string(b), "|")
	if !ok || updatedAt == "" || id == "" {
		return "", "", errInvalid("cursor", "not a cursor of this list")
	}
	return updatedAt, id, nil
}

// List returns Records ordered by (updated_at DESC, id DESC) with a keyset cursor. Q is a case-insensitive substring of
// the title (ASCII case folding, as SQLite LIKE); Total counts every match, whatever the cursor.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	var where []string
	var args []any
	if !f.IncludeHidden {
		where = append(where, `r.hidden_at IS NULL`)
	}
	if len(f.Statuses) > 0 {
		where = append(where, `r.status IN (`+placeholders(len(f.Statuses))+`)`)
		for _, st := range f.Statuses {
			args = append(args, string(st))
		}
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, `lower(r.title) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscape(strings.ToLower(q))+"%")
	}
	if f.SongID != "" {
		where = append(where, `EXISTS (SELECT 1 FROM record_slots s WHERE s.record_id = r.id AND s.song_id = ? AND s.removed_at IS NULL)`)
		args = append(args, f.SongID)
	}
	clause := ""
	if len(where) > 0 {
		clause = ` WHERE ` + strings.Join(where, ` AND `)
	}
	var page Page
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM records r`+clause, args...).Scan(&page.Total); err != nil {
		return Page{}, fmt.Errorf("records: count: %w", err)
	}
	pageClause, pageArgs := clause, append([]any(nil), args...)
	if f.Cursor != "" {
		at, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return Page{}, err
		}
		cur := `(r.updated_at, r.id) < (?, ?)`
		if pageClause == "" {
			pageClause = ` WHERE ` + cur
		} else {
			pageClause += ` AND ` + cur
		}
		pageArgs = append(pageArgs, at, id)
	}
	//nolint:gosec // G202: pageClause is built from fixed fragments with "?" placeholders only
	rows, err := s.st.R.QueryContext(ctx, `SELECT r.id, r.title, r.subtitle, r.status, r.revision, r.updated_at, r.hidden_at IS NOT NULL
		FROM records r`+pageClause+` ORDER BY r.updated_at DESC, r.id DESC LIMIT ?`, append(pageArgs, limit+1)...)
	if err != nil {
		return Page{}, fmt.Errorf("records: list: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sm Summary
		if err := rows.Scan(&sm.ID, &sm.Title, &sm.Subtitle, &sm.Status, &sm.Revision, &sm.UpdatedAt, &sm.Hidden); err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, sm)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	rows.Close()
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		c := encodeCursor(last.UpdatedAt, last.ID)
		page.NextCursor = &c
	}
	if err := s.fillSummaries(ctx, page.Items); err != nil {
		return Page{}, err
	}
	return page, nil
}

// fillSummaries adds slot counts, playable counts, duration and warnings of a page in one statement. A slot is playable
// when it resolves to a Take that exists and is not discarded (state ok); the warning count follows ResolveSlot, except
// that undecided_pick counts for published Records only (C121.1).
func (s *Service) fillSummaries(ctx context.Context, items []Summary) error {
	if len(items) == 0 {
		return nil
	}
	idx := make(map[string]int, len(items))
	args := make([]any, 0, len(items))
	for i, it := range items {
		idx[it.ID] = i
		args = append(args, it.ID)
	}
	//nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := s.st.R.QueryContext(ctx, `SELECT s.record_id, COUNT(*),
		SUM(t.id IS NOT NULL AND t.status <> 'discarded'),
		COALESCE(SUM(CASE WHEN t.status <> 'discarded' THEN t.duration_s END), 0),
		SUM((s.pinned_take_id IS NOT NULL AND t.status = 'discarded')
		  + (s.pinned_take_id IS NULL AND t.id IS NULL)
		  + (so.retired_at IS NOT NULL)
		  + (r.status = 'published' AND s.pinned_take_id IS NULL AND t.id IS NOT NULL AND sp.source IN ('newest', 'stars')))
		FROM record_slots s
		JOIN records r ON r.id = s.record_id
		JOIN songs so ON so.id = s.song_id
		LEFT JOIN song_picks sp ON sp.song_id = s.song_id
		LEFT JOIN tracks t ON t.id = COALESCE(s.pinned_take_id, sp.take_id)
		WHERE s.record_id IN (`+placeholders(len(args))+`) AND s.removed_at IS NULL
		GROUP BY s.record_id`, args...)
	if err != nil {
		return fmt.Errorf("records: summaries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var slots, playable, warnings int
		var dur float64
		if err := rows.Scan(&id, &slots, &playable, &dur, &warnings); err != nil {
			return err
		}
		it := &items[idx[id]]
		it.Slots, it.Playable, it.DurationS, it.Warnings = slots, playable, dur, warnings
	}
	return rows.Err()
}
