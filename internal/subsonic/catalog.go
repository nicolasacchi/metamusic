package subsonic

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// This file holds every SQL statement of the catalog endpoints (C109.1): read pool only, songs are tracks, albums
// are lineages (root_track_id). Every ORDER BY ends with the row id so pages are stable.

// errUnknownFolder is returned for a musicFolderId other than 1, 2 or 3.
var errUnknownFolder = errors.New("subsonic: unknown music folder")

// folderWhere is the one WHERE fragment of D-M4a-10 (alias t): folder 1 "Library" = final or to_relisten, folder 2
// "Drafts" = draft, folder 3 "Discarded" = discarded; no folder = 1 + 2.
func folderWhere(folder int) (string, error) {
	switch folder {
	case 0:
		return `t.status IN ('final','to_relisten','draft')`, nil
	case 1:
		return `t.status IN ('final','to_relisten')`, nil
	case 2:
		return `t.status = 'draft'`, nil
	case 3:
		return `t.status = 'discarded'`, nil
	}
	return "", errUnknownFolder
}

// catalog reads the library through the read pool.
type catalog struct{ db *sql.DB }

// songRow is one track as the Subsonic catalog sees it.
type songRow struct {
	ID, Root, Title, Slug, Status, StatusSetAt, CreatedAt, RecordedAt string
	DurationS                                                         float64
	SampleRate, BitsPerSample, Channels                               int
	MasterBytes                                                       int64
	Lossy                                                             bool
	BPM                                                               sql.NullInt64
	AlbumTitle, AlbumSlug                                             string
	Seed                                                              int64
	Style                                                             string
	Pos                                                               int
	MP3Bytes                                                          sql.NullInt64
	Plays                                                             int64
	LastPlay                                                          sql.NullString
	Stars                                                             sql.NullInt64
	LUFS, TruePeak                                                    sql.NullFloat64
	Terms                                                             sql.NullString
}

const songSelect = `SELECT t.id, t.root_track_id, t.title, t.slug, t.status, COALESCE(t.status_set_at, t.created_at), t.created_at, t.recorded_at,
 t.duration_s, t.sample_rate, t.bits_per_sample, t.channels, t.master_bytes, t.lossy, t.bpm_declared,
 rt.title, rt.slug, r.seed, r.style,
 (SELECT COUNT(*) FROM tracks m WHERE m.root_track_id = t.root_track_id AND (m.recorded_at < t.recorded_at OR (m.recorded_at = t.recorded_at AND m.id <= t.id))),
 (SELECT bytes FROM renditions n WHERE n.track_id = t.id AND n.kind = 'mp3_320'),
 (SELECT COUNT(*) FROM listen_events e WHERE e.track_id = t.id AND e.kind = 'start' AND e.principal_kind IN ('human','device')),
 (SELECT MAX(e.client_at) FROM listen_events e WHERE e.track_id = t.id AND e.kind = 'start' AND e.principal_kind IN ('human','device')),
 (SELECT stars FROM ratings g WHERE g.track_id = t.id AND g.source = 'human'),
 (SELECT json_extract(a.metrics_json, '$.integrated_lufs') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'loudness' ORDER BY a.created_at DESC, a.version DESC LIMIT 1),
 (SELECT json_extract(a.metrics_json, '$.true_peak_dbtp') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'loudness' ORDER BY a.created_at DESC, a.version DESC LIMIT 1),
 (SELECT group_concat(ts.term, char(31)) FROM track_style_terms ts WHERE ts.track_id = t.id)
 FROM tracks t JOIN tracks rt ON rt.id = t.root_track_id JOIN recipes r ON r.id = t.recipe_id`

