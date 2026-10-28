package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/money"
)

// TrackSummary is one row of the library list.
type TrackSummary struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Status         string   `json:"status"`
	DurationS      float64  `json:"duration_s"`
	RecordedAt     string   `json:"recorded_at" doc:"RFC 3339 UTC"`
	CreatedAt      string   `json:"created_at" doc:"RFC 3339 UTC"`
	RequestID      string   `json:"request_id"`
	Seed           int64    `json:"seed"`
	StyleTerms     []string `json:"style_terms" nullable:"false"`
	BPMDeclared    *int     `json:"bpm_declared" doc:"BPM declared in the style text; null when absent"`
	HasScore       bool     `json:"has_score"`
	Timed          bool     `json:"timed" doc:"Section times are known (aligned score)"`
	Truncated      *bool    `json:"truncated" doc:"abc or semantic stream truncated; null when unknown"`
	IntegratedLUFS *float64 `json:"integrated_lufs"`
	BandwidthHz    *int     `json:"bandwidth_hz"`
	RootTrackID    string   `json:"root_track_id"`
	ParentTrackID  *string  `json:"parent_track_id"`
	ChildrenCount  int      `json:"children_count"`

	// M2a (C38)
	Stars        *int     `json:"stars" doc:"Human stars; null when unrated"`
	CriticStars  *int     `json:"critic_stars" doc:"The agent's stars"`
	Tags         []string `json:"tags" nullable:"false" doc:"Effective tag names"`
	Comments     int      `json:"comments"`
	OpenComments int      `json:"open_comments"`

	// M2c (C59)
	RecipeKnown bool `json:"recipe_known" doc:"false for a reference track: its recipe is a stub; it cannot be re-rendered or used as a mother"`
	Lossy       bool `json:"lossy" doc:"The master is an MP3; there is no lossless file"`

	// M3 (C81.1)
	Origin              string  `json:"origin" doc:"import or job"`
	CostDisplayMicroUSD *int64  `json:"cost_display_micro_usd" doc:"Cost of a generated take (reconciled, else provisional, else estimate); null for imported takes"`
	CostDisplayUSD      *string `json:"cost_display_usd"`

	// L1 (C98)
	SongID *string `json:"song_id" doc:"The Song this take belongs to; null before the Song exists or on a pre-L1 schema"`
	IsPick bool    `json:"is_pick" doc:"The take is its Song's current Pick"`

	// Wave 4b: list rows show "Song · angle" without a second request.
	SongTitle *string `json:"song_title" doc:"Title of the take's Song; null when it has none"`
	SongAngle *string `json:"song_angle" doc:"The Song's angle; null when unset or the take has no Song"`

	jobID string // the job of a generated take (not part of the response)
}

// TrackPage is one page of search results.
type TrackPage struct {
	Items      []TrackSummary `json:"items" nullable:"false"`
	Total      int            `json:"total"`
	NextCursor *string        `json:"next_cursor"`
}

// SearchParams are the filters of SearchTracks.
type SearchParams struct {
	Q                          string
	Status                     []string
	Style                      []string
	MinDurationS, MaxDurationS *float64
	RecordedFrom, RecordedTo   string
	HasScore, Truncated        *bool
	RootTrackID                string
	MinStars                   *int     // human stars at least (M2a); exclusive with Unrated
	Unrated                    bool     // no human rating row, or one with stars NULL
	Tags                       []string // AND, normalised names
	HasOpenComments            *bool
	RecipeKnown                *bool    // (r.engine <> 'unknown') = v (M2c)
	Lossy                      *bool    // t.lossy = v (M2c)
	Origin                     []string // import | job (M3, C81.1)
	SongID                     string   // take belongs to this Song (L1, C98)
	IsPick                     *bool    // take is (not) its Song's current Pick (L1, C98)
	SessionID                  string   // take is in this Session by derived membership (L1, C98)
	Unheard                    *bool    // L2 (C125.3): no human or device start event on the Take
	InRecord                   *bool    // L2: the Take is the resolved Take of a live slot
	Sounds                     []string // L2: facet:value pairs over take_sounds, OR inside a facet, AND across facets
	CrateID                    string   // L2: membership of a manual Crate, or the expansion of a smart one
	noSongs                    bool     // set by the service: the schema has no Song tables, so L1 filters match nothing
	Sort                       string
	Limit                      int
	Cursor                     string
}

var trackStatuses = []string{"draft", "to_relisten", "final", "discarded"}

var trackSorts = []string{"recorded_desc", "recorded_asc", "duration_desc", "duration_asc", "title_asc", "relevance", "stars_desc"}

// parseWhen accepts an RFC 3339 date-time or a plain date. A date as an upper
// bound covers the whole day.
func parseWhen(field, v string, upper bool) (string, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return ts(t), nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return "", invalid(field, "want an RFC 3339 date or date-time")
	}
	if upper {
		t = t.AddDate(0, 0, 1)
	}
	return ts(t), nil
}

const summarySelect = `SELECT t.id, t.title, t.status, t.duration_s, t.recorded_at, t.created_at, r.request_id, r.seed, r.style, t.bpm_declared,
  EXISTS(SELECT 1 FROM scores s WHERE s.track_id = t.id),
  EXISTS(SELECT 1 FROM sections c WHERE c.track_id = t.id AND c.source = 'plan'),
  CASE WHEN t.truncated_abc IS NULL AND t.truncated_semantic IS NULL THEN NULL
       WHEN COALESCE(t.truncated_abc,0) = 1 OR COALESCE(t.truncated_semantic,0) = 1 THEN 1 ELSE 0 END,
  (SELECT json_extract(a.metrics_json, '$.integrated_lufs') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'loudness' ORDER BY a.created_at DESC, a.version DESC LIMIT 1),
  (SELECT json_extract(a.metrics_json, '$.bandwidth_hz') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'spectrum' ORDER BY a.created_at DESC, a.version DESC LIMIT 1),
  t.root_track_id, t.parent_track_id,
  (SELECT COUNT(*) FROM tracks c WHERE c.parent_track_id = t.id),
  hs.stars, ha.stars,
  (SELECT COUNT(*) FROM comments c WHERE c.track_id = t.id AND c.hidden_at IS NULL),
  (SELECT COUNT(*) FROM comments c WHERE c.track_id = t.id AND c.hidden_at IS NULL AND c.state = 'open'),
  (r.engine <> 'unknown'), t.lossy, t.origin, t.job_id`

