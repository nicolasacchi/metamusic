package subsonic

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// folderFrag resolves musicFolderId (D-M4a-10): absent = folders 1 + 2; 1, 2 or 3 = that folder; anything else is
// error 70.
func (c *call) folderFrag() (string, error) {
	n, err := c.folderNum()
	if err != nil {
		return "", err
	}
	frag, err := folderWhere(n)
	if err != nil {
		return "", fail(errNotFound, "music folder not found")
	}
	return frag, nil
}

// folderNum parses musicFolderId (0 when absent); a value that is not a number is error 70.
func (c *call) folderNum() (int, error) {
	n := 0
	if v := c.str("musicFolderId"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil {
			return 0, fail(errNotFound, "music folder not found")
		}
	}
	return n, nil
}

// intParam reads a non-negative integer parameter, falling back to def when absent or malformed, capped at ceiling.
func (c *call) intParam(name string, def, ceiling int) int {
	n, err := strconv.Atoi(c.str(name))
	if err != nil || n < 0 {
		return def
	}
	return min(n, ceiling)
}

// badID answers a malformed id parameter: error 10 when it is absent, error 70 when it cannot name anything.
func (c *call) badID() error {
	if c.str("id") == "" {
		return fail(errMissingParam, "required parameter is missing: id")
	}
	return fail(errNotFound, "item not found")
}

// maxCount caps every count and size parameter (C109).
const maxCount = 500

// cat returns the catalog or the "service unavailable" error when the handler has no store.
func (c *call) cat() (*catalog, error) {
	if c.s.cat == nil {
		return nil, fail(errGeneric, "service unavailable")
	}
	return c.s.cat, nil
}

// mp3Default reports whether the calling client's default format is MP3 (D-M4a-8).
func (c *call) mp3Default() (bool, error) {
	cl, err := c.client()
	if err != nil {
		return false, err
	}
	return cl.DefaultFormat == "mp3", nil
}

func albumRef() []ArtistRef { return []ArtistRef{{ID: artistID, Name: artistName}} }

// toChild maps a track to the Child of C109.
func toChild(s songRow, mp3 bool) Child {
	ch := Child{
		ID: s.ID, Parent: albumID(s.Root), Title: s.Title, Album: s.AlbumTitle, Artist: artistName, Track: s.Pos,
		Year: yearOf(s.RecordedAt), Genre: s.genre(), CoverArt: coverTrackID(s.ID), Duration: int(math.Round(s.DurationS)),
		SamplingRate: s.SampleRate, ChannelCount: s.Channels, PlayCount: s.Plays, Created: iso(s.CreatedAt),
		AlbumID: albumID(s.Root), ArtistID: artistID, Type: "music", MediaType: "song",
		SortName: sortName(s.Title), DisplayArtist: artistName, DisplayAlbumArtist: artistName,
		Artists: albumRef(), AlbumArtists: albumRef(), ISRC: []string{}, Moods: []string{}, Contributors: []Contributor{},
		ReplayGain: &ReplayGain{},
	}
	if g := ch.Genre; g != "" {
		ch.Genres = []ItemGenre{{Name: g}}
	}
	if s.LastPlay.Valid {
		ch.Played = iso(s.LastPlay.String)
	}
	if s.Status == "final" {
		ch.Starred = iso(s.StatusSetAt)
	}
	if s.Stars.Valid {
		ch.UserRating = int(s.Stars.Int64)
	}
	if s.BPM.Valid {
		ch.BPM = int(s.BPM.Int64)
	}
	if r := []rune(s.Style); len(r) > 200 {
		ch.Comment = string(r[:200])
	} else {
		ch.Comment = s.Style
	}
	ext := "flac"
	switch {
	case s.Lossy: // D-M4a-25: the master is an MP3, never transcoded
		ext = "mp3"
		ch.Size, ch.ContentType, ch.Suffix = s.MasterBytes, "audio/mpeg", "mp3"
		ch.BitRate = kbps(s.MasterBytes, s.DurationS)
	default:
		ch.Size, ch.ContentType, ch.Suffix, ch.BitDepth = s.MasterBytes, "audio/flac", "flac", s.BitsPerSample
		ch.BitRate = kbps(s.MasterBytes, s.DurationS)
		if mp3 {
			if s.MP3Bytes.Valid {
				ch.Size = s.MP3Bytes.Int64
			}
			ch.BitRate, ch.TranscodedContentType, ch.TranscodedSuffix = 320, "audio/mpeg", "mp3"
		}
	}
	ch.Path = s.AlbumSlug + "/" + s.Slug + "__" + strconv.FormatInt(s.Seed, 10) + "." + ext
	if s.LUFS.Valid {
		g := round2(-18 - s.LUFS.Float64)
		ch.ReplayGain.TrackGain = &g
	}
	if s.TruePeak.Valid {
		p := round2(math.Pow(10, s.TruePeak.Float64/20))
		ch.ReplayGain.TrackPeak = &p
	}
	return ch
}

func kbps(bytes int64, sec float64) int {
	if sec <= 0 {
		return 0
	}
	return int(math.Round(float64(bytes) * 8 / sec / 1000))
}

