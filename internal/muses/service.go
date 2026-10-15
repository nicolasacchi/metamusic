package muses

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// Service is the Muse service (C89). Muse text never appears in an error message or detail (C99.4): errors carry ids and
// field names only.
type Service struct {
	st    *store.Store
	now   func() time.Time
	newID func() string
}

// New builds the Service.
func New(st *store.Store, now func() time.Time, newID func() string) *Service {
	return &Service{st: st, now: now, newID: newID}
}

func invalid(field, msg string) error {
	return problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

func notFound(id string) error {
	return problem.E(http.StatusNotFound, "NOT_FOUND", "no such muse: "+id, map[string]any{"id": id})
}

func conflictRevision(cur int) error {
	return problem.E(http.StatusConflict, "REVISION_CONFLICT", "the muse changed since you read it; re-read it and retry", map[string]any{"current_revision": cur})
}

func existsErr(id string, hidden bool) error {
	return problem.E(http.StatusConflict, "MUSE_EXISTS", "a muse with this title and artist already exists", map[string]any{"muse_id": id, "hidden": hidden})
}

// cleanTitle collapses whitespace and checks 1..200 runes.
func cleanTitle(s string) (string, error) {
	t := collapse(s)
	if t == "" || utf8.RuneCountInString(t) > MaxTitleRunes {
		return "", invalid("title", "a Muse title must be 1 to 200 characters")
	}
	return t, nil
}

func cleanArtist(s *string) (*string, error) {
	if s == nil {
		return nil, nil
	}
	a := collapse(*s)
	if a == "" || utf8.RuneCountInString(a) > MaxArtistRunes {
		return nil, invalid("artist", "a Muse artist must be 1 to 200 characters")
	}
	return &a, nil
}

func cleanBrief(s *string) (*string, error) {
	if s == nil {
		return nil, nil
	}
	b := strings.TrimSpace(*s)
	if b == "" || utf8.RuneCountInString(b) > MaxBriefRunes {
		return nil, invalid("brief", "a Muse brief must be 1 to 2000 characters")
	}
	return &b, nil
}

const museCols = `id, kind, title, artist, brief, source, created_by, created_at, updated_by, updated_at, revision, hidden_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanMuse(r rowScanner) (Muse, error) {
	var m Muse
	var artist, brief, hidden sql.NullString
	if err := r.Scan(&m.ID, &m.Kind, &m.Title, &artist, &brief, &m.Source, &m.CreatedBy, &m.CreatedAt, &m.UpdatedBy, &m.UpdatedAt, &m.Revision, &hidden); err != nil {
		return Muse{}, err
	}
	if artist.Valid {
		m.Artist = &artist.String
	}
	if brief.Valid {
		m.Brief = &brief.String
	}
	if hidden.Valid {
		m.HiddenAt = &hidden.String
	}
	return m, nil
}

// Create makes a Muse (C89.1).
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewMuse) (Muse, error) {
	title, err := cleanTitle(in.Title)
	if err != nil {
		return Muse{}, err
	}
	var artist, brief *string
	switch in.Kind {
	case KindReference:
		if in.Brief != nil {
			return Muse{}, invalid("brief", "a reference Muse has no brief")
		}
		if artist, err = cleanArtist(in.Artist); err != nil {
			return Muse{}, err
		}
	case KindBrief:
		if in.Artist != nil {
			return Muse{}, invalid("artist", "a brief Muse has no artist")
		}
		if in.Brief == nil {
			return Muse{}, invalid("brief", "a brief Muse needs a brief")
		}
		if brief, err = cleanBrief(in.Brief); err != nil {
			return Muse{}, err
		}
	default:
		return Muse{}, invalid("kind", "kind must be reference or brief")
	}
	source := SourceAgent
	if p.ActsAsHuman() {
		source = SourceHuman
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Muse{}, err
	}
	defer func() { _ = tx.Rollback() }()
	id := s.newID()
	key := "brief:" + id
	if in.Kind == KindReference {
		key = Key(title, artist)
		var other string
		var hidden sql.NullString
		switch err := tx.QueryRowContext(ctx, `SELECT id, hidden_at FROM muses WHERE key = ?`, key).Scan(&other, &hidden); {
		case err == nil:
			return Muse{}, existsErr(other, hidden.Valid)
		case !errors.Is(err, sql.ErrNoRows):
			return Muse{}, fmt.Errorf("muses: create: %w", err)
		}
	}
	ts := store.FormatTime(s.now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO muses(id, kind, title, title_fold, artist, brief, key, source, created_by, created_at, updated_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(in.Kind), title, textfold.Norm(title), artist, brief, key, source, p.Key(), ts, p.Key(), ts); err != nil {
		return Muse{}, fmt.Errorf("muses: create: %w", err)
	}
	m, err := scanMuse(tx.QueryRowContext(ctx, `SELECT `+museCols+` FROM muses WHERE id = ?`, id))
	if err != nil {
		return Muse{}, err
	}
	return m, tx.Commit()
}

// Get reads one Muse.
func (s *Service) Get(ctx context.Context, id string) (Muse, error) {
	m, err := scanMuse(s.st.R.QueryRowContext(ctx, `SELECT `+museCols+` FROM muses WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Muse{}, notFound(id)
	}
	return m, err
}