func scanSong(rows interface{ Scan(...any) error }) (songRow, error) {
	var s songRow
	var lossy int
	err := rows.Scan(&s.ID, &s.Root, &s.Title, &s.Slug, &s.Status, &s.StatusSetAt, &s.CreatedAt, &s.RecordedAt,
		&s.DurationS, &s.SampleRate, &s.BitsPerSample, &s.Channels, &s.MasterBytes, &lossy, &s.BPM,
		&s.AlbumTitle, &s.AlbumSlug, &s.Seed, &s.Style, &s.Pos, &s.MP3Bytes, &s.Plays, &s.LastPlay, &s.Stars,
		&s.LUFS, &s.TruePeak, &s.Terms)
	s.Lossy = lossy == 1
	return s, err
}

// genre is the first style term of the recipe that the track also carries as a term row (D-M4a-11).
func (s songRow) genre() string {
	if !s.Terms.Valid || s.Terms.String == "" {
		return ""
	}
	have := map[string]bool{}
	for _, t := range strings.Split(s.Terms.String, "\x1f") {
		have[t] = true
	}
	terms, _ := yue2.StyleTerms(s.Style)
	for _, t := range terms {
		if have[t] {
			return t
		}
	}
	return ""
}

// songQuery selects songs: where is an AND-ed fragment over alias t (may be empty), order a full ORDER BY body.
type songQuery struct {
	where  string
	args   []any
	order  string
	limit  int // 0 => no limit
	offset int
	fts    string // optional FTS5 expression restricting to matching tracks (order by rank first)
}

