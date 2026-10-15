package muses

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// maxPageBytes caps the serialised size of a list page (C17); summaryOverhead reserves room for the coverage counts
// songview adds to each row and for the page envelope.
const (
	maxPageBytes    = 90000
	summaryOverhead = 160
	envelopeBytes   = 256
)

type cursor struct {
	Fold string `json:"f"`
	ID   string `json:"i"`
}

func encodeCursor(fold, id string) string {
	b, _ := json.Marshal(cursor{fold, id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(c string) (cursor, error) {
	var cur cursor
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || json.Unmarshal(b, &cur) != nil || cur.ID == "" {
		return cursor{}, problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "malformed cursor", map[string]any{"field": "cursor"})
	}
	return cur, nil
}

// ftsQuery turns user text into one FTS5 phrase-prefix expression: only letters and digits survive, so no FTS syntax
// (NEAR, quotes, operators, column filters) can reach the engine. "" means no text filter.
func ftsQuery(q string) string {
	toks := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(toks) == 0 {
		return ""
	}
	return `"` + strings.Join(toks, " ") + `" *`
}

// List pages Muses (C89.1): rows only, ordered by (title_fold, id).
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	where := []string{"1 = 1"}
	var args []any
	if !f.IncludeHidden {
		where = append(where, "m.hidden_at IS NULL")
	}
	if f.Kind != "" {
		where = append(where, "m.kind = ?")
		args = append(args, string(f.Kind))
	}
	if q := ftsQuery(f.Q); q != "" {
		where = append(where, "m.id IN (SELECT muse_id FROM muses_fts WHERE muses_fts MATCH ?)")
		args = append(args, q)
	}
	base := strings.Join(where, " AND ")
	var page Page
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM muses m WHERE `+base, args...).Scan(&page.Total); err != nil {
		return Page{}, fmt.Errorf("muses: list: %w", err)
	}
	pageWhere, pageArgs := base, append([]any(nil), args...)
	if f.Cursor != "" {
		cur, err := decodeCursor(f.Cursor)
		if err != nil {
			return Page{}, err
		}
		pageWhere += " AND (m.title_fold > ? OR (m.title_fold = ? AND m.id > ?))"
		pageArgs = append(pageArgs, cur.Fold, cur.Fold, cur.ID)
	}
	pageArgs = append(pageArgs, limit+1)
	rows, err := s.st.R.QueryContext(ctx, `SELECT `+mcols+`, m.title_fold FROM muses m WHERE `+pageWhere+` ORDER BY m.title_fold, m.id LIMIT ?`, pageArgs...) //nolint:gosec // G202: fragments are fixed strings built in this function
	if err != nil {
		return Page{}, fmt.Errorf("muses: list: %w", err)
	}
	defer rows.Close()
	size := envelopeBytes
	var lastFold string
	more := false
	for rows.Next() {
		var m Muse
		var fold string
		var artist, brief, hidden nullString
		if err := rows.Scan(&m.ID, &m.Kind, &m.Title, &artist, &brief, &m.Source, &m.CreatedBy, &m.CreatedAt, &m.UpdatedBy, &m.UpdatedAt, &m.Revision, &hidden, &fold); err != nil {
			return Page{}, err
		}
		m.Artist, m.Brief, m.HiddenAt = artist.ptr(), brief.ptr(), hidden.ptr()
		if len(page.Items) >= limit {
			more = true
			break
		}
		b, _ := json.Marshal(m)
		if len(page.Items) > 0 && size+len(b)+summaryOverhead+1 > maxPageBytes {
			more = true
			break
		}
		size += len(b) + summaryOverhead + 1
		page.Items = append(page.Items, Summary{m})
		lastFold = fold
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if more && len(page.Items) > 0 {
		c := encodeCursor(lastFold, page.Items[len(page.Items)-1].ID)
		page.NextCursor = &c
	}
	return page, nil
}

// mcols is museCols qualified with the list alias.
var mcols = "m." + strings.ReplaceAll(museCols, ", ", ", m.")
