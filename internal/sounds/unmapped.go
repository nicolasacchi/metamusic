package sounds

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// replaceUnmapped replaces the cached unmapped style terms of a Song (C93.3).
func replaceUnmapped(ctx context.Context, tx *sql.Tx, songID string, terms []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM song_unmapped_terms WHERE song_id = ?`, songID); err != nil {
		return fmt.Errorf("sounds: clear unmapped: %w", err)
	}
	for _, t := range terms {
		if _, err := tx.ExecContext(ctx, `INSERT INTO song_unmapped_terms(song_id, term) VALUES(?, ?) ON CONFLICT DO NOTHING`, songID, t); err != nil {
			return fmt.Errorf("sounds: write unmapped: %w", err)
		}
	}
	return nil
}

// unmappedPage lists the most frequent unmapped terms over live Songs, by count descending then term; the cursor is
// "count\x1fterm" of the last row returned.
func (s *Service) unmappedPage(ctx context.Context, q TaxonomyQuery, limit int) (items []UnmappedTerm, next *string, err error) {
	args := []any{}
	where := `s.retired_at IS NULL AND s.hidden_at IS NULL`
	if f := NormTerm(q.Q); f != "" {
		where += ` AND instr(u.term, ?) > 0`
		args = append(args, f)
	}
	having := ""
	if q.Cursor != "" {
		parts, ok := decodeCursor(q.Cursor, 2)
		c, cerr := strconv.Atoi(parts[0])
		if !ok || cerr != nil {
			return nil, nil, badCursor()
		}
		t := parts[1]
		having = ` HAVING c < ? OR (c = ? AND u.term > ?)`
		args = append(args, c, c, t)
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT u.term, COUNT(*) AS c FROM song_unmapped_terms u JOIN songs s ON s.id = u.song_id
		WHERE `+where+` GROUP BY u.term`+having+` ORDER BY c DESC, u.term LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, nil, fmt.Errorf("sounds: unmapped: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var u UnmappedTerm
		if err := rows.Scan(&u.Term, &u.Songs); err != nil {
			return nil, nil, err
		}
		items = append(items, u)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		c := encodeCursor(strconv.Itoa(last.Songs), last.Term)
		next = &c
	}
	return items, next, nil
}