// the human and agent rating rows of a track (primary-key lookups)
const ratingJoins = ` LEFT JOIN ratings hs ON hs.track_id = t.id AND hs.source = 'human' LEFT JOIN ratings ha ON ha.track_id = t.id AND ha.source = 'agent'`

// normTag lower-cases and space-collapses a tag name for filtering; the
// judgments package owns the full NormalizeTag rule and stored names already
// satisfy it, so a filter that normalises differently simply matches nothing.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func normTag(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// searchQuery is the validated, compiled form of SearchParams.
type searchQuery struct {
	from, where string
	args        []any
	order       string
	limit, off  int
}

// compileSearch validates p and builds its FROM/WHERE/ORDER BY. User text
// never reaches FTS5 syntax.
func compileSearch(p SearchParams) (searchQuery, error) {
	var q searchQuery
	off, err := decodeCursor(p.Cursor)
	if err != nil {
		return q, err
	}
	for _, st := range p.Status {
		if !oneOf(st, trackStatuses...) {
			return q, invalid("status", "unknown status %q", st)
		}
	}
	if len(p.Style) > 5 {
		return q, invalid("style", "at most 5 terms")
	}
	if len(p.Tags) > 5 {
		return q, invalid("tag", "at most 5 tags")
	}
	if p.MinStars != nil && (*p.MinStars < 1 || *p.MinStars > 5) {
		return q, invalid("min_stars", "must be 1..5")
	}
	if p.MinStars != nil && p.Unrated {
		return q, invalid("unrated", "cannot be combined with min_stars")
	}
	for _, o := range p.Origin {
		if !oneOf(o, "import", "job") {
			return q, invalid("origin", "unknown origin %q", o)
		}
	}
	if p.Sort != "" && !oneOf(p.Sort, trackSorts...) {
		return q, invalid("sort", "unknown sort %q", p.Sort)
	}
	fts := ftsQuery(p.Q)
	sort := p.Sort
	if sort == "" || (sort == "relevance" && fts == "") {
		sort = "recorded_desc"
		if fts != "" {
			sort = "relevance"
		}
	}
	q.limit, q.off = clampLimit(p.Limit, 50, 100), off

	q.from = ` FROM tracks t JOIN recipes r ON r.id = t.recipe_id` + ratingJoins
	args := &q.args
	if fts != "" {
		q.from += ` JOIN (SELECT track_id, bm25(tracks_fts, 0, 10, 2, 2, 1) AS rank FROM tracks_fts WHERE tracks_fts MATCH ?) f ON f.track_id = t.id`
		*args = append(*args, fts)
	}
	where := ` WHERE 1=1`
	if len(p.Status) > 0 {
		where += ` AND t.status IN (` + placeholders(len(p.Status)) + `)`
		for _, st := range p.Status {
			*args = append(*args, st)
		}
	} else {
		where += ` AND t.status <> 'discarded'` // D-M2a-18
	}
	for _, term := range p.Style {
		where += ` AND EXISTS (SELECT 1 FROM track_style_terms x WHERE x.track_id = t.id AND x.term = ?)`
		*args = append(*args, normTerm(term))
	}
	if p.MinDurationS != nil {
		where += ` AND t.duration_s >= ?`
		*args = append(*args, *p.MinDurationS)
	}
	if p.MaxDurationS != nil {
		where += ` AND t.duration_s <= ?`
		*args = append(*args, *p.MaxDurationS)
	}
	if p.RecordedFrom != "" {
		v, err := parseWhen("recorded_from", p.RecordedFrom, false)
		if err != nil {
			return q, err
		}
		where += ` AND t.recorded_at >= ?`
		*args = append(*args, v)
	}
	if p.RecordedTo != "" {
		v, err := parseWhen("recorded_to", p.RecordedTo, true)
		if err != nil {
			return q, err
		}
		if len(p.RecordedTo) == len("2006-01-02") {
			where += ` AND t.recorded_at < ?`
		} else {
			where += ` AND t.recorded_at <= ?`
		}
		*args = append(*args, v)
	}
	if p.HasScore != nil {
		if *p.HasScore {
			where += ` AND EXISTS (SELECT 1 FROM scores s WHERE s.track_id = t.id)`
		} else {
			where += ` AND NOT EXISTS (SELECT 1 FROM scores s WHERE s.track_id = t.id)`
		}
	}
	if p.Truncated != nil {
		cond := `(COALESCE(t.truncated_abc,0) = 1 OR COALESCE(t.truncated_semantic,0) = 1)`
		if !*p.Truncated {
			cond = `NOT ` + cond
		}
		where += ` AND ` + cond
	}
	if p.RootTrackID != "" {
		where += ` AND t.root_track_id = ?`
		*args = append(*args, p.RootTrackID)
	}
	if p.MinStars != nil {
		where += ` AND hs.stars >= ?`
		*args = append(*args, *p.MinStars)
	}
	if p.Unrated {
		where += ` AND hs.stars IS NULL`
	}
	for _, tag := range p.Tags {
		where += ` AND EXISTS (SELECT 1 FROM track_tags tt JOIN tags g ON g.id = tt.tag_id WHERE tt.track_id = t.id AND g.name = ? AND tt.removed_at IS NULL)`
		*args = append(*args, normTag(tag))
	}
	if p.HasOpenComments != nil {
		cond := `EXISTS (SELECT 1 FROM comments c WHERE c.track_id = t.id AND c.state = 'open' AND c.hidden_at IS NULL)`
		if !*p.HasOpenComments {
			cond = `NOT ` + cond
		}
		where += ` AND ` + cond
	}
	if p.RecipeKnown != nil {
		where += ` AND (r.engine <> 'unknown') = ?`
		*args = append(*args, boolInt(*p.RecipeKnown))
	}
	if p.Lossy != nil {
		where += ` AND t.lossy = ?`
		*args = append(*args, boolInt(*p.Lossy))
	}
	if len(p.Origin) > 0 {
		where += ` AND t.origin IN (` + placeholders(len(p.Origin)) + `)`
		for _, o := range p.Origin {
			*args = append(*args, o)
		}
	}
	if p.SongID != "" || p.IsPick != nil || p.SessionID != "" {
		if p.noSongs {
			where += ` AND 0` // a pre-L1 schema has no Songs: the L1 filters match nothing (C98)
		} else {
			if p.SongID != "" {
				where += ` AND EXISTS (SELECT 1 FROM take_songs ts WHERE ts.track_id = t.id AND ts.song_id = ?)`
				*args = append(*args, p.SongID)
			}
			if p.IsPick != nil {
				cond := `EXISTS (SELECT 1 FROM song_picks sp WHERE sp.take_id = t.id)`
				if !*p.IsPick {
					cond = `NOT ` + cond
				}
				where += ` AND ` + cond
			}
			if p.SessionID != "" {
				where += ` AND t.id IN (SELECT m.member_id FROM session_members m WHERE m.session_id = ? AND m.member_kind = 'track'
					UNION SELECT ts2.track_id FROM session_members m JOIN songs sg ON sg.draft_id = m.member_id AND m.member_kind = 'draft'
					JOIN take_songs ts2 ON ts2.song_id = sg.id WHERE m.session_id = ?)`
				*args = append(*args, p.SessionID, p.SessionID)
			}
		}
	}
	q.where = where
	q.order = map[string]string{
		"recorded_desc": `t.recorded_at DESC, t.id DESC`,
		"recorded_asc":  `t.recorded_at ASC, t.id ASC`,
		"duration_desc": `t.duration_s DESC, t.id DESC`,
		"duration_asc":  `t.duration_s ASC, t.id ASC`,
		"title_asc":     `t.title COLLATE NOCASE ASC, t.id ASC`,
		"relevance":     `f.rank ASC, t.id ASC`,
		"stars_desc":    `(hs.stars IS NULL) ASC, hs.stars DESC, t.recorded_at DESC, t.id DESC`,
	}[sort]
	return q, nil
}