func sortName(title string) string { return strings.ToLower(textfold.Fold(title)) }

func toChildren(rows []songRow, mp3 bool) []Child {
	out := make([]Child, 0, len(rows))
	for _, r := range rows {
		out = append(out, toChild(r, mp3))
	}
	return out
}

// toAlbum maps a lineage aggregate to AlbumID3.
func toAlbum(a albumRow) AlbumID3 {
	al := AlbumID3{
		ID: a.id(), Name: a.Name, Artist: artistName, ArtistID: artistID, CoverArt: a.id(),
		SongCount: a.SongCount, Duration: int(math.Round(a.DurationS)), PlayCount: a.Plays, Created: iso(a.Created),
		Year: yearOf(a.RootRecorded), Genre: a.genre(), SortName: sortName(a.Name), DisplayArtist: artistName,
		Artists: albumRef(), ReleaseTypes: []string{}, Moods: []string{}, DiscTitles: []DiscTitle{},
	}
	if g := al.Genre; g != "" {
		al.Genres = []ItemGenre{{Name: g}}
	}
	if a.LastPlay.Valid {
		al.Played = iso(a.LastPlay.String)
	}
	if a.Starred.Valid {
		al.Starred = iso(a.Starred.String)
	}
	if a.Stars.Valid {
		al.UserRating = int(a.Stars.Int64)
	}
	return al
}

func toAlbums(rows []albumRow) []AlbumID3 {
	out := make([]AlbumID3, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAlbum(r))
	}
	return out
}

// albumDir maps a lineage to a directory-style Child (getAlbumList, getStarred, search2).
func albumDir(a albumRow) Child {
	al := toAlbum(a)
	return Child{
		ID: al.ID, Parent: artistID, IsDir: true, Title: al.Name, Album: al.Name, Artist: artistName, Year: al.Year, Genre: al.Genre,
		CoverArt: al.CoverArt, PlayCount: al.PlayCount, Played: al.Played, Created: al.Created, Starred: al.Starred,
		ArtistID: artistID, UserRating: al.UserRating, SortName: al.SortName, MusicBrainzID: "", DisplayArtist: artistName,
		ISRC: []string{}, Moods: []string{}, Contributors: []Contributor{}, ExplicitStatus: "",
	}
}

func albumDirs(rows []albumRow) []Child {
	out := make([]Child, 0, len(rows))
	for _, r := range rows {
		out = append(out, albumDir(r))
	}
	return out
}

// indexLetter is the folded first letter of name ('#' for anything that is not a letter A-Z after folding).
func indexLetter(name string) string {
	r, _ := utf8.DecodeRuneInString(textfold.Fold(name))
	if r < unicode.MaxASCII && unicode.IsLetter(r) {
		return strings.ToUpper(string(r))
	}
	return "#"
}

// albumFor loads one lineage by id for the caller's folder rule. A lineage that exists but has no member in the rule
// still answers (a discarded take stays reachable by id) with all of its members.
func (c *call) albumFor(ctx context.Context, cat *catalog, rawRoot, frag string, explicitFolder bool) (albumRow, []songRow, error) {
	songs, err := cat.lineageSongs(ctx, rawRoot, frag)
	if err != nil {
		return albumRow{}, nil, err
	}
	if len(songs) == 0 && !explicitFolder {
		ok, err := cat.albumExists(ctx, rawRoot)
		if err != nil {
			return albumRow{}, nil, err
		}
		if !ok {
			return albumRow{}, nil, fail(errNotFound, "album not found")
		}
		frag = "1 = 1"
		if songs, err = cat.lineageSongs(ctx, rawRoot, ""); err != nil {
			return albumRow{}, nil, err
		}
	} else if len(songs) == 0 {
		return albumRow{}, nil, fail(errNotFound, "album not found")
	}
	albums, err := cat.albums(ctx, albumQuery{folderWhere: frag, where: "a.id = ?", args: []any{rawRoot}})
	if err != nil {
		return albumRow{}, nil, err
	}
	if len(albums) == 0 {
		return albumRow{}, nil, fail(errNotFound, "album not found")
	}
	return albums[0], songs, nil
}

