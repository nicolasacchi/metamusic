package songs

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const (
	maxPageBytes  = 90000
	envelopeBytes = 256
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

// SummaryColumns selects the Summary of one Song (aliases: songs s, state table st, song_picks p); the order matches ScanSummary.
const SummaryColumns = `s.id, s.title, s.angle, s.muse_id, s.draft_id, s.root_track_id, ` + store.SongStateCase + `, st.takes, p.take_id, p.source,
	st.queued_jobs, st.open_proposals, s.hidden_at IS NOT NULL, st.archived, s.updated_at`

// SummaryFrom is the FROM clause SummaryColumns needs.
const SummaryFrom = `FROM songs s JOIN (` + store.SongStateSQL + `) st ON st.song_id = s.id LEFT JOIN song_picks p ON p.song_id = s.id`

// ScanSummary reads one row of SummaryColumns; extra receives further columns scanned after them.
func ScanSummary(r interface{ Scan(...any) error }, extra ...any) (Summary, error) {
	var s Summary
	var angle, muse, draft, root, pickTake, pickSource sql.NullString
	var state string
	var hidden, archived bool
	dest := append([]any{&s.ID, &s.Title, &angle, &muse, &draft, &root, &state, &s.Takes, &pickTake, &pickSource, &s.QueuedJobs, &s.OpenProposals, &hidden, &archived, &s.UpdatedAt}, extra...)
	if err := r.Scan(dest...); err != nil {
		return Summary{}, err
	}
	s.State, s.Hidden, s.Archived = State(state), hidden, archived
	s.Angle, s.MuseID, s.DraftID, s.RootTrackID = nullPtr(angle), nullPtr(muse), nullPtr(draft), nullPtr(root)
	if pickTake.Valid {
		s.Pick = &PickRef{TakeID: pickTake.String, Source: pickSource.String}
	}
	return s, nil
}

func nullPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}

// List pages Songs (C90.7): (created_at DESC, id DESC) keyset, hidden / retired / archived excluded by default.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	where, args := FilterSQL(f, "")
	var page Page
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM songs s WHERE `+where, args...).Scan(&page.Total); err != nil {
		return Page{}, fmt.Errorf("songs: count: %w", err)
	}
	pageWhere, pageArgs := where, append([]any(nil), args...)
	if f.Cursor != "" {
		cur, err := decodeCursor(f.Cursor)
		if err != nil {
			return Page{}, err
		}
		pageWhere += " AND (s.created_at < ? OR (s.created_at = ? AND s.id < ?))"
		pageArgs = append(pageArgs, cur.At, cur.At, cur.ID)
	}
	pageArgs = append(pageArgs, limit+1)
	rows, err := s.st.R.QueryContext(ctx, `SELECT `+SummaryColumns+`, s.created_at `+SummaryFrom+` WHERE `+pageWhere+` ORDER BY s.created_at DESC, s.id DESC LIMIT ?`, pageArgs...) //nolint:gosec // G202: fragments are fixed strings built in this function
	if err != nil {
		return Page{}, fmt.Errorf("songs: list: %w", err)
	}
	defer rows.Close()
	size := envelopeBytes
	more := false
	var lastAt string
	for rows.Next() {
		var created string
		sum, err := ScanSummary(rows, &created)
		if err != nil {
			return Page{}, err
		}
		if len(page.Items) >= limit {
			more = true
			break
		}
		b, _ := json.Marshal(sum)
		if len(page.Items) > 0 && size+len(b)+1 > maxPageBytes {
			more = true
			break
		}
		size += len(b) + 1
		page.Items = append(page.Items, sum)
		lastAt = created
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if more && len(page.Items) > 0 {
		c := encodeCursor(lastAt, page.Items[len(page.Items)-1].ID)
		page.NextCursor = &c
	}
	return page, nil
}
