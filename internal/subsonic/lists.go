package subsonic

import (
	"strconv"
	"strings"
)

const genreExists = `EXISTS (SELECT 1 FROM track_style_terms g WHERE g.track_id = t.id AND g.term = ?)`

// genreParam normalises the genre parameter like style terms (lower case, single spaces).
func (c *call) genreParam() string {
	return strings.ToLower(strings.Join(strings.Fields(c.str("genre")), " "))
}

// shuffle permutes ids with Fisher-Yates over the injected random source (deterministic in tests).
func (s *server) shuffle(ids []string) {
	for i := len(ids) - 1; i > 0; i-- {
		j := int(s.rand() % uint64(i+1)) //nolint:gosec // G115: the remainder is below i+1
		ids[i], ids[j] = ids[j], ids[i]
	}
}

// albumList answers the album list types of C109; unknown types are error 10.
func (c *call) albumList(cat *catalog) ([]albumRow, error) {
	frag, err := c.folderFrag()
	if err != nil {
		return nil, err
	}
	size := c.intParam("size", 10, maxCount)
	offset := c.intParam("offset", 0, 1<<30)
	q := albumQuery{folderWhere: frag, limit: max(size, 1), offset: offset}
	switch typ := c.str("type"); typ {
	case "":
		return nil, fail(errMissingParam, "required parameter is missing: type")
	case "random":
		ids, err := cat.albumIDs(c.ctx, frag)
		if err != nil {
			return nil, err
		}
		c.s.shuffle(ids)
		if len(ids) > size {
			ids = ids[:size]
		}
		return cat.albumsByIDs(c.ctx, frag, ids)
	case "newest":
		q.order = "a.created DESC, a.id"
	case "highest":
		q.order = "a.stars IS NULL, a.stars DESC, lower(a.name), a.id"
	case "frequent":
		q.order = "a.plays DESC, lower(a.name), a.id"
	case "recent":
		q.where, q.order = "a.last_play IS NOT NULL", "a.last_play DESC, a.id"
	case "alphabeticalByName", "alphabeticalByArtist":
	case "starred":
		q.where, q.order = "a.starred IS NOT NULL", "a.starred DESC, a.id"
	case "byYear":
		if !c.params.Has("fromYear") || !c.params.Has("toYear") {
			return nil, fail(errMissingParam, "required parameter is missing: fromYear and toYear")
		}
		from, to := c.intParam("fromYear", 0, 9999), c.intParam("toYear", 9999, 9999)
		dir := "ASC"
		if from > to {
			from, to, dir = to, from, "DESC"
		}
		q.where, q.args = "a.year BETWEEN ? AND ?", []any{from, to}
		q.order = "a.year " + dir + ", lower(a.name), a.id"
	case "byGenre":
		g := c.genreParam()
		if g == "" {
			return nil, fail(errMissingParam, "required parameter is missing: genre")
		}
		q.where = `EXISTS (SELECT 1 FROM tracks t JOIN track_style_terms g ON g.track_id = t.id WHERE t.root_track_id = a.id AND g.term = ?)`
		q.args = []any{g}
	default:
		return nil, fail(errMissingParam, "unsupported album list type")
	}
	if size == 0 {
		return []albumRow{}, nil
	}
	return cat.albums(c.ctx, q)
}

// similar lists the lineage relatives of a song, or the members of an album, in recorded order (D-M4a-11).
func (c *call) similar(cat *catalog) ([]songRow, error) {
	kind, raw := parseID(c.str("id"))
	var root, exclude string
	switch kind {
	case kindBad:
		return nil, c.badID()
	case kindSong:
		row, ok, err := cat.song(c.ctx, raw)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fail(errNotFound, "song not found")
		}
		root, exclude = row.Root, row.ID
	case kindAlbum:
		ok, err := cat.albumExists(c.ctx, raw)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fail(errNotFound, "album not found")
		}
		root = raw
	default:
		return nil, fail(errNotFound, "song not found")
	}
	frag, _ := folderWhere(0)
	rows, err := cat.lineageSongs(c.ctx, root, frag)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, r := range rows {
		if r.ID != exclude {
			out = append(out, r)
		}
	}
	if n := c.intParam("count", 50, maxCount); len(out) > n {
		out = out[:n]
	}
	return out, nil
}