// Update patches a Muse under the revision lock (C89.1).
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Muse, error) {
	if patch.Title == nil && patch.Artist == nil && patch.Brief == nil && patch.Hidden == nil {
		return Muse{}, invalid("body", "nothing to change")
	}
	var title *string
	var artist, brief *string
	var err error
	if patch.Title != nil {
		t, err := cleanTitle(*patch.Title)
		if err != nil {
			return Muse{}, err
		}
		title = &t
	}
	if artist, err = cleanArtist(patch.Artist); err != nil {
		return Muse{}, err
	}
	if brief, err = cleanBrief(patch.Brief); err != nil {
		return Muse{}, err
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Muse{}, err
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := scanMuse(tx.QueryRowContext(ctx, `SELECT `+museCols+` FROM muses WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Muse{}, notFound(id)
	}
	if err != nil {
		return Muse{}, err
	}
	if cur.Revision != patch.Revision {
		return Muse{}, conflictRevision(cur.Revision)
	}
	switch Kind(cur.Kind) {
	case KindReference:
		if brief != nil {
			return Muse{}, invalid("brief", "a reference Muse has no brief")
		}
	case KindBrief:
		if artist != nil {
			return Muse{}, invalid("artist", "a brief Muse has no artist")
		}
	}
	next := cur
	if title != nil {
		next.Title = *title
	}
	if artist != nil {
		next.Artist = artist
	}
	if brief != nil {
		next.Brief = brief
	}
	key := "brief:" + id
	if Kind(cur.Kind) == KindReference {
		key = Key(next.Title, next.Artist)
		var other string
		switch err := tx.QueryRowContext(ctx, `SELECT id FROM muses WHERE key = ? AND id <> ?`, key, id).Scan(&other); {
		case err == nil:
			var hidden sql.NullString
			_ = tx.QueryRowContext(ctx, `SELECT hidden_at FROM muses WHERE id = ?`, other).Scan(&hidden)
			return Muse{}, existsErr(other, hidden.Valid)
		case !errors.Is(err, sql.ErrNoRows):
			return Muse{}, fmt.Errorf("muses: update: %w", err)
		}
	}
	ts := store.FormatTime(s.now())
	hiddenAt, hiddenBy := cur.HiddenAt, any(nil)
	if cur.HiddenAt != nil {
		hiddenBy = s.hiddenBy(ctx, tx, id)
	}
	if patch.Hidden != nil {
		if *patch.Hidden {
			if cur.HiddenAt == nil {
				hiddenAt, hiddenBy = &ts, p.Key()
			}
		} else {
			hiddenAt, hiddenBy = nil, nil
		}
	}
	keySQL := ", key = ?"
	keyArgs := []any{key}
	if Kind(cur.Kind) == KindBrief {
		keySQL, keyArgs = "", nil // the brief key is frozen by trigger even for an identical value
	}
	args := append([]any{next.Title, textfold.Norm(next.Title), next.Artist, next.Brief, hiddenAt, hiddenBy, p.Key(), ts}, keyArgs...)
	args = append(args, id)
	if _, err := tx.ExecContext(ctx, `UPDATE muses SET title = ?, title_fold = ?, artist = ?, brief = ?, hidden_at = ?, hidden_by = ?,
		updated_by = ?, updated_at = ?, revision = revision + 1`+keySQL+` WHERE id = ?`, args...); err != nil {
		return Muse{}, fmt.Errorf("muses: update: %w", err)
	}
	m, err := scanMuse(tx.QueryRowContext(ctx, `SELECT `+museCols+` FROM muses WHERE id = ?`, id))
	if err != nil {
		return Muse{}, err
	}
	return m, tx.Commit()
}

func (s *Service) hiddenBy(ctx context.Context, tx *sql.Tx, id string) any {
	var by sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT hidden_by FROM muses WHERE id = ?`, id).Scan(&by)
	if by.Valid {
		return by.String
	}
	return nil
}

// AssignSongs assigns the Muse to 1..200 Songs, all or nothing (C89.1).
func (s *Service) AssignSongs(ctx context.Context, p auth.Principal, museID string, songIDs []string) (AssignResult, error) {
	if len(songIDs) < 1 || len(songIDs) > 200 {
		return AssignResult{}, invalid("song_ids", "1 to 200 Song ids")
	}
	seen := make(map[string]bool, len(songIDs))
	for _, id := range songIDs {
		if id == "" || seen[id] {
			return AssignResult{}, invalid("song_ids", "Song ids must be non-empty and distinct")
		}
		seen[id] = true
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return AssignResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var hidden sql.NullString
	switch err := tx.QueryRowContext(ctx, `SELECT hidden_at FROM muses WHERE id = ?`, museID).Scan(&hidden); {
	case errors.Is(err, sql.ErrNoRows):
		return AssignResult{}, notFound(museID)
	case err != nil:
		return AssignResult{}, err
	}
	if hidden.Valid {
		return AssignResult{}, problem.E(http.StatusConflict, "MUSE_HIDDEN", "the muse is hidden", map[string]any{"muse_id": museID})
	}
	var res AssignResult
	ts := store.FormatTime(s.now())
	for _, id := range songIDs {
		var cur, into sql.NullString
		var retired sql.NullString
		switch err := tx.QueryRowContext(ctx, `SELECT muse_id, retired_at, retired_into FROM songs WHERE id = ?`, id).Scan(&cur, &retired, &into); {
		case errors.Is(err, sql.ErrNoRows):
			return AssignResult{}, problem.E(http.StatusUnprocessableEntity, "UNKNOWN_SONG", "no such song: "+id, map[string]any{"song_id": id})
		case err != nil:
			return AssignResult{}, err
		}
		if retired.Valid {
			return AssignResult{}, problem.E(http.StatusConflict, "SONG_RETIRED", "the song is retired: "+id, map[string]any{"song_id": id, "retired_into": into.String})
		}
		if cur.Valid && cur.String == museID {
			res.Unchanged++
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE songs SET muse_id = ?, muse_source = 'manual', revision = revision + 1, updated_at = ?, updated_by = ? WHERE id = ?`,
			museID, ts, p.Key(), id); err != nil {
			return AssignResult{}, fmt.Errorf("muses: assign: %w", err)
		}
		res.Assigned++
	}
	return res, tx.Commit()
}