func (s *server) registerBrowse() {
	s.add("getIndexes", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		frag, err := c.folderFrag()
		if err != nil {
			return err
		}
		last, err := cat.lastModified(c.ctx, frag)
		if err != nil {
			return err
		}
		stamp, err := c.recordStamp(cat)
		if err != nil {
			return err
		}
		last = max(last, stamp)
		ix := &Indexes{LastModified: last, IgnoredArticles: ""}
		c.resp.Indexes = ix
		if v := c.str("ifModifiedSince"); v != "" {
			if since, perr := strconv.ParseInt(v, 10, 64); perr == nil && since >= last {
				return nil
			}
		}
		albums, err := c.allAlbums(cat, frag)
		if err != nil {
			return err
		}
		groups := map[string][]Artist{}
		var names []string
		for _, a := range albums {
			l := indexLetter(a.Name)
			if _, seen := groups[l]; !seen {
				names = append(names, l)
			}
			groups[l] = append(groups[l], Artist{ID: a.id(), Name: a.Name})
		}
		slices.Sort(names)
		for _, n := range names {
			ix.Index = append(ix.Index, Index{Name: n, Artist: groups[n]})
		}
		return nil
	})

	s.add("getMusicDirectory", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		frag, err := c.folderFrag()
		if err != nil {
			return err
		}
		kind, raw := parseID(c.str("id"))
		switch kind {
		case kindBad:
			return c.badID()
		case kindArtist:
			albums, err := c.allAlbums(cat, frag)
			if err != nil {
				return err
			}
			c.resp.Directory = &Directory{ID: artistID, Name: artistName, Child: albumDirs(albums)}
			return nil
		case kindRecord:
			mp3, err := c.mp3Default()
			if err != nil {
				return err
			}
			ra, err := c.recordAlbum(cat, raw)
			if err != nil {
				return err
			}
			c.resp.Directory = &Directory{ID: ra.row.id(), Name: ra.row.Name, Parent: artistID, Child: ra.children(mp3)}
			return nil
		case kindAlbum:
			mp3, err := c.mp3Default()
			if err != nil {
				return err
			}
			a, songs, err := c.albumFor(c.ctx, cat, raw, frag, c.str("musicFolderId") != "")
			if err != nil {
				return err
			}
			c.resp.Directory = &Directory{ID: albumID(a.ID), Name: a.Name, Parent: artistID, Child: toChildren(songs, mp3)}
			return nil
		}
		return fail(errNotFound, "directory not found")
	})

	s.add("getArtists", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		frag, err := c.folderFrag()
		if err != nil {
			return err
		}
		ar, err := c.artistFor(cat, frag)
		if err != nil {
			return err
		}
		c.resp.Artists = &ArtistsID3{IgnoredArticles: "", Index: []IndexID3{{Name: "M", Artist: []ArtistID3{ar.ArtistID3}}}}
		return nil
	})

	s.add("getArtist", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		frag, err := c.folderFrag()
		if err != nil {
			return err
		}
		if kind, _ := parseID(c.str("id")); kind == kindBad {
			return c.badID()
		} else if kind != kindArtist {
			return fail(errNotFound, "artist not found")
		}
		ar, err := c.artistFor(cat, frag)
		if err != nil {
			return err
		}
		c.resp.Artist = ar
		return nil
	})

	s.add("getAlbum", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		kind, raw := parseID(c.str("id"))
		switch kind {
		case kindBad:
			return c.badID()
		case kindAlbum, kindRecord:
		default:
			return fail(errNotFound, "album not found")
		}
		frag, err := c.folderFrag()
		if err != nil {
			return err
		}
		mp3, err := c.mp3Default()
		if err != nil {
			return err
		}
		if kind == kindRecord {
			ra, err := c.recordAlbum(cat, raw)
			if err != nil {
				return err
			}
			c.resp.Album = &AlbumWithSong{AlbumID3: toAlbum(ra.row), Song: ra.children(mp3)}
			return nil
		}
		a, songs, err := c.albumFor(c.ctx, cat, raw, frag, c.str("musicFolderId") != "")
		if err != nil {
			return err
		}
		c.resp.Album = &AlbumWithSong{AlbumID3: toAlbum(a), Song: toChildren(songs, mp3)}
		return nil
	})

	s.add("getSong", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		kind, raw := parseID(c.str("id"))
		switch kind {
		case kindBad:
			return c.badID()
		case kindSong:
		default:
			return fail(errNotFound, "song not found")
		}
		mp3, err := c.mp3Default()
		if err != nil {
			return err
		}
		row, ok, err := cat.song(c.ctx, raw)
		if err != nil {
			return err
		}
		if !ok {
			return fail(errNotFound, "song not found")
		}
		ch := toChild(row, mp3)
		c.resp.Song = &ch
		return nil
	})

	s.add("getGenres", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		frag, _ := folderWhere(0)
		rows, err := cat.genres(c.ctx, frag)
		if err != nil {
			return err
		}
		g := &Genres{Genre: make([]Genre, 0, len(rows))}
		for _, r := range rows {
			g.Genre = append(g.Genre, Genre{Value: r.Term, SongCount: r.SongCount, AlbumCount: r.AlbumCnt})
		}
		c.resp.Genres = g
		return nil
	})
}

// artistFor builds the one artist with its albums in the folder rule.
func (c *call) artistFor(cat *catalog, frag string) (*ArtistWithAlb, error) {
	albums, err := c.allAlbums(cat, frag)
	if err != nil {
		return nil, err
	}
	ar := &ArtistWithAlb{ArtistID3: ArtistID3{ID: artistID, Name: artistName, AlbumCount: len(albums), SortName: artistName}, Album: toAlbums(albums)}
	if len(albums) > 0 {
		newest := albums[0]
		for _, a := range albums[1:] {
			if a.Created > newest.Created {
				newest = a
			}
		}
		ar.CoverArt = newest.id()
	}
	return ar, nil
}
