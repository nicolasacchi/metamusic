package sessions

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Service is the Session list/rename service (C92.2). Session names are Muse-adjacent text (C99): they never reach
// logs, errors or audit text from here. The sync functions (sync.go, ensure.go) are the Reconciler's.
type Service struct {
	st  *store.Store
	now func() time.Time
}

// New builds the Service.
func New(st *store.Store, now func() time.Time) *Service { return &Service{st: st, now: now} }

const (
	maxPageBytes  = 90000
	envelopeBytes = 256
)

func invalid(field, msg string) error {
	return problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

func notFound(id string) error {
	return problem.E(http.StatusNotFound, "NOT_FOUND", "no such session: "+id, map[string]any{"id": id})
}

type cursor struct {
	At string `json:"s"`
	ID string `json:"i"`
}

func decodeCursor(c string) (cursor, error) {
	var cur cursor
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || json.Unmarshal(b, &cur) != nil || cur.ID == "" {
		return cursor{}, invalid("cursor", "malformed cursor")
	}
	return cur, nil
}

// summaryCols selects a Summary from `sessions x`; the three counts are correlated subqueries: drafts and takes are the
// member rows by kind, songs the distinct live Songs reached through track members (take mapping) or draft members.
const summaryCols = `x.id, x.kind, x.source_key, x.name, x.started_at, x.ended_at, x.created_by, x.created_at, x.updated_by, x.updated_at, x.revision, x.hidden_at,
 (SELECT COUNT(*) FROM session_members m WHERE m.session_id = x.id AND m.member_kind = 'draft'),
 (SELECT COUNT(*) FROM session_members m WHERE m.session_id = x.id AND m.member_kind = 'track'),
 (SELECT COUNT(*) FROM (
    SELECT ts.song_id AS sid FROM session_members m JOIN take_songs ts ON ts.track_id = m.member_id WHERE m.session_id = x.id AND m.member_kind = 'track'
    UNION SELECT s.id FROM session_members m JOIN songs s ON s.draft_id = m.member_id AND s.retired_at IS NULL WHERE m.session_id = x.id AND m.member_kind = 'draft'))`

func scanSummary(r interface{ Scan(...any) error }) (Summary, error) {
	var s Summary
	var key, ended, hidden sql.NullString
	if err := r.Scan(&s.ID, &s.Kind, &key, &s.Name, &s.StartedAt, &ended, &s.CreatedBy, &s.CreatedAt, &s.UpdatedBy, &s.UpdatedAt, &s.Revision, &hidden,
		&s.Drafts, &s.Takes, &s.Songs); err != nil {
		return Summary{}, err
	}
	if key.Valid {
		s.SourceKey = &key.String
	}
	if ended.Valid {
		s.EndedAt = &ended.String
	}
	if hidden.Valid {
		s.HiddenAt = &hidden.String
	}
	return s, nil
}

// List pages Sessions newest first (C92.2).
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	where := []string{"1 = 1"}
	var args []any
	if !f.IncludeHidden {
		where = append(where, "x.hidden_at IS NULL")
	}
	if len(f.Kinds) > 0 {
		where = append(where, "x.kind IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.Kinds)), ",")+")")
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, `x.name LIKE ? ESCAPE '\'`)
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)+"%")
	}
	base := strings.Join(where, " AND ")
	var page Page
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions x WHERE `+base, args...).Scan(&page.Total); err != nil {
		return Page{}, fmt.Errorf("sessions: count: %w", err)
	}
	pageWhere, pageArgs := base, append([]any(nil), args...)
	if f.Cursor != "" {
		cur, err := decodeCursor(f.Cursor)
		if err != nil {
			return Page{}, err
		}
		pageWhere += " AND (x.started_at < ? OR (x.started_at = ? AND x.id < ?))"
		pageArgs = append(pageArgs, cur.At, cur.At, cur.ID)
	}
	pageArgs = append(pageArgs, limit+1)
	rows, err := s.st.R.QueryContext(ctx, `SELECT `+summaryCols+` FROM sessions x WHERE `+pageWhere+` ORDER BY x.started_at DESC, x.id DESC LIMIT ?`, pageArgs...) //nolint:gosec // G202: fragments are fixed strings built in this function
	if err != nil {
		return Page{}, fmt.Errorf("sessions: list: %w", err)
	}
	defer rows.Close()
	size := envelopeBytes
	more := false
	for rows.Next() {
		sum, err := scanSummary(rows)
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
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if more && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		b, _ := json.Marshal(cursor{last.StartedAt, last.ID})
		c := base64.RawURLEncoding.EncodeToString(b)
		page.NextCursor = &c
	}
	return page, nil
}

// Summary reads one Session with its counts (hidden ones included).
func (s *Service) Summary(ctx context.Context, id string) (Summary, error) {
	sum, err := scanSummary(s.st.R.QueryRowContext(ctx, `SELECT `+summaryCols+` FROM sessions x WHERE x.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Summary{}, notFound(id)
	}
	return sum, err
}

// Update renames or hides a Session under the revision lock (C92.2).
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Session, error) {
	if patch.Name == nil && patch.Hidden == nil {
		return Session{}, invalid("body", "nothing to change")
	}
	var name string
	if patch.Name != nil {
		name = strings.TrimSpace(*patch.Name)
		if name == "" || utf8.RuneCountInString(name) > 120 {
			return Session{}, invalid("name", "a session name must be 1 to 120 characters")
		}
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var cur int
	var hidden sql.NullString
	switch err := tx.QueryRowContext(ctx, `SELECT revision, hidden_at FROM sessions WHERE id = ?`, id).Scan(&cur, &hidden); {
	case errors.Is(err, sql.ErrNoRows):
		return Session{}, notFound(id)
	case err != nil:
		return Session{}, err
	}
	if cur != patch.Revision {
		return Session{}, problem.E(http.StatusConflict, "REVISION_CONFLICT", "the session changed since you read it; re-read it and retry", map[string]any{"current_revision": cur})
	}
	ts := store.FormatTime(s.now())
	sets := []string{"revision = revision + 1", "updated_at = ?", "updated_by = ?"}
	args := []any{ts, p.Key()}
	if patch.Name != nil {
		sets, args = append(sets, "name = ?"), append(args, name)
	}
	if patch.Hidden != nil {
		if *patch.Hidden {
			if !hidden.Valid {
				sets, args = append(sets, "hidden_at = ?", "hidden_by = ?"), append(args, ts, p.Key())
			}
		} else {
			sets = append(sets, "hidden_at = NULL", "hidden_by = NULL")
		}
	}
	args = append(args, id)
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil { //nolint:gosec // G202: sets are fixed column assignments
		return Session{}, fmt.Errorf("sessions: update: %w", err)
	}
	sum, err := scanSummary(tx.QueryRowContext(ctx, `SELECT `+summaryCols+` FROM sessions x WHERE x.id = ?`, id))
	if err != nil {
		return Session{}, err
	}
	return sum.Session, tx.Commit()
}