func (s *server) registerLists() {
	s.add("getAlbumList", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		rows, err := c.albumList(cat)
		if err != nil {
			return err
		}
		c.resp.AlbumList = &AlbumList{Album: albumDirs(rows)}
		return nil
	})
	s.add("getAlbumList2", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		rows, err := c.albumList(cat)
		if err != nil {
			return err
		}
		c.resp.AlbumList2 = &AlbumList2{Album: toAlbums(rows)}
		return nil
	})

	s.add("getRandomSongs", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		frag, err := c.folderFrag()
		if err != nil {
			return err
		}
		mp3, err := c.mp3Default()
		if err != nil {
			return err
		}
		where, args := frag, []any(nil)
		if g := c.genreParam(); g != "" {
			where, args = where+" AND "+genreExists, append(args, g)
		}
		if v := c.str("fromYear"); v != "" {
			if n, perr := strconv.Atoi(v); perr == nil {
				where, args = where+" AND CAST(substr(t.recorded_at, 1, 4) AS INTEGER) >= ?", append(args, n)
			}
		}
		if v := c.str("toYear"); v != "" {
			if n, perr := strconv.Atoi(v); perr == nil {
				where, args = where+" AND CAST(substr(t.recorded_at, 1, 4) AS INTEGER) <= ?", append(args, n)
			}
		}
		ids, err := cat.songIDs(c.ctx, where, args)
		if err != nil {
			return err
		}
		c.s.shuffle(ids)
		if n := c.intParam("size", 10, maxCount); len(ids) > n {
			ids = ids[:n]
		}
		rows, err := cat.songsByIDs(c.ctx, ids)
		if err != nil {
			return err
		}
		c.resp.RandomSongs = &SongList{Song: toChildren(rows, mp3)}
		return nil
	})

	starred := func(c *call) ([]songRow, []albumRow, bool, error) {
		cat, err := c.cat()
		if err != nil {
			return nil, nil, false, err
		}
		frag, err := c.folderFrag()
		if err != nil {
			return nil, nil, false, err
		}
		mp3, err := c.mp3Default()
		if err != nil {
			return nil, nil, false, err
		}
		songs, err := cat.songs(c.ctx, songQuery{where: frag + " AND t.status = 'final'", order: "t.status_set_at DESC, t.id"})
		if err != nil {
			return nil, nil, false, err
		}
		albums, err := cat.albums(c.ctx, albumQuery{folderWhere: frag, where: "a.starred IS NOT NULL", order: "a.starred DESC, a.id"})
		return songs, albums, mp3, err
	}
	s.add("getStarred", false, func(c *call) error {
		songs, albums, mp3, err := starred(c)
		if err != nil {
			return err
		}
		c.resp.Starred = &Starred{Song: toChildren(songs, mp3), Album: albumDirs(albums)}
		return nil
	})
	s.add("getStarred2", false, func(c *call) error {
		songs, albums, mp3, err := starred(c)
		if err != nil {
			return err
		}
		c.resp.Starred2 = &Starred2{Song: toChildren(songs, mp3), Album: toAlbums(albums)}
		return nil
	})

	s.add("getSongsByGenre", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		g := c.genreParam()
		if g == "" {
			return fail(errMissingParam, "required parameter is missing: genre")
		}
		frag, err := c.folderFrag()
		if err != nil {
			return err
		}
		mp3, err := c.mp3Default()
		if err != nil {
			return err
		}
		rows, err := cat.songs(c.ctx, songQuery{where: frag + " AND " + genreExists, args: []any{g},
			limit: max(c.intParam("count", 10, maxCount), 1), offset: c.intParam("offset", 0, 1<<30)})
		if err != nil {
			return err
		}
		c.resp.SongsByGenre = &SongList{Song: toChildren(rows, mp3)}
		return nil
	})

	s.add("getSimilarSongs", false, func(c *call) error {
		list, err := c.similarList()
		if err != nil {
			return err
		}
		c.resp.SimilarSongs = list
		return nil
	})
	s.add("getSimilarSongs2", false, func(c *call) error {
		list, err := c.similarList()
		if err != nil {
			return err
		}
		c.resp.SimilarSongs2 = list
		return nil
	})
}

func (c *call) similarList() (*SongList, error) {
	cat, err := c.cat()
	if err != nil {
		return nil, err
	}
	mp3, err := c.mp3Default()
	if err != nil {
		return nil, err
	}
	rows, err := c.similar(cat)
	if err != nil {
		return nil, err
	}
	return &SongList{Song: toChildren(rows, mp3)}, nil
}
