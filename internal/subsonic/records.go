package subsonic

import (
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/tape"
	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// This file serves the Record albums of L2 (C126.7, D-L2-35): published, non-hidden Records with at least one listable
// slot, as `rec-<record id>` albums next to the lineage albums of M4a. The lineage ids, every song's albumId/parent outside a
// Record album and getSong are unchanged. Membership comes from albums.Service; the aggregates (plays, stars, durations, the
// folder rule) are computed here from the same song rows M4a uses (D-L2-34).

// albumKinds is the album-kind dispatch of the catalog: the kinds the server serves besides the lineage. Lane S2 adds the
// muse and unfiled kinds here (and in the loaders below) without touching the endpoint code.
func albumKinds() []albums.Kind { return []albums.Kind{albums.KindRecord} }

// recSong is one song of a Record album as served: the track row with the album's position, disc and plan gain.
type recSong struct {
	row       songRow
	pos, disc int
	gainCDB   int // trackGain in centi-dB (gain_db + albumGain); valid when hasGain
	hasGain   bool
}

// recAlbum is one Record album over the Takes the folder rule keeps.
type recAlbum struct {
	row      albumRow
	songs    []recSong
	albumCDB int // albumGain in centi-dB: -18.0 - T
	cover    cover.Input
	terms    map[string]bool // the style terms of the kept songs (byGenre)
}

// recItem is one album of the catalog with its listed songs, before the folder rule.
type recItem struct {
	album albums.Album
	songs []albums.Song
}

// statuses is the folder rule as a set of track statuses.
func (c *call) statuses() (map[string]bool, error) {
	n, err := c.folderNum()
	if err != nil {
		return nil, err
	}
	set, err := folderStatuses(n)
	if err != nil {
		return nil, fail(errNotFound, "music folder not found")
	}
	return set, nil
}

// recordAlbums returns every Record album that keeps at least one song under the folder rule (nil without the catalog).
func (c *call) recordAlbums(cat *catalog) ([]*recAlbum, error) {
	if c.s.alb == nil {
		return nil, nil
	}
	set, err := c.statuses()
	if err != nil {
		return nil, err
	}
	list, err := c.s.alb.List(c.ctx, albumKinds()...)
	if err != nil {
		return nil, err
	}
	items := make([]recItem, 0, len(list))
	for _, al := range list {
		a, songs, err := c.s.alb.Get(c.ctx, al.ID)
		if errors.Is(err, albums.ErrNotFound) {
			continue // unpublished between the two reads
		}
		if err != nil {
			return nil, err
		}
		items = append(items, recItem{a, songs})
	}
	return c.buildRecs(cat, items, set)
}

// recordAlbum loads one Record album; an unknown, unpublished, hidden or (under the folder rule) empty one is error 70.
func (c *call) recordAlbum(cat *catalog, raw string) (*recAlbum, error) {
	if c.s.alb == nil {
		return nil, fail(errNotFound, "album not found")
	}
	set, err := c.statuses()
	if err != nil {
		return nil, err
	}
	a, songs, err := c.s.alb.Get(c.ctx, prefixRecord+raw)
	if errors.Is(err, albums.ErrNotFound) {
		return nil, fail(errNotFound, "album not found")
	}
	if err != nil {
		return nil, err
	}
	out, err := c.buildRecs(cat, []recItem{{a, songs}}, set)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fail(errNotFound, "album not found")
	}
	return out[0], nil
}

// buildRecs turns the listed songs into albums: one read of the song rows for every Take, the plan gains over the listed
// Takes, then the folder rule drops what it excludes (a gain does not depend on the folder).
func (c *call) buildRecs(cat *catalog, items []recItem, set map[string]bool) ([]*recAlbum, error) {
	var ids []string
	seen := map[string]bool{}
	for _, it := range items {
		for _, s := range it.songs {
			if !seen[s.TakeID] {
				seen[s.TakeID] = true
				ids = append(ids, s.TakeID)
			}
		}
	}
	rows, err := cat.songsByIDs(c.ctx, ids)
	if err != nil {
		return nil, err
	}
	by := make(map[string]songRow, len(rows))
	for _, r := range rows {
		by[r.ID] = r
	}
	created, err := cat.recordCreated(c.ctx)
	if err != nil {
		return nil, err
	}
	var out []*recAlbum
	for _, it := range items {
		if ra := assembleRec(it, by, created[*it.album.RecordID], set); len(ra.songs) > 0 {
			out = append(out, ra)
		}
	}
	return out, nil
}