func (c *catalog) songs(ctx context.Context, q songQuery) ([]songRow, error) {
	var b strings.Builder
	var args []any
	b.WriteString(songSelect)
	if q.fts != "" {
		b.WriteString(` JOIN (SELECT track_id, bm25(tracks_fts, 0, 10, 2, 2, 1) AS rank FROM tracks_fts WHERE tracks_fts MATCH ?) f ON f.track_id = t.id`)
		args = append(args, q.fts)
	}
	if q.where != "" {
		b.WriteString(" WHERE " + q.where)
		args = append(args, q.args...)
	}
	order := q.order
	if order == "" {
		order = "t.created_at, t.id"
	}
	if q.fts != "" {
		order = "f.rank, " + order
	}
	b.WriteString(" ORDER BY " + order)
	if q.limit > 0 {
		b.WriteString(" LIMIT ? OFFSET ?")
		args = append(args, q.limit, q.offset)
	} else if q.offset > 0 {
		b.WriteString(" LIMIT -1 OFFSET ?")
		args = append(args, q.offset)
	}
	rows, err := c.db.QueryContext(ctx, b.String(), args...) //nolint:gosec // G202: fragments are fixed strings from this package
	if err != nil {
		return nil, fmt.Errorf("subsonic: songs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []songRow
	for rows.Next() {
		s, err := scanSong(rows)
		if err != nil {
			return nil, fmt.Errorf("subsonic: scan song: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// song returns one track by id regardless of status (a discarded take stays playable by id).
func (c *catalog) song(ctx context.Context, id string) (songRow, bool, error) {
	rows, err := c.songs(ctx, songQuery{where: "t.id = ?", args: []any{id}})
	if err != nil || len(rows) == 0 {
		return songRow{}, false, err
	}
	return rows[0], true, nil
}

// songsByIDs returns the tracks with the given ids in that order (unknown ids are skipped).
func (c *catalog) songsByIDs(ctx context.Context, ids []string) ([]songRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := c.songs(ctx, songQuery{where: "t.id IN (" + ph + ")", args: args})
	if err != nil {
		return nil, err
	}
	by := make(map[string]songRow, len(rows))
	for _, r := range rows {
		by[r.ID] = r
	}
	out := make([]songRow, 0, len(ids))
	for _, id := range ids {
		if r, ok := by[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// songIDs lists the ids matching the fragment, in id order (the input of the seeded shuffle).
func (c *catalog) songIDs(ctx context.Context, where string, args []any) ([]string, error) {
	return c.ids(ctx, `SELECT t.id FROM tracks t WHERE `+where+` ORDER BY t.id`, args)
}

func (c *catalog) ids(ctx context.Context, q string, args []any) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, q, args...) //nolint:gosec // G202: fixed fragments
	if err != nil {
		return nil, fmt.Errorf("subsonic: ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// albumRow is one lineage aggregated over its members inside the folder rule.
type albumRow struct {
	ID, Name, Slug, Created string
	RootRecorded            string
	Style                   string
	Year                    int
	SongCount               int
	DurationS               float64
	Plays                   int64
	LastPlay                sql.NullString
	Stars                   sql.NullInt64
	Starred                 sql.NullString
	Modified                string
}

func (a albumRow) genre() string {
	terms, _ := yue2.StyleTerms(a.Style)
	if len(terms) == 0 {
		return ""
	}
	return terms[0]
}

// albumAgg is the lineage aggregate over alias t (with the folder fragment); %s is that fragment.
const albumAgg = `SELECT t.root_track_id AS id, rt.title AS name, rt.slug AS slug, rt.created_at AS created, rt.recorded_at AS rrec, rr.style AS style,
 CAST(substr(rt.recorded_at, 1, 4) AS INTEGER) AS year, COUNT(*) AS songs, SUM(t.duration_s) AS dur,
 SUM((SELECT COUNT(*) FROM listen_events e WHERE e.track_id = t.id AND e.kind = 'start' AND e.principal_kind IN ('human','device'))) AS plays,
 MAX((SELECT MAX(e.client_at) FROM listen_events e WHERE e.track_id = t.id AND e.kind = 'start' AND e.principal_kind IN ('human','device'))) AS last_play,
 MAX((SELECT stars FROM ratings g WHERE g.track_id = t.id AND g.source = 'human')) AS stars,
 MAX(CASE WHEN t.status = 'final' THEN COALESCE(t.status_set_at, t.created_at) END) AS starred,
 MAX(MAX(t.created_at), MAX(COALESCE(t.status_set_at, t.created_at))) AS modified
 FROM tracks t JOIN tracks rt ON rt.id = t.root_track_id JOIN recipes rr ON rr.id = rt.recipe_id
 WHERE %s GROUP BY t.root_track_id`

// albumQuery selects albums: where is over alias a (outer), order a full ORDER BY body (default name, id).
type albumQuery struct {
	folderWhere string // over alias t
	where       string // over alias a; may be empty
	args        []any
	order       string
	limit       int
	offset      int
}

func (c *catalog) albums(ctx context.Context, q albumQuery) ([]albumRow, error) {
	var b strings.Builder
	b.WriteString("SELECT a.* FROM (" + fmt.Sprintf(albumAgg, q.folderWhere) + ") a")
	if q.where != "" {
		b.WriteString(" WHERE " + q.where)
	}
	order := q.order
	if order == "" {
		order = "lower(a.name), a.id"
	}
	b.WriteString(" ORDER BY " + order)
	args := append([]any(nil), q.args...)
	if q.limit > 0 {
		b.WriteString(" LIMIT ? OFFSET ?")
		args = append(args, q.limit, q.offset)
	} else if q.offset > 0 {
		b.WriteString(" LIMIT -1 OFFSET ?")
		args = append(args, q.offset)
	}
	rows, err := c.db.QueryContext(ctx, b.String(), args...) //nolint:gosec // G202: fixed fragments
	if err != nil {
		return nil, fmt.Errorf("subsonic: albums: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []albumRow
	for rows.Next() {
		var a albumRow
		if err := rows.Scan(&a.ID, &a.Name, &a.Slug, &a.Created, &a.RootRecorded, &a.Style, &a.Year, &a.SongCount, &a.DurationS,
			&a.Plays, &a.LastPlay, &a.Stars, &a.Starred, &a.Modified); err != nil {
			return nil, fmt.Errorf("subsonic: scan album: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// albumIDs lists the lineage ids with a member in the folder rule, in id order (the input of the seeded shuffle).
func (c *catalog) albumIDs(ctx context.Context, folderFrag string) ([]string, error) {
	return c.ids(ctx, `SELECT DISTINCT t.root_track_id FROM tracks t WHERE `+folderFrag+` ORDER BY t.root_track_id`, nil)
}

// albumsByIDs returns the albums with the given root ids in that order (members restricted by the folder rule).
func (c *catalog) albumsByIDs(ctx context.Context, folderFrag string, ids []string) ([]albumRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := c.albums(ctx, albumQuery{folderWhere: folderFrag, where: "a.id IN (" + ph + ")", args: args})
	if err != nil {
		return nil, err
	}
	by := make(map[string]albumRow, len(rows))
	for _, r := range rows {
		by[r.ID] = r
	}
	out := make([]albumRow, 0, len(ids))
	for _, id := range ids {
		if r, ok := by[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// albumExists reports whether any track (in any folder) belongs to the lineage.
func (c *catalog) albumExists(ctx context.Context, root string) (bool, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE root_track_id = ?`, root).Scan(&n)
	return n > 0, err
}

// genreRow is one style term with at least one song in the folder rule.
type genreRow struct {
	Term                string
	SongCount, AlbumCnt int
}

func (c *catalog) genres(ctx context.Context, folderFrag string) ([]genreRow, error) {
	q := `SELECT ts.term, COUNT(DISTINCT t.id), COUNT(DISTINCT t.root_track_id) FROM track_style_terms ts JOIN tracks t ON t.id = ts.track_id WHERE ` + folderFrag + ` GROUP BY ts.term ORDER BY 2 DESC, 1` //nolint:gosec // G202: fixed fragment
	rows, err := c.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("subsonic: genres: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []genreRow
	for rows.Next() {
		var g genreRow
		if err := rows.Scan(&g.Term, &g.SongCount, &g.AlbumCnt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// countSongs counts the songs in the folder rule.
func (c *catalog) countSongs(ctx context.Context, folderFrag string) (int, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks t WHERE `+folderFrag).Scan(&n) //nolint:gosec // G202: fixed fragment
	return n, err
}

// lastModified is the newest created_at / status_set_at in the folder rule, in Unix milliseconds (0 when empty).
func (c *catalog) lastModified(ctx context.Context, folderFrag string) (int64, error) {
	var created, set sql.NullString
	err := c.db.QueryRowContext(ctx, `SELECT MAX(t.created_at), MAX(COALESCE(t.status_set_at, t.created_at)) FROM tracks t WHERE `+folderFrag).Scan(&created, &set) //nolint:gosec // G202: fixed fragment
	if err != nil {
		return 0, err
	}
	var best int64
	for _, s := range []sql.NullString{created, set} {
		if !s.Valid {
			continue
		}
		if t, ok := parseStamp(s.String); ok && t.UnixMilli() > best {
			best = t.UnixMilli()
		}
	}
	return best, nil
}

// lineageSongs lists every member of a lineage in recorded order (no folder rule).
func (c *catalog) lineageSongs(ctx context.Context, root string, folderFrag string) ([]songRow, error) {
	where := "t.root_track_id = ?"
	if folderFrag != "" {
		where += " AND " + folderFrag
	}
	return c.songs(ctx, songQuery{where: where, args: []any{root}, order: "t.recorded_at, t.id"})
}

// ftsExpr turns free text into a safe FTS5 expression (M1's token rules: Unicode letter/digit runs, at most 8, each
// a quoted prefix term, implicitly AND-ed). It returns the tokens too.
func ftsExpr(q string) (string, []string) {
	toks := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(toks) > 8 {
		toks = toks[:8]
	}
	quoted := make([]string, len(toks))
	for i, t := range toks {
		quoted[i] = `"` + t + `"*`
	}
	return strings.Join(quoted, " "), toks
}

// searchSongs pages the songs matching an FTS expression (empty expression = everything by created_at, id).
func (c *catalog) searchSongs(ctx context.Context, folderFrag, fts string, limit, offset int) ([]songRow, error) {
	return c.songs(ctx, songQuery{where: folderFrag, fts: fts, limit: limit, offset: offset})
}

// searchAlbums pages the albums that contain a matching song or whose root title matches (empty = everything by
// root created_at, root id).
func (c *catalog) searchAlbums(ctx context.Context, folderFrag, fts string, limit, offset int) ([]albumRow, error) {
	q := albumQuery{folderWhere: folderFrag, order: "a.created, a.id", limit: limit, offset: offset}
	if fts != "" {
		q.where = `(a.id IN (SELECT m.root_track_id FROM tracks m WHERE m.id IN (SELECT track_id FROM tracks_fts WHERE tracks_fts MATCH ?))
 OR a.id IN (SELECT track_id FROM tracks_fts WHERE tracks_fts MATCH ?))`
		q.args = []any{fts, "{title}: (" + fts + ")"}
	}
	return c.albums(ctx, q)
}

// parseStamp parses either timestamp form of the database (20-character seconds or millisecond TimeLayout).
func parseStamp(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// iso renders a stored timestamp as ISO 8601 with milliseconds (as the schema's date-time wants).
func iso(s string) string {
	if t, ok := parseStamp(s); ok {
		return t.Format("2006-01-02T15:04:05.000Z")
	}
	return s
}

func yearOf(recorded string) int {
	if t, ok := parseStamp(recorded); ok {
		return t.Year()
	}
	return 0
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// mediaSHAs returns the master's sha256 and the mp3_320 rendition's sha256 (invalid when there is none) of a track.
func (c *catalog) mediaSHAs(ctx context.Context, id string) (master string, mp3 sql.NullString, err error) {
	err = c.db.QueryRowContext(ctx, `SELECT t.master_sha256, (SELECT n.sha256 FROM renditions n WHERE n.track_id = t.id AND n.kind = 'mp3_320')
 FROM tracks t WHERE t.id = ?`, id).Scan(&master, &mp3)
	return master, mp3, err
}

// lyricsOf returns the lyrics text of a track's recipe ("" when it has none).
func (c *catalog) lyricsOf(ctx context.Context, trackID string) (string, error) {
	var l string
	err := c.db.QueryRowContext(ctx, `SELECT r.lyrics FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, trackID).Scan(&l)
	return l, err
}

// playlistRow is one playlist as the Subsonic catalog sees it.
type playlistRow struct {
	ID, Name, Desc, Created, Changed string
	Revision                         int
	Items                            []string
	Role                             sql.NullString
	Hidden                           bool
	DurationS                        float64
}

const playlistSelect = `SELECT p.id, p.name, p.description, p.created_at, p.updated_at, p.revision, p.items_json, p.role, p.hidden_at IS NOT NULL,
 COALESCE((SELECT SUM(t.duration_s) FROM json_each(p.items_json) j JOIN tracks t ON t.id = j.value), 0) FROM playlists p`

func scanPlaylist(rows interface{ Scan(...any) error }) (playlistRow, error) {
	var p playlistRow
	var items string
	if err := rows.Scan(&p.ID, &p.Name, &p.Desc, &p.Created, &p.Changed, &p.Revision, &items, &p.Role, &p.Hidden, &p.DurationS); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(items), &p.Items); err != nil {
		return p, fmt.Errorf("subsonic: stored playlist items: %w", err)
	}
	return p, nil
}

// playlists lists the non-hidden playlists plus the offline set (which is listed even when a direct edit hid it).
func (c *catalog) playlists(ctx context.Context) ([]playlistRow, error) {
	rows, err := c.db.QueryContext(ctx, playlistSelect+` WHERE p.hidden_at IS NULL OR p.role IS NOT NULL ORDER BY p.updated_at DESC, p.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("subsonic: playlists: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []playlistRow
	for rows.Next() {
		p, err := scanPlaylist(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// playlist returns one playlist by id; a hidden playlist other than the offline set does not exist for the bridge.
func (c *catalog) playlist(ctx context.Context, id string) (playlistRow, bool, error) {
	p, err := scanPlaylist(c.db.QueryRowContext(ctx, playlistSelect+` WHERE p.id = ? AND (p.hidden_at IS NULL OR p.role IS NOT NULL)`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return playlistRow{}, false, nil
	}
	return p, err == nil, err
}