// SearchTracks lists tracks with FTS and filters.
func (s *Service) SearchTracks(ctx context.Context, p SearchParams) (TrackPage, error) {
	has, err := s.hasTableDB(ctx, "songs")
	if err != nil {
		return TrackPage{}, err
	}
	p.noSongs = !has
	q, err := compileSearch(p)
	if err != nil {
		return TrackPage{}, err
	}
	var total int
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*)`+q.from+q.where, q.args...).Scan(&total); err != nil {
		return TrackPage{}, fmt.Errorf("library: count tracks: %w", err)
	}
	rows, err := s.st.R.QueryContext(ctx, summarySelect+q.from+q.where+` ORDER BY `+q.order+` LIMIT ? OFFSET ?`, append(slices.Clone(q.args), q.limit, q.off)...)
	if err != nil {
		return TrackPage{}, fmt.Errorf("library: search tracks: %w", err)
	}
	defer rows.Close()
	page := TrackPage{Items: []TrackSummary{}, Total: total}
	pg := newPager(page)
	more := false
	var sums []TrackSummary
	for rows.Next() {
		sum, err := scanSummary(rows)
		if err != nil {
			return TrackPage{}, err
		}
		sums = append(sums, sum)
	}
	if err := rows.Err(); err != nil {
		return TrackPage{}, err
	}
	if err := rows.Close(); err != nil {
		return TrackPage{}, err
	}
	if err := s.fillTags(ctx, sums); err != nil {
		return TrackPage{}, err
	}
	if err := s.fillCosts(ctx, sums); err != nil {
		return TrackPage{}, err
	}
	if has {
		if err := s.fillSongs(ctx, sums); err != nil {
			return TrackPage{}, err
		}
	}
	for _, sum := range sums {
		if !pg.add(sizeOf(sum)) {
			more = true
			break
		}
		page.Items = append(page.Items, sum)
	}
	next := q.off + len(page.Items)
	if more || next < total {
		c := encodeCursor(next)
		page.NextCursor = &c
	}
	return page, nil
}

// MatchTrackIDs returns the id of every track SearchTracks would list for p,
// in the same order, without paging (Limit and Cursor are ignored). Exports use
// it to select tracks with the exact search semantics.
func (s *Service) MatchTrackIDs(ctx context.Context, p SearchParams) ([]string, error) {
	p.Limit, p.Cursor = 0, ""
	has, err := s.hasTableDB(ctx, "songs")
	if err != nil {
		return nil, err
	}
	p.noSongs = !has
	q, err := compileSearch(p)
	if err != nil {
		return nil, err
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT t.id`+q.from+q.where+` ORDER BY `+q.order, q.args...) //nolint:gosec // G202: fragments are fixed strings built by compileSearch
	if err != nil {
		return nil, fmt.Errorf("library: match tracks: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// fillSongs sets song_id, song_title, song_angle and is_pick of a page (L1, C98): two lookups per page, no per-row query.
func (s *Service) fillSongs(ctx context.Context, sums []TrackSummary) error {
	if len(sums) == 0 {
		return nil
	}
	idx := make(map[string]int, len(sums))
	args := make([]any, len(sums))
	for i, x := range sums {
		idx[x.ID] = i
		args[i] = x.ID
	}
	const base = `SELECT ts.track_id, ts.song_id, so.title, so.angle, EXISTS(SELECT 1 FROM song_picks sp WHERE sp.take_id = ts.track_id)
		FROM take_songs ts LEFT JOIN songs so ON so.id = ts.song_id WHERE ts.track_id IN (`
	q := base + placeholders(len(sums)) + `)` //nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := s.st.R.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("library: summary songs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tid, sid string
		var title, angle sql.NullString
		var pick bool
		if err := rows.Scan(&tid, &sid, &title, &angle, &pick); err != nil {
			return err
		}
		sums[idx[tid]].SongID, sums[idx[tid]].IsPick = &sid, pick
		if title.Valid {
			sums[idx[tid]].SongTitle = &title.String
		}
		if angle.Valid && angle.String != "" {
			sums[idx[tid]].SongAngle = &angle.String
		}
	}
	return rows.Err()
}

// fillCosts sets the displayed cost of the generated takes of a page (C81.1); imported takes keep null.
func (s *Service) fillCosts(ctx context.Context, sums []TrackSummary) error {
	for i := range sums {
		if sums[i].jobID == "" {
			continue
		}
		c, err := JobCost(ctx, s.st.R, sums[i].jobID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return err
		}
		v, u := c.Display, money.MicroUSD(c.Display).USD()
		sums[i].CostDisplayMicroUSD, sums[i].CostDisplayUSD = &v, &u
	}
	return nil
}

// fillTags sets the effective tag names of each summary (one query per page).
func (s *Service) fillTags(ctx context.Context, sums []TrackSummary) error {
	if len(sums) == 0 {
		return nil
	}
	idx := make(map[string]int, len(sums))
	args := make([]any, len(sums))
	for i, x := range sums {
		idx[x.ID] = i
		args[i] = x.ID
	}
	const base = `SELECT DISTINCT tt.track_id, g.name FROM track_tags tt JOIN tags g ON g.id = tt.tag_id
		WHERE tt.removed_at IS NULL AND tt.track_id IN (`
	q := base + placeholders(len(sums)) + `) ORDER BY g.name` //nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := s.st.R.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("library: summary tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		sums[idx[id]].Tags = append(sums[idx[id]].Tags, name)
	}
	return rows.Err()
}

