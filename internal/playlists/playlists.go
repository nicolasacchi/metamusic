// Package playlists owns ordered track lists (contract C33). Items live in one JSON array column
// with a revision counter (D-M2a-4): reordering is atomic and nothing is ever deleted.
package playlists

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"uuid"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// MaxItems is the most tracks a playlist holds.
const MaxItems = 1000

// OfflineMaxItems is the most tracks the offline set holds (C102, C111).
const OfflineMaxItems = 100

// Error is a client-visible failure with a stable code.
type Error struct {
	Status  int
	Code    string
	Msg     string
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func invalid(field, msg string) error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION_FAILED", Msg: msg, Details: map[string]any{"field": field}}
}

// NewPlaylist is the input of Create.
type NewPlaylist struct {
	Name, Description string
	TrackIDs          []string
}

// PlaylistPatch is the input of Update; Revision is required. TrackIDs and
// AddTrackIDs are mutually exclusive (a nil slice means "not sent").
type PlaylistPatch struct {
	Revision          int
	Name, Description *string
	TrackIDs          []string // full ordered replacement
	AddTrackIDs       []string // appended; duplicates allowed
	Hidden            *bool
}

// PlaylistSummary is one row of the playlist list.
type PlaylistSummary struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	ItemsCount int     `json:"items_count"`
	DurationS  float64 `json:"duration_s"`
	Hidden     bool    `json:"hidden"`
	Revision   int     `json:"revision"`
	UpdatedAt  string  `json:"updated_at"`
	Role       *string `json:"role" nullable:"true" doc:"offline for the protected offline set; null for every other playlist"`
}

// Item is one playlist position.
type Item struct {
	Position int                  `json:"position"`
	Track    library.TrackSummary `json:"track"`
}

