package songs

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
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Service is the Song list/update service (C90). The structural reconcile functions (reconcile.go) are the Reconciler's;
// Update touches the disjoint edit columns only (C88.2).
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

// NotFound is the 404 of an unknown Song id (the message carries the id only).
func NotFound(id string) error {
	return problem.E(http.StatusNotFound, "NOT_FOUND", "no such song: "+id, map[string]any{"id": id})
}

const songCols = `id, root_track_id, draft_id, muse_id, angle, angle_source, title, origin, revision, muse_source,
	created_by, created_at, updated_by, updated_at, hidden_at, retired_at, retired_into`

type scanner interface{ Scan(dest ...any) error }

func scanSong(r scanner) (Song, error) {
	var s Song
	var root, draft, muse, angle, angleSrc, museSrc, hidden, retired, into sql.NullString
	if err := r.Scan(&s.ID, &root, &draft, &muse, &angle, &angleSrc, &s.Title, &s.Origin, &s.Revision, &museSrc,
		&s.CreatedBy, &s.CreatedAt, &s.UpdatedBy, &s.UpdatedAt, &hidden, &retired, &into); err != nil {
		return Song{}, err
	}
	s.RootTrackID, s.DraftID, s.MuseID = nullPtr(root), nullPtr(draft), nullPtr(muse)
	s.Angle, s.AngleSource, s.MuseSource = nullPtr(angle), nullPtr(angleSrc), nullPtr(museSrc)
	s.HiddenAt, s.RetiredAt, s.RetiredInto = nullPtr(hidden), nullPtr(retired), nullPtr(into)
	return s, nil
}

// Get reads one Song row (retired Songs included); an unknown id is 404 NOT_FOUND.
func (s *Service) Get(ctx context.Context, id string) (Song, error) {
	song, err := scanSong(s.st.R.QueryRowContext(ctx, `SELECT `+songCols+` FROM songs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Song{}, NotFound(id)
	}
	return song, err
}

// Update patches a Song under the revision lock (C90.5).
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, patch Patch) (Song, error) {
	if patch.Title == nil && !patch.Angle.Set && !patch.MuseID.Set && patch.Hidden == nil {
		return Song{}, invalid("body", "nothing to change")
	}
	var title string
	if patch.Title != nil {
		title = strings.TrimSpace(*patch.Title)
		if title == "" || utf8.RuneCountInString(title) > 200 {
			return Song{}, invalid("title", "a Song title must be 1 to 200 characters")
		}
	}
	var angle string
	if patch.Angle.Set && patch.Angle.Value != nil {
		in := strings.ToLower(strings.TrimSpace(*patch.Angle.Value))
		slug, ok := AngleSlug(in)
		if !ok || slug != in {
			return Song{}, problem.E(http.StatusUnprocessableEntity, "INVALID_ANGLE", "an angle is a slug: lower-case letters, digits and '-', starting with a letter, at most 32 characters", map[string]any{"field": "angle"})
		}
		angle = slug
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Song{}, err
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := scanSong(tx.QueryRowContext(ctx, `SELECT `+songCols+` FROM songs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Song{}, NotFound(id)
	}
	if err != nil {
		return Song{}, err
	}
	if cur.RetiredAt != nil {
		d := map[string]any{"song_id": id}
		if cur.RetiredInto != nil {
			d["retired_into"] = *cur.RetiredInto
		}
		return Song{}, problem.E(http.StatusConflict, "SONG_RETIRED", "the song is retired: "+id, d)
	}
	if cur.Revision != patch.Revision {
		return Song{}, problem.E(http.StatusConflict, "REVISION_CONFLICT", "the song changed since you read it; re-read it and retry", map[string]any{"current_revision": cur.Revision})
	}
	ts := store.FormatTime(s.now())
	sets := []string{"revision = revision + 1", "updated_at = ?", "updated_by = ?"}
	args := []any{ts, p.Key()}
	if patch.Title != nil {
		sets, args = append(sets, "title = ?"), append(args, title)
	}
	if patch.Angle.Set {
		if patch.Angle.Value == nil {
			sets = append(sets, "angle = NULL", "angle_source = 'cleared'")
		} else {
			sets, args = append(sets, "angle = ?", "angle_source = 'manual'"), append(args, angle)
		}
	}
	if patch.MuseID.Set {
		if patch.MuseID.Value == nil {
			sets = append(sets, "muse_id = NULL", "muse_source = 'manual'")
		} else {
			if err := muses.CheckAssignableTx(ctx, tx, *patch.MuseID.Value); err != nil {
				return Song{}, err
			}
			sets, args = append(sets, "muse_id = ?", "muse_source = 'manual'"), append(args, *patch.MuseID.Value)
		}
	}
	if patch.Hidden != nil {
		if *patch.Hidden {
			if cur.HiddenAt == nil {
				sets, args = append(sets, "hidden_at = ?", "hidden_by = ?"), append(args, ts, p.Key())
			}
		} else {
			sets = append(sets, "hidden_at = NULL", "hidden_by = NULL")
		}
	}
	args = append(args, id)
	if _, err := tx.ExecContext(ctx, `UPDATE songs SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil { //nolint:gosec // G202: sets are fixed column assignments
		return Song{}, fmt.Errorf("songs: update: %w", err)
	}
	out, err := scanSong(tx.QueryRowContext(ctx, `SELECT `+songCols+` FROM songs WHERE id = ?`, id))
	if err != nil {
		return Song{}, err
	}
	return out, tx.Commit()
}

// Core is a Song row with the derived fields update_song and get_song show next to it (C90.6).
type Core struct {
	Song
	State        State
	QueuedJobs   int
	Archived     bool
	RootTrackIDs []string
}

// Core reads one Song with its derived state and root ids (retired Songs included).
func (s *Service) Core(ctx context.Context, id string) (Core, error) {
	row, err := s.Get(ctx, id)
	if err != nil {
		return Core{}, err
	}
	c := Core{Song: row, RootTrackIDs: []string{}}
	var st string
	if err := s.st.R.QueryRowContext(ctx, `SELECT `+store.SongStateCase+`, st.queued_jobs, st.archived FROM (`+store.SongStateSQL+` WHERE s.id = ?) st`, id).Scan(&st, &c.QueuedJobs, &c.Archived); err != nil {
		return Core{}, fmt.Errorf("songs: state: %w", err)
	}
	c.State = State(st)
	rows, err := s.st.R.QueryContext(ctx, `SELECT root_track_id FROM song_roots WHERE song_id = ? ORDER BY root_track_id = ? DESC, root_track_id`, id, row.RootTrackID)
	if err != nil {
		return Core{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return Core{}, err
		}
		c.RootTrackIDs = append(c.RootTrackIDs, r)
	}
	return c, rows.Err()
}