func placeholders(n int) string {
	b := make([]byte, 0, 2*n)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}

func normTerm(t string) string {
	terms, _ := yue2.StyleTerms(t)
	if len(terms) == 1 {
		return terms[0]
	}
	return t
}

func scanSummary(rows *sql.Rows) (TrackSummary, error) {
	var (
		t                   TrackSummary
		style               string
		bpm, bw             sql.NullInt64
		trunc               sql.NullInt64
		lufs                sql.NullFloat64
		parent              sql.NullString
		hasScore, timedFlag bool
		stars, critic       sql.NullInt64
		jobID               sql.NullString
	)
	if err := rows.Scan(&t.ID, &t.Title, &t.Status, &t.DurationS, &t.RecordedAt, &t.CreatedAt, &t.RequestID, &t.Seed, &style, &bpm,
		&hasScore, &timedFlag, &trunc, &lufs, &bw, &t.RootTrackID, &parent, &t.ChildrenCount,
		&stars, &critic, &t.Comments, &t.OpenComments, &t.RecipeKnown, &t.Lossy, &t.Origin, &jobID); err != nil {
		return t, fmt.Errorf("library: scan track: %w", err)
	}
	t.HasScore, t.Timed = hasScore, timedFlag
	t.jobID = jobID.String
	terms, _ := yue2.StyleTerms(style)
	if terms == nil {
		terms = []string{}
	}
	t.StyleTerms = terms
	t.Tags = []string{}
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

// TermCount is one style facet.
type TermCount struct {
	Term  string `json:"term"`
	Count int    `json:"count"`
}

// StatusCount is one status facet.
type StatusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// Range is a min/max pair; both null on an empty library.
type Range struct {
	Min *float64 `json:"min"`
	Max *float64 `json:"max"`
}

// TimeRange is a recorded_at min/max pair.
type TimeRange struct {
	Min *string `json:"min"`
	Max *string `json:"max"`
}

// Facets feeds the library filters.
type Facets struct {
	StyleTerms []TermCount   `json:"style_terms" nullable:"false"`
	Statuses   []StatusCount `json:"statuses" nullable:"false"`
	DurationS  Range         `json:"duration_s"`
	RecordedAt TimeRange     `json:"recorded_at"`

	// M2a (C38)
	Tags    []TagFacet  `json:"tags" nullable:"false" doc:"Top 50 effective tags"`
	Stars   []StarFacet `json:"stars" nullable:"false" doc:"Human star histogram"`
	Unrated int         `json:"unrated" doc:"Tracks without a human star rating"`
}

// Facets returns the top 50 style terms, status counts and ranges.
func (s *Service) Facets(ctx context.Context) (Facets, error) {
	f := Facets{StyleTerms: []TermCount{}, Statuses: []StatusCount{}, Tags: []TagFacet{}, Stars: []StarFacet{}}
	rows, err := s.st.R.QueryContext(ctx, `SELECT term, COUNT(*) c FROM track_style_terms GROUP BY term ORDER BY c DESC, term ASC LIMIT 50`)
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var tc TermCount
		if err := rows.Scan(&tc.Term, &tc.Count); err != nil {
			_ = rows.Close()
			return f, err
		}
		f.StyleTerms = append(f.StyleTerms, tc)
	}
	if err := rows.Close(); err != nil {
		return f, err
	}
	rows, err = s.st.R.QueryContext(ctx, `SELECT status, COUNT(*) FROM tracks GROUP BY status ORDER BY status`)
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var sc StatusCount
		if err := rows.Scan(&sc.Status, &sc.Count); err != nil {
			_ = rows.Close()
			return f, err
		}
		f.Statuses = append(f.Statuses, sc)
	}
	if err := rows.Close(); err != nil {
		return f, err
	}
	var dmin, dmax sql.NullFloat64
	var rmin, rmax sql.NullString
	if err := s.st.R.QueryRowContext(ctx, `SELECT MIN(duration_s), MAX(duration_s), MIN(recorded_at), MAX(recorded_at) FROM tracks`).Scan(&dmin, &dmax, &rmin, &rmax); err != nil {
		return f, err
	}
	if dmin.Valid {
		f.DurationS = Range{&dmin.Float64, &dmax.Float64}
		f.RecordedAt = TimeRange{&rmin.String, &rmax.String}
	}
	rows, err = s.st.R.QueryContext(ctx, `SELECT g.name, COUNT(DISTINCT tt.track_id) c FROM track_tags tt JOIN tags g ON g.id = tt.tag_id
		WHERE tt.removed_at IS NULL GROUP BY g.id ORDER BY c DESC, g.name ASC LIMIT 50`)
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var tf TagFacet
		if err := rows.Scan(&tf.Tag, &tf.Count); err != nil {
			_ = rows.Close()
			return f, err
		}
		f.Tags = append(f.Tags, tf)
	}
	if err := rows.Close(); err != nil {
		return f, err
	}
	rows, err = s.st.R.QueryContext(ctx, `SELECT stars, COUNT(*) FROM ratings WHERE source = 'human' AND stars IS NOT NULL GROUP BY stars ORDER BY stars`)
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var sf StarFacet
		if err := rows.Scan(&sf.Stars, &sf.Count); err != nil {
			_ = rows.Close()
			return f, err
		}
		f.Stars = append(f.Stars, sf)
	}
	if err := rows.Close(); err != nil {
		return f, err
	}
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks t WHERE NOT EXISTS
		(SELECT 1 FROM ratings r WHERE r.track_id = t.id AND r.source = 'human' AND r.stars IS NOT NULL)`).Scan(&f.Unrated); err != nil {
		return f, err
	}
	return f, nil
}

// ---- Track detail ----

// MP3Info describes the MP3 rendition inside TrackDetail.
type MP3Info struct {
	SHA256 string  `json:"sha256"`
	Bytes  int64   `json:"bytes"`
	GainDB float64 `json:"gain_db"`
}

// AudioInfo is the frozen master's stream data.
type AudioInfo struct {
	SampleRate    int      `json:"sample_rate"`
	BitsPerSample int      `json:"bits_per_sample" doc:"Nominal (16) for a lossy master"`
	Channels      int      `json:"channels"`
	TotalSamples  int64    `json:"total_samples"`
	MasterSHA256  string   `json:"master_sha256"`
	MasterBytes   int64    `json:"master_bytes"`
	AudioMD5      string   `json:"audio_md5"`
	MP3           *MP3Info `json:"mp3" doc:"MP3 rendition; null when missing"`
}

// RecipeInfo is the generating recipe.
type RecipeInfo struct {
	ID           string `json:"id"`
	Engine       string `json:"engine"`
	RequestID    string `json:"request_id"`
	Style        string `json:"style"`
	Lyrics       string `json:"lyrics"`
	Cot          string `json:"cot"`
	Seed         int64  `json:"seed"`
	Hash         string `json:"hash"`
	SourcesCount int    `json:"sources_count"`
}

// Sibling is another take of the same lineage root.
type Sibling struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Seed       int64  `json:"seed"`
	RecordedAt string `json:"recorded_at"`
}

// Lineage places a track in its family.
type Lineage struct {
	RootTrackID   string    `json:"root_track_id"`
	ParentTrackID *string   `json:"parent_track_id"`
	Generation    int       `json:"generation"`
	Operator      *string   `json:"operator"`
	Inferred      *bool     `json:"inferred"`
	Rule          *string   `json:"rule"`
	Siblings      []Sibling `json:"siblings" nullable:"false"`
}

// Truncation reports YuE2 stream truncation flags.
type Truncation struct {
	ABC      *bool `json:"abc"`
	Semantic *bool `json:"semantic"`
}

// ScoreInfo summarises score.abc.
type ScoreInfo struct {
	Bars          int      `json:"bars"`
	Meter         string   `json:"meter"`
	Q             *string  `json:"q"`
	BPM           *float64 `json:"bpm"`
	SecondsPerBar *float64 `json:"seconds_per_bar"`
	Aligned       bool     `json:"aligned"`
}

// SectionOut is one lyric or plan section; times only when known.
type SectionOut struct {
	Idx        int      `json:"idx"`
	Kind       string   `json:"kind"`
	Label      string   `json:"label"`
	Raw        string   `json:"raw"`
	Lines      []string `json:"lines" nullable:"false"`
	StartS     *float64 `json:"start_s"`
	EndS       *float64 `json:"end_s"`
	Source     string   `json:"source" doc:"lyrics | plan | measured | manual"`
	Confidence *float64 `json:"confidence"`
}

// LoudnessOut is the stored loudness analysis.
type LoudnessOut struct {
	analysis.Loudness
	ReplayGainTrackGainDB float64 `json:"replaygain_track_gain_db"`
	Version               string  `json:"version"`
}

// SpectrumOut is the stored bandwidth analysis.
type SpectrumOut struct {
	analysis.Spectrum
	Version string `json:"version"`
}

// AnalysisInfo groups analyses.
type AnalysisInfo struct {
	Loudness *LoudnessOut `json:"loudness" doc:"null when not analysed"`
	Spectrum *SpectrumOut `json:"spectrum" doc:"null when not analysed"`
}

// ImportInfo says how a track entered the library.
type ImportInfo struct {
	Rule  string  `json:"rule"`
	RunID *string `json:"run_id"`
}

// TrackDetail is the full track view.
type TrackDetail struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Slug        string       `json:"slug"`
	Status      string       `json:"status"`
	StatusSetBy string       `json:"status_set_by"`
	Origin      string       `json:"origin"`
	RecordedAt  string       `json:"recorded_at"`
	CreatedAt   string       `json:"created_at"`
	DurationS   float64      `json:"duration_s"`
	Audio       AudioInfo    `json:"audio"`
	Recipe      RecipeInfo   `json:"recipe"`
	StyleTerms  []string     `json:"style_terms" nullable:"false"`
	BPMDeclared *int         `json:"bpm_declared"`
	Lineage     Lineage      `json:"lineage"`
	Truncated   *Truncation  `json:"truncated" doc:"null when result.json was not imported"`
	Score       *ScoreInfo   `json:"score" doc:"null when the track has no score"`
	Sections    []SectionOut `json:"sections" nullable:"false"`
	Timed       bool         `json:"timed"`
	Analysis    AnalysisInfo `json:"analysis"`
	Import      ImportInfo   `json:"import"`

	// M2a (C38)
	Rating       TrackRating    `json:"rating"`
	Tags         []TagRef       `json:"tags" nullable:"false"`
	Comments     int            `json:"comments"`
	OpenComments int            `json:"open_comments"`
	Playlists    []PlaylistRef  `json:"playlists" nullable:"false"`
	Listening    ListeningStats `json:"listening"`

	// M2c (C59)
	RecipeKnown bool `json:"recipe_known" doc:"false for a reference track: its recipe is a stub"`
	Lossy       bool `json:"lossy" doc:"The master is an MP3; bits_per_sample is nominal and there is no lossless file"`

	// M3 (C81.1)
	Generation *GenerationInfo `json:"generation" doc:"How the take was generated; null for imported takes"`

	// L1 (C98)
	Song *TrackSong `json:"song" doc:"The Song of the take; null before the Song exists or on a pre-L1 schema"`
}

// TrackSong is the Song reference of a TrackDetail (C98).
type TrackSong struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Angle      *string `json:"angle"`
	MuseID     *string `json:"muse_id"`
	PickSource *string `json:"pick_source" doc:"How the Song's current Pick was chosen; null when it has none"`
}

func nb(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Int64 == 1
	return &v
}

// sectionRank orders section sources: the most informative wins.
var sectionRank = map[string]int{"manual": 4, "measured": 3, "plan": 2, "lyrics": 1}

// selectSections is the one section-selection rule (shared by Track and Sections): the highest-ranked
// source present wins; timed means every row of the winner has a start. It returns "" when bySource is
// empty.
func selectSections(bySource map[string][]SectionOut) (best string, rows []SectionOut, timed bool) {
	for src := range bySource {
		if sectionRank[src] > sectionRank[best] {
			best = src
		}
	}
	if best == "" {
		return "", nil, false
	}
	rows = bySource[best]
	timed = len(rows) > 0
	for _, so := range rows {
		if so.StartS == nil || so.EndS == nil {
			timed = false
		}
	}
	return best, rows, timed
}

// Track returns the full detail of one track.
func (s *Service) Track(ctx context.Context, id string) (TrackDetail, error) {
	d := TrackDetail{Tags: []TagRef{}, Playlists: []PlaylistRef{}}
	d.Rating.TrackID = id
	var (
		parent, runID sql.NullString
		bpm           sql.NullInt64
		tabc, tsem    sql.NullInt64
		recipeID      string
	)
	err := s.st.R.QueryRowContext(ctx, `SELECT t.id, t.title, t.slug, t.status, t.status_set_by, t.origin, t.recorded_at, t.created_at, t.duration_s,
		t.sample_rate, t.bits_per_sample, t.channels, t.total_samples, t.master_sha256, t.master_bytes, t.audio_md5,
		t.bpm_declared, t.root_track_id, t.parent_track_id, t.generation, t.truncated_abc, t.truncated_semantic, t.import_rule, t.import_run_id,
		t.lossy, r.id, r.engine, r.request_id, r.style, r.lyrics, r.cot, r.seed, r.hash,
		(SELECT COUNT(*) FROM recipe_sources rs WHERE rs.recipe_id = r.id)
		FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, id).
		Scan(&d.ID, &d.Title, &d.Slug, &d.Status, &d.StatusSetBy, &d.Origin, &d.RecordedAt, &d.CreatedAt, &d.DurationS,
			&d.Audio.SampleRate, &d.Audio.BitsPerSample, &d.Audio.Channels, &d.Audio.TotalSamples, &d.Audio.MasterSHA256, &d.Audio.MasterBytes, &d.Audio.AudioMD5,
			&bpm, &d.Lineage.RootTrackID, &parent, &d.Lineage.Generation, &tabc, &tsem, &d.Import.Rule, &runID,
			&d.Lossy, &recipeID, &d.Recipe.Engine, &d.Recipe.RequestID, &d.Recipe.Style, &d.Recipe.Lyrics, &d.Recipe.Cot, &d.Recipe.Seed, &d.Recipe.Hash, &d.Recipe.SourcesCount)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, fmt.Errorf("library: track: %w", err)
	}
	d.Recipe.ID = recipeID
	d.RecipeKnown = RecipeKnownEngine(d.Recipe.Engine)
	if bpm.Valid {
		v := int(bpm.Int64)
		d.BPMDeclared = &v
	}
	if parent.Valid {
		d.Lineage.ParentTrackID = &parent.String
	}
	if runID.Valid {
		d.Import.RunID = &runID.String
	}
	if tabc.Valid || tsem.Valid {
		d.Truncated = &Truncation{ABC: nb(tabc), Semantic: nb(tsem)}
	}
	d.StyleTerms, _ = yue2.StyleTerms(d.Recipe.Style)
	if d.StyleTerms == nil {
		d.StyleTerms = []string{}
	}
	if err := s.trackExtras(ctx, &d); err != nil {
		return d, err
	}
	if err := s.judgmentExtras(ctx, &d); err != nil {
		return d, err
	}
	if d.Origin == "job" {
		if d.Generation, err = GenerationOf(ctx, s.st.R, id); err != nil {
			return d, err
		}
	}
	if d.Song, err = s.trackSong(ctx, id); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Service) trackExtras(ctx context.Context, d *TrackDetail) error {
	q := s.st.R
	// mp3
	var mp3 MP3Info
	var params string
	switch err := q.QueryRowContext(ctx, `SELECT sha256, bytes, params_json FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, d.ID).Scan(&mp3.SHA256, &mp3.Bytes, &params); {
	case err == nil:
		var p struct {
			GainDB float64 `json:"gain_db"`
		}
		_ = json.Unmarshal([]byte(params), &p)
		mp3.GainDB = p.GainDB
		d.Audio.MP3 = &mp3
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	// lineage
	var op, rule sql.NullString
	var inf sql.NullInt64
	var pj sql.NullString
	switch err := q.QueryRowContext(ctx, `SELECT operator, inferred, params_json FROM mutations WHERE child_track_id = ?`, d.ID).Scan(&op, &inf, &pj); {
	case err == nil:
		d.Lineage.Operator, d.Lineage.Inferred = &op.String, nb(inf)
		if pj.Valid {
			var p struct {
				Rule string `json:"rule"`
			}
			if json.Unmarshal([]byte(pj.String), &p) == nil && p.Rule != "" {
				rule = sql.NullString{String: p.Rule, Valid: true}
				d.Lineage.Rule = &rule.String
			}
		}
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	d.Lineage.Siblings = []Sibling{}
	rows, err := q.QueryContext(ctx, `SELECT t.id, t.title, r.seed, t.recorded_at FROM tracks t JOIN recipes r ON r.id = t.recipe_id
		WHERE t.root_track_id = ? AND t.id <> ? ORDER BY t.recorded_at, t.id`, d.Lineage.RootTrackID, d.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sb Sibling
		if err := rows.Scan(&sb.ID, &sb.Title, &sb.Seed, &sb.RecordedAt); err != nil {
			_ = rows.Close()
			return err
		}
		d.Lineage.Siblings = append(d.Lineage.Siblings, sb)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// score
	var sc ScoreInfo
	var qv sql.NullString
	var bpm, spb sql.NullFloat64
	switch err := q.QueryRowContext(ctx, `SELECT bars, meter, q, bpm, seconds_per_bar, aligned FROM scores WHERE track_id = ?`, d.ID).Scan(&sc.Bars, &sc.Meter, &qv, &bpm, &spb, &sc.Aligned); {
	case err == nil:
		if qv.Valid {
			sc.Q = &qv.String
		}
		if bpm.Valid {
			sc.BPM = &bpm.Float64
		}
		if spb.Valid {
			sc.SecondsPerBar = &spb.Float64
		}
		d.Score = &sc
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	// sections: the highest-ranked source present
	d.Sections = []SectionOut{}
	rows, err = q.QueryContext(ctx, `SELECT source, idx, kind, label, raw_label, start_s, end_s, confidence, lines_json FROM sections WHERE track_id = ? ORDER BY idx`, d.ID)
	if err != nil {
		return err
	}
	bySource := map[string][]SectionOut{}
	for rows.Next() {
		var so SectionOut
		var st, en, conf sql.NullFloat64
		var lj sql.NullString
		if err := rows.Scan(&so.Source, &so.Idx, &so.Kind, &so.Label, &so.Raw, &st, &en, &conf, &lj); err != nil {
			_ = rows.Close()
			return err
		}
		if st.Valid {
			so.StartS, so.EndS = &st.Float64, &en.Float64
		}
		if conf.Valid {
			so.Confidence = &conf.Float64
		}
		so.Lines = []string{}
		if lj.Valid {
			_ = json.Unmarshal([]byte(lj.String), &so.Lines)
			if so.Lines == nil {
				so.Lines = []string{}
			}
		}
		bySource[so.Source] = append(bySource[so.Source], so)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if best, rows, timed := selectSections(bySource); best != "" {
		d.Sections, d.Timed = rows, timed
	}
	// analyses (latest version of each)
	rows, err = q.QueryContext(ctx, `SELECT analyzer, version, metrics_json FROM analyses WHERE track_id = ? ORDER BY created_at ASC, version ASC`, d.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var an, ver, mj string
		if err := rows.Scan(&an, &ver, &mj); err != nil {
			_ = rows.Close()
			return err
		}
		switch an {
		case "loudness":
			var l LoudnessOut
			if json.Unmarshal([]byte(mj), &l) == nil {
				if l.ShortTerm1Hz == nil {
					l.ShortTerm1Hz = []float64{}
				}
				l.Version = ver
				d.Analysis.Loudness = &l
			}
		case "spectrum":
			var sp SpectrumOut
			if json.Unmarshal([]byte(mj), &sp) == nil {
				sp.Version = ver
				d.Analysis.Spectrum = &sp
			}
		}
	}
	return rows.Close()
}

// PeaksLevel is one waveform level of a track.
type PeaksLevel struct {
	TrackID   string            `json:"track_id"`
	Level     string            `json:"level"`
	DurationS float64           `json:"duration_s"`
	Peaks     analysis.Waveform `json:"peaks"`
}

// Peaks returns the overview or detail waveform from the peaks rendition.
func (s *Service) Peaks(ctx context.Context, id, level string) (PeaksLevel, error) {
	if level == "" {
		level = "overview"
	}
	if !oneOf(level, "overview", "detail") {
		return PeaksLevel{}, invalid("level", "must be overview or detail")
	}
	var path string
	err := s.st.R.QueryRowContext(ctx, `SELECT r.path FROM renditions r WHERE r.track_id = ? AND r.kind = 'peaks'`, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		if err := s.st.R.QueryRowContext(ctx, `SELECT id FROM tracks WHERE id = ?`, id).Scan(&path); errors.Is(err, sql.ErrNoRows) {
			return PeaksLevel{}, ErrNotFound
		}
		return PeaksLevel{}, ErrMediaUnavailable
	}
	if err != nil {
		return PeaksLevel{}, err
	}
	if s.data == nil {
		return PeaksLevel{}, ErrMediaUnavailable
	}
	f, err := s.data.Open(path)
	if err != nil {
		return PeaksLevel{}, fmt.Errorf("library: open peaks: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return PeaksLevel{}, fmt.Errorf("library: read peaks: %w", err)
	}
	var pk analysis.Peaks
	if err := json.Unmarshal(b, &pk); err != nil {
		return PeaksLevel{}, fmt.Errorf("library: parse peaks: %w", err)
	}
	out := PeaksLevel{TrackID: id, Level: level, DurationS: pk.DurationS, Peaks: pk.Overview}
	if level == "detail" {
		out.Peaks = pk.Detail
	}
	if out.Peaks.Data == nil {
		out.Peaks.Data = []int8{}
	}
	return out, nil
}

// FlacMedia describes the master as a media object.
type FlacMedia struct {
	SHA256        string `json:"sha256"`
	ContentType   string `json:"content_type"`
	Bytes         int64  `json:"bytes"`
	SampleRate    int    `json:"sample_rate"`
	BitsPerSample int    `json:"bits_per_sample"`
}

// MP3Media describes the MP3 rendition as a media object.
type MP3Media struct {
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	BitrateKbps int    `json:"bitrate_kbps"`
}

// MediaSet is what get_media_links signs. Flac is nil for a lossy track (no
// lossless file exists); MP3 is then the master itself.
type MediaSet struct {
	TrackID string
	Flac    *FlacMedia
	MP3     MP3Media
	Lossy   bool
}

// Media returns the media addresses of a track.
func (s *Service) Media(ctx context.Context, trackID string) (MediaSet, error) {
	m := MediaSet{TrackID: trackID}
	var fl FlacMedia
	var dur float64
	var lossy int
	err := s.st.R.QueryRowContext(ctx, `SELECT master_sha256, master_bytes, sample_rate, bits_per_sample, duration_s, lossy FROM tracks WHERE id = ?`, trackID).
		Scan(&fl.SHA256, &fl.Bytes, &fl.SampleRate, &fl.BitsPerSample, &dur, &lossy)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	if lossy == 1 {
		// D-M2c-7: the master is the MP3; the bitrate is what the file averages.
		m.Lossy = true
		m.MP3 = MP3Media{SHA256: fl.SHA256, ContentType: "audio/mpeg", Bytes: fl.Bytes}
		if dur > 0 {
			m.MP3.BitrateKbps = int(math.Round(float64(fl.Bytes) * 8 / dur / 1000))
		}
		return m, nil
	}
	fl.ContentType = "audio/flac"
	m.Flac = &fl
	err = s.st.R.QueryRowContext(ctx, `SELECT sha256, bytes FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, trackID).Scan(&m.MP3.SHA256, &m.MP3.Bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrMediaUnavailable
	}
	if err != nil {
		return m, err
	}
	m.MP3.ContentType, m.MP3.BitrateKbps = "audio/mpeg", 320
	return m, nil
}

