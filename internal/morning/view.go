package morning

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Verdicts of a chapter, strongest first (C380.3).
const (
	VerdictKept    = "kept"
	VerdictSkipped = "skipped"
	VerdictMarked  = "marked"
	VerdictHeard   = "heard"
	VerdictUnheard = "unheard"
)

// minLinkTTL is the least a side link lives: a side is 15 minutes of audio and may be heard on the way, offline included.
const minLinkTTL = 6 * time.Hour

// TrackRef names a take. It is present only after the reveal (blind: no title, id or lineage reaches anyone before).
type TrackRef struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	RootTrackID string `json:"root_track_id"`
}

// SideView is one file of a crate.
type SideView struct {
	Side       int        `json:"side"`
	Label      string     `json:"label"`
	DurationMS int64      `json:"duration_ms"`
	Bytes      int64      `json:"bytes"`
	Chapters   int        `json:"chapters"`
	URL        *string    `json:"url"`
	ExpiresAt  *time.Time `json:"expires_at"`
	SHA256     string     `json:"sha256"`
	Removed    bool       `json:"removed"`
}

// ChapterView is one take of a crate as the viewer may see it. Before the reveal Track, Anchor, GainDB, Limited and LUFSAfter are nil.
type ChapterView struct {
	N             int       `json:"n"`
	Side          int       `json:"side"`
	StartMS       int64     `json:"start_ms"`
	AudioStartMS  int64     `json:"audio_start_ms"`
	EndMS         int64     `json:"end_ms"`
	Stars         *int      `json:"stars"`
	Verdict       string    `json:"verdict"`
	Markers       int       `json:"markers"`
	StrongMarkers int       `json:"strong_markers"`
	SkippedAtMS   *int64    `json:"skipped_at_ms"`
	Track         *TrackRef `json:"track"`
	Anchor        *bool     `json:"anchor"`
	GainDB        *float64  `json:"gain_db"`
	Limited       *bool     `json:"limited"`
	LUFSAfter     *float64  `json:"lufs_after"`
}

// Crate is the full view of one crate.
type Crate struct {
	ID               string        `json:"id"`
	State            string        `json:"state"`
	Trigger          string        `json:"trigger"`
	CreatedAt        time.Time     `json:"created_at"`
	BuiltAt          *time.Time    `json:"built_at"`
	Revealed         bool          `json:"revealed"`
	RevealedAt       *time.Time    `json:"revealed_at"`
	AdjacencyRelaxed bool          `json:"adjacency_relaxed"`
	Takes            int           `json:"takes"`
	Anchors          int           `json:"anchors"`
	DurationMS       int64         `json:"duration_ms"`
	ErrorCode        *string       `json:"error_code"`
	Sides            []SideView    `json:"sides"`
	Chapters         []ChapterView `json:"chapters"`
	PlaylistID       *string       `json:"playlist_id"`
}

// CrateSummary is a row of the list: nothing that names a take.
type CrateSummary struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	Trigger    string     `json:"trigger"`
	CreatedAt  time.Time  `json:"created_at"`
	BuiltAt    *time.Time `json:"built_at"`
	Revealed   bool       `json:"revealed"`
	RevealedAt *time.Time `json:"revealed_at"`
	Takes      int        `json:"takes"`
	Anchors    int        `json:"anchors"`
	Sides      int        `json:"sides"`
	DurationMS int64      `json:"duration_ms"`
	ErrorCode  *string    `json:"error_code"`
}

// ListFilter selects crates, newest first.
type ListFilter struct {
	State  string // empty: every state
	Limit  int    // 0: 20, at most 100
	Cursor string
}

// Page is one page of the list.
type Page struct {
	Items      []CrateSummary `json:"items"`
	NextCursor string         `json:"next_cursor"`
}

func notFound(id string) *problem.Error {
	return problem.E(404, "NOT_FOUND", "no such morning crate", map[string]any{"id": id})
}

func label(i int) string { return "Side " + string(rune('A'+i%26)) }

func frameMS(f int64) int64 { return f * 1000 / SampleRate }

func optTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := store.ParseInstant(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

const crateCols = `id, state, trigger, created_at, built_at, revealed_at, adjacency_relaxed, takes, anchors, duration_ms, error_code, playlist_id`

type crateRow struct {
	Crate
	built, revealed sql.NullString
	created         string
	errCode, plist  sql.NullString
	adj             int
}

func scanCrate(r interface{ Scan(...any) error }) (crateRow, error) {
	var c crateRow
	if err := r.Scan(&c.ID, &c.State, &c.Trigger, &c.created, &c.built, &c.revealed, &c.adj, &c.Takes, &c.Anchors, &c.DurationMS, &c.errCode, &c.plist); err != nil {
		return crateRow{}, err
	}
	var err error
	if c.CreatedAt, err = store.ParseInstant(c.created); err != nil {
		return crateRow{}, err
	}
	if c.BuiltAt, err = optTime(c.built); err != nil {
		return crateRow{}, err
	}
	if c.RevealedAt, err = optTime(c.revealed); err != nil {
		return crateRow{}, err
	}
	c.Revealed = c.RevealedAt != nil
	c.AdjacencyRelaxed = c.adj == 1
	if c.errCode.Valid {
		c.ErrorCode = &c.errCode.String
	}
	if c.plist.Valid && c.Revealed {
		c.PlaylistID = &c.plist.String
	}
	return c, nil
}

// Get returns one crate. Only the Owner's side sees any crate (D-M6N-8); before the reveal no chapter names its take.
func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Crate, error) {
	if err := ownerSide(p); err != nil {
		return Crate{}, err
	}
	row, err := scanCrate(s.o.Store.R.QueryRowContext(ctx, `SELECT `+crateCols+` FROM morning_crates WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Crate{}, notFound(id)
	}
	if err != nil {
		return Crate{}, fmt.Errorf("morning: get: %w", err)
	}
	c := row.Crate
	c.Sides, c.Chapters = []SideView{}, []ChapterView{}
	if c.State != StateReady {
		return c, nil
	}
	if c.Sides, err = s.sides(ctx, p, id); err != nil {
		return Crate{}, err
	}
	if c.Chapters, err = s.chapters(ctx, id, c.Revealed); err != nil {
		return Crate{}, err
	}
	return c, nil
}

func (s *Service) sides(ctx context.Context, p auth.Principal, id string) ([]SideView, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT side_index, sha256, path, bytes, samples, removed_at,
		(SELECT COUNT(*) FROM morning_crate_items i WHERE i.crate_id = s.crate_id AND i.side_index = s.side_index)
		FROM morning_crate_sides s WHERE crate_id = ? ORDER BY side_index`, id)
	if err != nil {
		return nil, fmt.Errorf("morning: sides: %w", err)
	}
	defer rows.Close()
	out := []SideView{}
	ttl, bucket := serve.TTLFor(p)
	if p.IsPerson() && p.CanSeePrivate() {
		ttl = max(ttl, minLinkTTL) // agents keep the 15-minute policy of D-U1-13
	}
	for rows.Next() {
		var (
			v       SideView
			path    string
			samples int64
			removed sql.NullString
		)
		if err := rows.Scan(&v.Side, &v.SHA256, &path, &v.Bytes, &samples, &removed, &v.Chapters); err != nil {
			return nil, err
		}
		v.Label, v.DurationMS, v.Removed = label(v.Side), frameMS(samples), removed.Valid
		if !v.Removed && s.o.Signer != nil && s.fileExists(path) {
			l := s.o.Signer.Sign(v.SHA256, "flac", ttl, bucket)
			exp := l.Expires.UTC()
			v.URL, v.ExpiresAt = &l.URL, &exp
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Service) fileExists(rel string) bool {
	if s.o.Data == nil {
		return false
	}
	_, err := s.o.Data.Stat(rel)
	return err == nil
}

type evAgg struct {
	markers, strong int
	played          bool
	skippedAt       *int64
	stars           *int
	haveStars       bool
}

// events folds the crate's raw events per chapter: the latest rating, the marker counts and the last skip.
func (s *Service) events(ctx context.Context, id string) (map[int]*evAgg, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT chapter, kind, stars, side_ms FROM morning_crate_events WHERE crate_id = ? ORDER BY received_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("morning: events: %w", err)
	}
	defer rows.Close()
	m := map[int]*evAgg{}
	for rows.Next() {
		var (
			ch     int
			kind   string
			stars  sql.NullInt64
			sideMS int64
		)
		if err := rows.Scan(&ch, &kind, &stars, &sideMS); err != nil {
			return nil, err
		}
		a := m[ch]
		if a == nil {
			a = &evAgg{}
			m[ch] = a
		}
		switch kind {
		case "marker":
			a.markers++
		case "strong_marker":
			a.strong++
		case "skip":
			ms := sideMS
			a.skippedAt = &ms
		case "complete", "play":
			a.played = true
		case "rate":
			a.stars, a.haveStars = nil, true
			if stars.Valid {
				v := int(stars.Int64)
				a.stars = &v
			}
		}
	}
	return m, rows.Err()
}

func verdictOf(a *evAgg) string {
	switch {
	case a == nil:
		return VerdictUnheard
	case a.strong > 0:
		return VerdictKept
	case a.skippedAt != nil:
		return VerdictSkipped
	case a.markers > 0:
		return VerdictMarked
	case a.played || a.haveStars:
		return VerdictHeard
	}
	return VerdictUnheard
}

// chapters lists a ready crate's chapters. Names, gains and the anchor flag are filled only when revealed.
func (s *Service) chapters(ctx context.Context, id string, revealed bool) ([]ChapterView, error) {
	ev, err := s.events(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT i.n, i.side_index, i.start_frame, i.audio_start_frame, i.end_frame, i.track_id, i.anchor, i.gain_cdb, i.limited,
		i.lufs_after, t.title, t.root_track_id FROM morning_crate_items i JOIN tracks t ON t.id = i.track_id WHERE i.crate_id = ? ORDER BY i.n`, id)
	if err != nil {
		return nil, fmt.Errorf("morning: chapters: %w", err)
	}
	defer rows.Close()
	out := []ChapterView{}
	for rows.Next() {
		var (
			v                  ChapterView
			start, astart, end int64
			track, title, root string
			anchor, cdb, lim   int
			lufs               sql.NullFloat64
		)
		if err := rows.Scan(&v.N, &v.Side, &start, &astart, &end, &track, &anchor, &cdb, &lim, &lufs, &title, &root); err != nil {
			return nil, err
		}
		v.StartMS, v.AudioStartMS, v.EndMS = frameMS(start), frameMS(astart), frameMS(end)
		a := ev[v.N]
		v.Verdict = verdictOf(a)
		if a != nil {
			v.Markers, v.StrongMarkers, v.SkippedAtMS, v.Stars = a.markers, a.strong, a.skippedAt, a.stars
		}
		if revealed {
			an, lm, gain := anchor == 1, lim == 1, float64(cdb)/100
			v.Track, v.Anchor, v.GainDB, v.Limited = &TrackRef{ID: track, Title: title, RootTrackID: root}, &an, &gain, &lm
			if lufs.Valid {
				l := lufs.Float64
				v.LUFSAfter = &l
			}
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// List returns crates newest first, as summaries.
func (s *Service) List(ctx context.Context, p auth.Principal, f ListFilter) (Page, error) {
	if err := ownerSide(p); err != nil {
		return Page{}, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	q := `SELECT ` + crateCols + `, (SELECT COUNT(*) FROM morning_crate_sides x WHERE x.crate_id = c.id) FROM morning_crates c WHERE 1 = 1`
	var args []any
	if f.State != "" {
		switch f.State {
		case StateBuilding, StateReady, StateEmpty, StateFailed:
		default:
			return Page{}, problem.E(422, "VALIDATION", "unknown state", map[string]any{"field": "state"})
		}
		q += ` AND c.state = ?`
		args = append(args, f.State)
	}
	if f.Cursor != "" {
		at, id, ok := strings.Cut(f.Cursor, "|")
		if !ok || at == "" || id == "" {
			return Page{}, problem.E(422, "VALIDATION", "bad cursor", map[string]any{"field": "cursor"})
		}
		q += ` AND (c.created_at < ? OR (c.created_at = ? AND c.id < ?))`
		args = append(args, at, at, id)
	}
	q += ` ORDER BY c.created_at DESC, c.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.o.Store.R.QueryContext(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("morning: list: %w", err)
	}
	defer rows.Close()
	pg := Page{Items: []CrateSummary{}}
	var lastAt, lastID string
	for rows.Next() {
		var c crateRow
		var nsides int
		if err := rows.Scan(&c.ID, &c.State, &c.Trigger, &c.created, &c.built, &c.revealed, &c.adj, &c.Takes, &c.Anchors, &c.DurationMS, &c.errCode, &c.plist, &nsides); err != nil {
			return Page{}, err
		}
		if len(pg.Items) == limit { // the extra row only proves there is a next page
			pg.NextCursor = lastAt + "|" + lastID
			break
		}
		sm := CrateSummary{ID: c.ID, State: c.State, Trigger: c.Trigger, Takes: c.Takes, Anchors: c.Anchors, Sides: nsides, DurationMS: c.DurationMS}
		if sm.CreatedAt, err = store.ParseInstant(c.created); err != nil {
			return Page{}, err
		}
		if sm.BuiltAt, err = optTime(c.built); err != nil {
			return Page{}, err
		}
		if sm.RevealedAt, err = optTime(c.revealed); err != nil {
			return Page{}, err
		}
		sm.Revealed = sm.RevealedAt != nil
		if c.errCode.Valid {
			e := c.errCode.String
			sm.ErrorCode = &e
		}
		pg.Items = append(pg.Items, sm)
		lastAt, lastID = c.created, c.ID
	}
	return pg, rows.Err()
}
