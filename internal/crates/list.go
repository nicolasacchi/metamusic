package crates

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

type cursor struct {
	At string `json:"c"`
	ID string `json:"i"`
}

func decodeCursor(c string) (cursor, error) {
	var cur cursor
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || json.Unmarshal(b, &cur) != nil || cur.ID == "" {
		return cursor{}, problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "malformed cursor", map[string]any{"field": "cursor"})
	}
	return cur, nil
}

func encodeCursor(at, id string) string {
	b, _ := json.Marshal(cursor{at, id})
	return base64.RawURLEncoding.EncodeToString(b)
}

// List returns Crates ordered by (updated_at DESC, id DESC) with a keyset cursor. Manual counts are always filled, smart
// counts only with f.Counts.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	where, args := []string{"1 = 1"}, []any{}
	if !f.IncludeHidden {
		where = append(where, "hidden_at IS NULL")
	}
	if f.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, string(f.Kind))
	}
	if f.Subject != "" {
		where, args = append(where, "subject = ?"), append(args, string(f.Subject))
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
		where, args = append(where, `name LIKE ? ESCAPE '\'`), append(args, "%"+esc+"%")
	}
	base := strings.Join(where, " AND ")
	var page Page
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM crates WHERE `+base, args...).Scan(&page.Total); err != nil { //nolint:gosec // fixed fragments
		return Page{}, fmt.Errorf("crates: count: %w", err)
	}
	page.Items = []Summary{}
	qargs := append([]any{}, args...)
	paged := base
	if f.Cursor != "" {
		cur, err := decodeCursor(f.Cursor)
		if err != nil {
			return Page{}, err
		}
		paged += ` AND (updated_at < ? OR (updated_at = ? AND id < ?))`
		qargs = append(qargs, cur.At, cur.At, cur.ID)
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT `+crateColumns+` FROM crates WHERE `+paged+` ORDER BY updated_at DESC, id DESC LIMIT ?`, append(qargs, limit+1)...) //nolint:gosec // fixed fragments
	if err != nil {
		return Page{}, fmt.Errorf("crates: list: %w", err)
	}
	var crates []Crate
	for rows.Next() {
		c, err := scanCrate(rows)
		if err != nil {
			rows.Close()
			return Page{}, err
		}
		crates = append(crates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Page{}, err
	}
	rows.Close()
	if len(crates) > limit {
		crates = crates[:limit]
		last := crates[len(crates)-1]
		next := encodeCursor(last.UpdatedAt, last.ID)
		page.NextCursor = &next
	}
	for _, c := range crates {
		sum := Summary{Crate: c}
		if c.Kind == KindManual || f.Counts {
			n, err := s.count(ctx, c)
			if err != nil {
				return Page{}, err
			}
			sum.Count = &n
		}
		page.Items = append(page.Items, sum)
	}
	return page, nil
}