// assembleRec builds one album (C126.4): the aggregates mirror albumAgg over the kept songs, the gains are the plan gains
// (C128.2, auto target) over every listed Take, albumGain = -18.0 - T and trackGain = gain + albumGain, both in integer centi-dB.
func assembleRec(it recItem, by map[string]songRow, created string, set map[string]bool) *recAlbum {
	var ms []tape.Measure
	var listed []songRow
	for _, s := range it.songs {
		r := by[s.TakeID]
		listed = append(listed, r)
		ms = append(ms, tape.Measure{LUFS: r.LUFS.Float64, TruePeakDBTP: r.TruePeak.Float64, OK: r.LUFS.Valid && r.TruePeak.Valid})
	}
	target, plans := tape.PlanGains(ms, nil)
	ra := &recAlbum{albumCDB: -1800 - int(math.Round(target*100)), cover: it.album.CoverInput, terms: map[string]bool{}}
	a := albumRow{ExtID: it.album.ID, ID: *it.album.RecordID, Name: it.album.Name, Created: created, RootRecorded: created, Year: yearOf(created)}
	for i, s := range it.songs {
		r := listed[i]
		if r.ID == "" || !set[r.Status] {
			continue // a Take that vanished, or one the folder rule excludes
		}
		rs := recSong{row: r, pos: s.Position, disc: s.Disc}
		if plans[i].Status != tape.GainUnmeasured {
			rs.gainCDB, rs.hasGain = plans[i].GainCDB+ra.albumCDB, true
		}
		ra.songs = append(ra.songs, rs)
		a.SongCount++
		a.DurationS += r.DurationS
		a.Plays += r.Plays
		if r.LastPlay.Valid && (!a.LastPlay.Valid || r.LastPlay.String > a.LastPlay.String) {
			a.LastPlay.String, a.LastPlay.Valid = r.LastPlay.String, true
		}
		if r.Stars.Valid && (!a.Stars.Valid || r.Stars.Int64 > a.Stars.Int64) {
			a.Stars = r.Stars
		}
		if r.Status == "final" && (!a.Starred.Valid || r.StatusSetAt > a.Starred.String) {
			a.Starred.String, a.Starred.Valid = r.StatusSetAt, true
		}
		a.Modified = max(a.Modified, r.CreatedAt, r.StatusSetAt)
		if a.Style == "" {
			a.Style = r.Style
		}
		if r.Terms.Valid {
			for _, t := range strings.Split(r.Terms.String, "\x1f") {
				ra.terms[t] = true
			}
		}
	}
	ra.row = a
	return ra
}

// child is a song as served inside the album: albumId and parent are the Record album, track the position and discNumber the
// side; replayGain carries the album gain and the plan track gain (unmeasured Takes carry no track gain).
func (ra *recAlbum) child(rs recSong, mp3 bool) Child {
	ch := toChild(rs.row, mp3)
	ch.Parent, ch.AlbumID, ch.Album = ra.row.id(), ra.row.id(), ra.row.Name
	ch.Track, ch.DiscNumber = rs.pos, rs.disc
	ag := float64(ra.albumCDB) / 100
	ch.ReplayGain.AlbumGain = &ag
	ch.ReplayGain.TrackGain = nil
	if rs.hasGain {
		tg := float64(rs.gainCDB) / 100
		ch.ReplayGain.TrackGain = &tg
	}
	return ch
}

func (ra *recAlbum) children(mp3 bool) []Child {
	out := make([]Child, 0, len(ra.songs))
	for _, s := range ra.songs {
		out = append(out, ra.child(s, mp3))
	}
	return out
}

func recRows(recs []*recAlbum) []albumRow {
	out := make([]albumRow, len(recs))
	for i, r := range recs {
		out[i] = r.row
	}
	return out
}

// lowerASCII is SQLite's lower(): ASCII letters only, so a merged list orders like the SQL of the lineage albums.
func lowerASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

func cmpName(a, b albumRow) int {
	return cmp.Or(cmp.Compare(lowerASCII(a.Name), lowerASCII(b.Name)), cmp.Compare(a.id(), b.id()))
}

// nullsLast orders `x IS NULL` ascending: a row with a value (false) before one without (true).
func nullsLast(aNull, bNull bool) int {
	switch {
	case aNull == bNull:
		return 0
	case aNull:
		return 1
	}
	return -1
}