// Playlist is the full playlist view.
type Playlist struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Revision    int     `json:"revision"`
	Hidden      bool    `json:"hidden"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	CreatedBy   string  `json:"created_by" doc:"Principal key; masked for agents"`
	UpdatedBy   string  `json:"updated_by" doc:"Principal key; masked for agents"`
	Items       []Item  `json:"items" nullable:"false"`
	Role        *string `json:"role" nullable:"true" doc:"offline for the protected offline set; null for every other playlist"`
}

// Service implements C33's playlists.
type Service struct {
	st    *store.Store
	lib   *library.Service
	now   func() time.Time
	newID func() string
}

// New builds the playlists service; nil now means time.Now, nil newID a UUIDv7 generator.
func New(st *store.Store, lib *library.Service, now func() time.Time, newID func() string) *Service {
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func() string { return uuid.NewV7().String() }
	}
	return &Service{st: st, lib: lib, now: now, newID: newID}
}

func (s *Service) stamp() string { return s.now().UTC().Format(time.RFC3339) }

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 120 {
		return "", invalid("name", "1 to 120 characters")
	}
	for _, r := range name {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return "", invalid("name", "must not contain control characters")
		}
	}
	return name, nil
}

func cleanDesc(d string) (string, error) {
	d = strings.TrimSpace(d)
	if utf8.RuneCountInString(d) > 2000 {
		return "", invalid("description", "at most 2000 characters")
	}
	return d, nil
}

// checkTracks fails with UNKNOWN_TRACK for the first id that is not a track.
func (s *Service) checkTracks(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var one int
		err := q.QueryRowContext(ctx, `SELECT 1 FROM tracks WHERE id = ?`, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return &Error{Status: http.StatusUnprocessableEntity, Code: "UNKNOWN_TRACK", Msg: "no such track", Details: map[string]any{"track_id": id}}
		}
		if err != nil {
			return fmt.Errorf("playlists: check track: %w", err)
		}
	}
	return nil
}

// Create stores a new playlist.
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewPlaylist) (Playlist, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return Playlist{}, err
	}
	desc, err := cleanDesc(in.Description)
	if err != nil {
		return Playlist{}, err
	}
	if len(in.TrackIDs) > MaxItems {
		return Playlist{}, invalid("track_ids", fmt.Sprintf("at most %d tracks", MaxItems))
	}
	if err := s.checkTracks(ctx, s.st.R, in.TrackIDs); err != nil {
		return Playlist{}, err
	}
	items, _ := json.Marshal(nonNil(in.TrackIDs))
	id, now := s.newID(), s.stamp()
	if _, err := s.st.W.ExecContext(ctx, `INSERT INTO playlists(id, name, description, items_json, items_count, revision, created_by, updated_by, created_at, updated_at)
		VALUES (?,?,?,?,?,1,?,?,?,?)`, id, name, desc, string(items), len(in.TrackIDs), p.Key(), p.Key(), now, now); err != nil {
		return Playlist{}, fmt.Errorf("playlists: create: %w", err)
	}
	return s.Get(ctx, id)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Update edits a playlist under optimistic locking.
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, in PlaylistPatch) (Playlist, error) {
	if in.TrackIDs != nil && in.AddTrackIDs != nil {
		return Playlist{}, invalid("add_track_ids", "track_ids and add_track_ids are mutually exclusive")
	}
	if in.Name == nil && in.Description == nil && in.TrackIDs == nil && in.AddTrackIDs == nil && in.Hidden == nil {
		return Playlist{}, invalid("body", "nothing to change")
	}
	var name, desc *string
	if in.Name != nil {
		n, err := cleanName(*in.Name)
		if err != nil {
			return Playlist{}, err
		}
		name = &n
	}
	if in.Description != nil {
		d, err := cleanDesc(*in.Description)
		if err != nil {
			return Playlist{}, err
		}
		desc = &d
	}
	if len(in.TrackIDs) > MaxItems || len(in.AddTrackIDs) > MaxItems {
		return Playlist{}, invalid("track_ids", fmt.Sprintf("at most %d tracks", MaxItems))
	}

	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Playlist{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var (
		cur      int
		itemsRaw string
		curName  string
		curDesc  string
		hidden   sql.NullString
	)
	err = tx.QueryRowContext(ctx, `SELECT revision, items_json, name, description, hidden_at FROM playlists WHERE id = ?`, id).
		Scan(&cur, &itemsRaw, &curName, &curDesc, &hidden)
	if errors.Is(err, sql.ErrNoRows) {
		return Playlist{}, library.ErrNotFound
	}
	if err != nil {
		return Playlist{}, fmt.Errorf("playlists: read: %w", err)
	}
	if cur != in.Revision {
		return Playlist{}, &Error{Status: http.StatusConflict, Code: "REVISION_CONFLICT", Msg: "the playlist changed; re-read it and retry",
			Details: map[string]any{"revision": cur}}
	}
	var items []string
	if err := json.Unmarshal([]byte(itemsRaw), &items); err != nil {
		return Playlist{}, fmt.Errorf("playlists: stored items: %w", err)
	}
	switch {
	case in.TrackIDs != nil:
		if err := s.checkTracks(ctx, tx, in.TrackIDs); err != nil {
			return Playlist{}, err
		}
		items = in.TrackIDs
	case in.AddTrackIDs != nil:
		if err := s.checkTracks(ctx, tx, in.AddTrackIDs); err != nil {
			return Playlist{}, err
		}
		items = append(items, in.AddTrackIDs...)
	}
	if len(items) > MaxItems {
		return Playlist{}, invalid("add_track_ids", fmt.Sprintf("a playlist holds at most %d tracks", MaxItems))
	}
	if name == nil {
		name = &curName
	}
	if desc == nil {
		desc = &curDesc
	}
	hid := hidden
	if in.Hidden != nil {
		hid = sql.NullString{}
		if *in.Hidden {
			hid = sql.NullString{String: s.stamp(), Valid: true}
			if hidden.Valid {
				hid = hidden
			}
		}
	}
	b, _ := json.Marshal(nonNil(items))
	if _, err := tx.ExecContext(ctx, `UPDATE playlists SET name = ?, description = ?, items_json = ?, items_count = ?, hidden_at = ?,
		revision = revision + 1, updated_by = ?, updated_at = ? WHERE id = ? AND revision = ?`,
		*name, *desc, string(b), len(items), hid, p.Key(), s.stamp(), id, in.Revision); err != nil {
		return Playlist{}, fmt.Errorf("playlists: update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Playlist{}, err
	}
	return s.Get(ctx, id)
}

// Get returns one playlist with its tracks.
func (s *Service) Get(ctx context.Context, id string) (Playlist, error) {
	var (
		pl       Playlist
		itemsRaw string
		hidden   sql.NullString
	)
	err := s.st.R.QueryRowContext(ctx, `SELECT id, name, description, revision, created_at, updated_at, created_by, updated_by, items_json, hidden_at, role
		FROM playlists WHERE id = ?`, id).
		Scan(&pl.ID, &pl.Name, &pl.Description, &pl.Revision, &pl.CreatedAt, &pl.UpdatedAt, &pl.CreatedBy, &pl.UpdatedBy, &itemsRaw, &hidden, &pl.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return Playlist{}, library.ErrNotFound
	}
	if err != nil {
		return Playlist{}, fmt.Errorf("playlists: get: %w", err)
	}
	pl.Hidden = hidden.Valid
	var ids []string
	if err := json.Unmarshal([]byte(itemsRaw), &ids); err != nil {
		return Playlist{}, fmt.Errorf("playlists: stored items: %w", err)
	}
	sums, err := s.summaries(ctx, ids)
	if err != nil {
		return Playlist{}, err
	}
	pl.Items = make([]Item, 0, len(ids))
	for i, tid := range ids {
		pl.Items = append(pl.Items, Item{Position: i + 1, Track: sums[tid]})
	}
	return pl, nil
}

// List returns the playlists, hidden ones only on request.
func (s *Service) List(ctx context.Context, includeHidden bool) ([]PlaylistSummary, error) {
	q := `SELECT p.id, p.name, p.items_count, p.revision, p.updated_at, p.hidden_at IS NOT NULL,
		COALESCE((SELECT SUM(t.duration_s) FROM json_each(p.items_json) j JOIN tracks t ON t.id = j.value), 0), p.role
		FROM playlists p`
	if !includeHidden {
		q += ` WHERE p.hidden_at IS NULL`
	}
	rows, err := s.st.R.QueryContext(ctx, q+` ORDER BY p.updated_at DESC, p.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("playlists: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []PlaylistSummary{}
	for rows.Next() {
		var x PlaylistSummary
		if err := rows.Scan(&x.ID, &x.Name, &x.ItemsCount, &x.Revision, &x.UpdatedAt, &x.Hidden, &x.DurationS, &x.Role); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// summaries loads the library summary of each distinct track id. The column set and the tag rule
// mirror library.SearchTracks, which has no lookup by id.
func (s *Service) summaries(ctx context.Context, ids []string) (map[string]library.TrackSummary, error) {
	out := map[string]library.TrackSummary{}
	var distinct []string
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			out[id] = library.TrackSummary{}
			distinct = append(distinct, id)
		}
	}
	arr, _ := json.Marshal(distinct)
	rows, err := s.st.R.QueryContext(ctx, `SELECT t.id, t.title, t.status, t.duration_s, t.recorded_at, t.created_at, r.request_id, r.seed, r.style, t.bpm_declared,
		EXISTS(SELECT 1 FROM scores sc WHERE sc.track_id = t.id),
		EXISTS(SELECT 1 FROM sections c WHERE c.track_id = t.id AND c.source = 'plan'),
		CASE WHEN t.truncated_abc IS NULL AND t.truncated_semantic IS NULL THEN NULL
		     WHEN COALESCE(t.truncated_abc,0) = 1 OR COALESCE(t.truncated_semantic,0) = 1 THEN 1 ELSE 0 END,
		(SELECT json_extract(a.metrics_json, '$.integrated_lufs') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'loudness' ORDER BY a.created_at DESC, a.version DESC LIMIT 1),
		(SELECT json_extract(a.metrics_json, '$.bandwidth_hz') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'spectrum' ORDER BY a.created_at DESC, a.version DESC LIMIT 1),
		t.root_track_id, t.parent_track_id,
		(SELECT COUNT(*) FROM tracks c WHERE c.parent_track_id = t.id),
		hs.stars, ha.stars,
		(SELECT COUNT(*) FROM comments c WHERE c.track_id = t.id AND c.hidden_at IS NULL),
		(SELECT COUNT(*) FROM comments c WHERE c.track_id = t.id AND c.hidden_at IS NULL AND c.state = 'open')
		FROM tracks t JOIN recipes r ON r.id = t.recipe_id
		LEFT JOIN ratings hs ON hs.track_id = t.id AND hs.source = 'human'
		LEFT JOIN ratings ha ON ha.track_id = t.id AND ha.source = 'agent'
		WHERE t.id IN (SELECT value FROM json_each(?))`, string(arr))
	if err != nil {
		return nil, fmt.Errorf("playlists: track summaries: %w", err)
	}
	for rows.Next() {
		t, err := scanSummary(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[t.ID] = t
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// effective tags
	trows, err := s.st.R.QueryContext(ctx, `SELECT DISTINCT tt.track_id, g.name FROM track_tags tt JOIN tags g ON g.id = tt.tag_id
		WHERE tt.removed_at IS NULL AND tt.track_id IN (SELECT value FROM json_each(?)) ORDER BY g.name`, string(arr))
	if err != nil {
		return nil, fmt.Errorf("playlists: track tags: %w", err)
	}
	for trows.Next() {
		var id, tag string
		if err := trows.Scan(&id, &tag); err != nil {
			_ = trows.Close()
			return nil, err
		}
		t := out[id]
		t.Tags = append(t.Tags, tag)
		out[id] = t
	}
	if err := trows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanSummary(rows *sql.Rows) (library.TrackSummary, error) {
	var (
		t                   library.TrackSummary
		style               string
		bpm, bw             sql.NullInt64
		trunc               sql.NullInt64
		lufs                sql.NullFloat64
		parent              sql.NullString
		hasScore, timedFlag bool
		stars, critic       sql.NullInt64
	)
	if err := rows.Scan(&t.ID, &t.Title, &t.Status, &t.DurationS, &t.RecordedAt, &t.CreatedAt, &t.RequestID, &t.Seed, &style, &bpm,
		&hasScore, &timedFlag, &trunc, &lufs, &bw, &t.RootTrackID, &parent, &t.ChildrenCount,
		&stars, &critic, &t.Comments, &t.OpenComments); err != nil {
		return t, fmt.Errorf("playlists: scan track: %w", err)
	}
	t.HasScore, t.Timed = hasScore, timedFlag
	terms, _ := yue2.StyleTerms(style)
	if terms == nil {
		terms = []string{}
	}
	t.StyleTerms, t.Tags = terms, []string{}
	if stars.Valid {
		v := int(stars.Int64)
		t.Stars = &v
	}
	if critic.Valid {
		v := int(critic.Int64)
		t.CriticStars = &v
	}
	if bpm.Valid {
		v := int(bpm.Int64)
		t.BPMDeclared = &v
	}
	if trunc.Valid {
		v := trunc.Int64 == 1
		t.Truncated = &v
	}
	if lufs.Valid {
		t.IntegratedLUFS = &lufs.Float64
	}
	if bw.Valid {
		v := int(bw.Int64)
		t.BandwidthHz = &v
	}
	if parent.Valid {
		t.ParentTrackID = &parent.String
	}
	return t, nil
}