// Resolve implements serve.Resolver: flac addresses masters, mp3 addresses
// the MP3 rendition, jpg the spectrogram rendition.
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) {
	var (
		q  string
		ct string
	)
	switch ext {
	case "flac":
		q, ct = `SELECT master_path, created_at FROM tracks WHERE master_sha256 = ? AND lossy = 0`, "audio/flac"
	case "mp3":
		// the MP3 rendition, or the master of a lossy track (D-M2c-7)
		q, ct = `SELECT path, created_at FROM renditions WHERE kind = 'mp3_320' AND sha256 = ?
			UNION ALL SELECT master_path, created_at FROM tracks WHERE lossy = 1 AND master_sha256 = ? LIMIT 1`, "audio/mpeg"
	case "jpg":
		q, ct = `SELECT path, created_at FROM renditions WHERE kind = 'spectrogram' AND sha256 = ? LIMIT 1`, "image/jpeg"
	default:
		return serve.Object{}, serve.ErrNotFound
	}
	var path, created string
	args := []any{sha}
	if ext == "mp3" {
		args = append(args, sha)
	}
	err := s.st.R.QueryRowContext(ctx, q, args...).Scan(&path, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return serve.Object{}, serve.ErrNotFound
	}
	if err != nil {
		return serve.Object{}, err
	}
	mt, _ := time.Parse(time.RFC3339, created)
	return serve.Object{RelPath: path, ContentType: ct, ETag: sha, ModTime: mt}, nil
}