// listOrder is the ORDER BY of getAlbumList's type as a comparator over album rows, the twin of the SQL in albumList.
func listOrder(typ string, descYear bool) func(a, b albumRow) int {
	byID := func(a, b albumRow) int { return cmp.Compare(a.id(), b.id()) }
	switch typ {
	case "newest":
		return func(a, b albumRow) int { return cmp.Or(cmp.Compare(b.Created, a.Created), byID(a, b)) }
	case "highest":
		return func(a, b albumRow) int {
			return cmp.Or(nullsLast(!a.Stars.Valid, !b.Stars.Valid), cmp.Compare(b.Stars.Int64, a.Stars.Int64), cmpName(a, b))
		}
	case "frequent":
		return func(a, b albumRow) int { return cmp.Or(cmp.Compare(b.Plays, a.Plays), cmpName(a, b)) }
	case "recent":
		return func(a, b albumRow) int { return cmp.Or(cmp.Compare(b.LastPlay.String, a.LastPlay.String), byID(a, b)) }
	case "starred":
		return func(a, b albumRow) int { return cmp.Or(cmp.Compare(b.Starred.String, a.Starred.String), byID(a, b)) }
	case "byYear":
		return func(a, b albumRow) int {
			y := cmp.Compare(a.Year, b.Year)
			if descYear {
				y = -y
			}
			return cmp.Or(y, cmpName(a, b))
		}
	}
	return cmpName // alphabeticalByName, alphabeticalByArtist, byGenre
}

// recordFilter is the WHERE of getAlbumList's type over a Record album (the twin of q.where in albumList).
func (c *call) recordFilter(typ string, from, to int) func(*recAlbum) bool {
	switch typ {
	case "recent":
		return func(r *recAlbum) bool { return r.row.LastPlay.Valid }
	case "starred":
		return func(r *recAlbum) bool { return r.row.Starred.Valid }
	case "byYear":
		return func(r *recAlbum) bool { return r.row.Year >= from && r.row.Year <= to }
	case "byGenre":
		g := c.genreParam()
		return func(r *recAlbum) bool { return r.terms[g] }
	}
	return func(*recAlbum) bool { return true }
}

// mergeAlbums merges the lineage rows (already ordered and cut to offset+size by the SQL) with the Record rows under one
// comparator and cuts the page.
func mergeAlbums(lineage []albumRow, recs []albumRow, less func(a, b albumRow) int, offset, size int) []albumRow {
	all := append(slices.Clone(lineage), recs...)
	slices.SortStableFunc(all, less)
	if offset >= len(all) {
		return []albumRow{}
	}
	all = all[offset:]
	if size > 0 && len(all) > size {
		all = all[:size]
	}
	return all
}

// listWithRecords is albumList's merged path: the lineage albums as M4a selects them (cut to offset+size) and the Record albums
// that pass the same filter, ordered together.
func (c *call) listWithRecords(cat *catalog, recs []*recAlbum, typ string, q albumQuery, size, offset, from, to int, desc bool) ([]albumRow, error) {
	q.limit, q.offset = size+offset, 0
	lineage, err := cat.albums(c.ctx, q)
	if err != nil {
		return nil, err
	}
	keep := c.recordFilter(typ, from, to)
	var rows []albumRow
	for _, r := range recs {
		if keep(r) {
			rows = append(rows, r.row)
		}
	}
	return mergeAlbums(lineage, rows, listOrder(typ, desc), offset, size), nil
}