// rowQuerier is the read side of *sql.DB and *sql.Tx.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// JobCostInfo is the displayed cost of a generated take (D-M3-17): its provisional execution cost plus its
// share (ceiling of settled overhead over the batch's takes) of the overhead settled at batch end,
// `reconciled` once billing matched the batch, `estimate` while nothing was settled.
type JobCostInfo struct {
	Expected, Display int64
	Basis             string // estimate | provisional | reconciled
}

// JobCost reads the cost of one job from the ledger.
func JobCost(ctx context.Context, x rowQuerier, jobID string) (JobCostInfo, error) {
	var c JobCostInfo
	var state string
	var takes int
	var reconciled bool
	var execSettled, rows, overhead int64
	err := x.QueryRowContext(ctx, `SELECT j.expected_micro_usd, j.state, b.n_takes, b.reconciled_at IS NOT NULL,
		(SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE job_id = j.id AND kind = 'settle'),
		(SELECT COUNT(*) FROM ledger WHERE job_id = j.id AND kind = 'settle'),
		(SELECT COALESCE(SUM(micro_usd), 0) FROM ledger WHERE batch_id = b.id AND kind = 'settle' AND reason = 'overhead')
		FROM jobs j JOIN batches b ON b.id = j.batch_id WHERE j.id = ?`, jobID).
		Scan(&c.Expected, &state, &takes, &reconciled, &execSettled, &rows, &overhead)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, fmt.Errorf("library: job cost: %w", err)
	}
	share := int64(0)
	if takes > 0 {
		share = (overhead + int64(takes) - 1) / int64(takes)
	}
	terminal := state == "completed" || state == "failed" || state == "cancelled" || state == "expired"
	switch {
	case rows == 0 && !terminal:
		c.Display, c.Basis = c.Expected, "estimate"
	case reconciled:
		c.Display, c.Basis = execSettled+share, "reconciled"
	case rows == 0 && state == "completed":
		c.Display, c.Basis = c.Expected, "estimate"
	default:
		c.Display, c.Basis = execSettled+share, "provisional"
	}
	return c, nil
}

// EnvByID reads an environment fingerprint; nil when the id is empty or unknown.
func EnvByID(ctx context.Context, x rowQuerier, envID string) (*Env, error) {
	if envID == "" {
		return nil, nil
	}
	var e Env
	var weights, detail string
	err := x.QueryRowContext(ctx, `SELECT env_id, engine, pkg_version, code_commit, weights_json, decoder, backend, gpu_class, image_digest, detail_json FROM env_fingerprints WHERE env_id = ?`, envID).
		Scan(&e.EnvID, &e.Engine, &e.PkgVersion, &e.CodeCommit, &weights, &e.Decoder, &e.Backend, &e.GPUClass, &e.ImageDigest, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("library: env fingerprint: %w", err)
	}
	if err := json.Unmarshal([]byte(weights), &e.Weights); err != nil || e.Weights == nil {
		e.Weights = map[string]string{}
	}
	_ = json.Unmarshal([]byte(detail), &e.Detail)
	return &e, nil
}

// GenerationOf reads how a take was generated; nil for imported takes (C81.1).
func GenerationOf(ctx context.Context, x rowQuerier, trackID string) (*GenerationInfo, error) {
	var g GenerationInfo
	var jobID, envID, rationale, mutation sql.NullString
	err := x.QueryRowContext(ctx, `SELECT t.job_id, t.env_id, j.batch_id, j.operator, j.mutation_json FROM tracks t JOIN jobs j ON j.id = t.job_id WHERE t.id = ? AND t.origin = 'job'`, trackID).
		Scan(&jobID, &envID, &g.BatchID, &g.Operator, &mutation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("library: generation of %s: %w", trackID, err)
	}
	g.JobID = jobID.String
	var m struct {
		Rationale *string `json:"rationale"`
	}
	if json.Unmarshal([]byte(mutation.String), &m) == nil && m.Rationale != nil {
		rationale = sql.NullString{String: *m.Rationale, Valid: true}
	}
	if rationale.Valid {
		g.Rationale = &rationale.String
	}
	c, err := JobCost(ctx, x, g.JobID)
	if err != nil {
		return nil, err
	}
	g.Cost = &GenerationCost{DisplayMicroUSD: c.Display, DisplayUSD: money.MicroUSD(c.Display).USD(), Basis: c.Basis}
	if g.Env, err = EnvByID(ctx, x, envID.String); err != nil {
		return nil, err
	}
	return &g, nil
}