// randomWithRecords is type=random over the union of lineage and Record album ids: the seeded shuffle runs over the sorted
// ids (lineage `al-` ids first, then `rec-`), so a library without Records shuffles exactly as before.
func (c *call) randomWithRecords(cat *catalog, frag string, lineageIDs []string, recs []*recAlbum, size int) ([]albumRow, error) {
	recBy := make(map[string]albumRow, len(recs))
	all := make([]string, 0, len(lineageIDs)+len(recs))
	for _, id := range lineageIDs {
		all = append(all, albumID(id))
	}
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		recBy[r.row.id()] = r.row
		ids = append(ids, r.row.id())
	}
	slices.Sort(ids)
	all = append(all, ids...)
	c.s.shuffle(all)
	if len(all) > size {
		all = all[:size]
	}
	var raw []string
	for _, id := range all {
		if _, ok := recBy[id]; !ok {
			raw = append(raw, strings.TrimPrefix(id, prefixAlbum))
		}
	}
	lin, err := cat.albumsByIDs(c.ctx, frag, raw)
	if err != nil {
		return nil, err
	}
	linBy := make(map[string]albumRow, len(lin))
	for _, a := range lin {
		linBy[a.id()] = a
	}
	out := make([]albumRow, 0, len(all))
	for _, id := range all {
		if r, ok := recBy[id]; ok {
			out = append(out, r)
		} else if a, ok := linBy[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// allAlbums is the lineage albums plus the Record albums in the default order (lower(name), id): what getIndexes, getArtists,
// getArtist and the artist directory list.
func (c *call) allAlbums(cat *catalog, frag string) ([]albumRow, error) {
	rows, err := cat.albums(c.ctx, albumQuery{folderWhere: frag})
	if err != nil {
		return nil, err
	}
	recs, err := c.recordAlbums(cat)
	if err != nil || len(recs) == 0 {
		return rows, err
	}
	all := append(rows, recRows(recs)...)
	slices.SortStableFunc(all, cmpName)
	return all, nil
}

// searchAlbums pages the albums of search2/search3: the lineage albums as M4a finds them and the Record albums whose title
// contains the query (plain, case-insensitive, accent-folded substring; an empty query matches every Record album).
func (c *call) searchAlbums(cat *catalog, frag string, p searchPlan, n, off int) ([]albumRow, error) {
	recs, err := c.recordAlbums(cat)
	if err != nil {
		return nil, err
	}
	var hit []albumRow
	needle := strings.ToLower(textfold.Fold(strings.TrimSpace(c.str("query"))))
	if needle == `""` {
		needle = ""
	}
	for _, r := range recs {
		if strings.Contains(strings.ToLower(textfold.Fold(r.row.Name)), needle) {
			hit = append(hit, r.row)
		}
	}
	if len(hit) == 0 {
		return cat.searchAlbums(c.ctx, frag, p.fts, n, off)
	}
	lineage, err := cat.searchAlbums(c.ctx, frag, p.fts, n+off, 0)
	if err != nil {
		return nil, err
	}
	byCreated := func(a, b albumRow) int { return cmp.Or(cmp.Compare(a.Created, b.Created), cmp.Compare(a.id(), b.id())) }
	return mergeAlbums(lineage, hit, byCreated, off, n), nil
}

// recStamp turns the album token into a modification stamp: getIndexes' lastModified must move when the token does, and a
// token is a hash. The stamp is the moment this process saw the token change, never earlier than the newest Record update
// (which covers an unpublish and a restart); a library that never had a Record contributes nothing.
type recStamp struct {
	mu   sync.Mutex
	seen string
	at   int64
}

// recordStamp is the Record part of lastModified in Unix milliseconds (0 without Records).
func (c *call) recordStamp(cat *catalog) (int64, error) {
	if c.s.alb == nil {
		return 0, nil
	}
	upd, err := cat.recordsUpdated(c.ctx)
	if err != nil || upd == "" {
		return 0, err
	}
	tok, err := c.s.alb.Token(c.ctx)
	if err != nil {
		return 0, err
	}
	st := &c.s.stamp
	st.mu.Lock()
	defer st.mu.Unlock()
	if tok != st.seen {
		if st.seen != "" {
			st.at = c.s.now().UnixMilli()
		}
		st.seen = tok
	}
	best := st.at
	if t, ok := parseStamp(upd); ok {
		best = max(best, t.UnixMilli())
	}
	return best, nil
}

// recordCover answers getCoverArt id=rec-<id>: the album's mosaic through the cover service (an unknown, unpublished or hidden
// Record is error 70).
func (c *call) recordCover(raw string, size int) (serve.Object, error) {
	if c.s.alb == nil {
		return serve.Object{}, fail(errNotFound, "cover art not found")
	}
	a, _, err := c.s.alb.Get(c.ctx, prefixRecord+raw)
	if errors.Is(err, albums.ErrNotFound) {
		return serve.Object{}, fail(errNotFound, "cover art not found")
	}
	if err != nil {
		return serve.Object{}, err
	}
	m, ok := c.s.covers.(mosaicSource)
	if !ok {
		return serve.Object{}, fail(errGeneric, "service unavailable")
	}
	obj, _, err := m.EnsureMosaic(c.ctx, a.CoverInput, size)
	return obj, err
}

// mosaicSource is the part of *cover.Service that renders Record covers; a CoverSource without it answers "unavailable".
type mosaicSource interface {
	EnsureMosaic(ctx context.Context, in cover.Input, size int) (serve.Object, string, error)
}