// HasProvider reports whether a generation provider is configured (a provider_state row exists), the
// default test behind the generation hint of the context bundle.
func (s *Service) HasProvider(ctx context.Context) (bool, error) {
	var n int
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_state`).Scan(&n); err != nil {
		return false, fmt.Errorf("library: provider state: %w", err)
	}
	return n > 0, nil
}

// Slug is the filename-safe form of a title (D-M1-3); callers that may not import the engine
// implementation (the job ingest bridge) use it instead of yue2.Slug.
func Slug(s string) string { return yue2.Slug(s) }

// trackSong reads the Song reference of a take; nil on a pre-L1 schema or when the take has no Song yet.
func (s *Service) trackSong(ctx context.Context, trackID string) (*TrackSong, error) {
	has, err := s.hasTableDB(ctx, "songs")
	if err != nil || !has {
		return nil, err
	}
	var out TrackSong
	var angle, muse, src sql.NullString
	err = s.st.R.QueryRowContext(ctx, `SELECT g.id, g.title, g.angle, g.muse_id, (SELECT p.source FROM song_picks p WHERE p.song_id = g.id AND p.take_id IS NOT NULL)
		FROM take_songs ts JOIN songs g ON g.id = ts.song_id WHERE ts.track_id = ?`, trackID).Scan(&out.ID, &out.Title, &angle, &muse, &src)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("library: track song: %w", err)
	}
	out.Angle, out.MuseID, out.PickSource = ptrStr(angle), ptrStr(muse), ptrStr(src)
	return &out, nil
}
