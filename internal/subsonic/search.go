package subsonic

import (
	"strings"

	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// searchPlan is the parsed query of search2/search3.
type searchPlan struct {
	fts    string // "" => everything
	none   bool   // a non-empty query without any letter or digit matches nothing
	artist bool   // the artist matches
}

// planSearch applies C109: an empty query, the two-character query `""` or no query at all means everything; any
// other text goes through M1's FTS token rules (operators never reach FTS5).
func planSearch(q string) searchPlan {
	q = strings.TrimSpace(q)
	if q == "" || q == `""` {
		return searchPlan{artist: true}
	}
	expr, toks := ftsExpr(q)
	if expr == "" {
		return searchPlan{none: true}
	}
	p := searchPlan{fts: expr, artist: true}
	for _, t := range toks {
		if !strings.HasPrefix(artistName, strings.ToLower(textfold.Fold(t))) {
			p.artist = false
		}
	}
	return p
}

type searchHits struct {
	artist []ArtistID3
	albums []albumRow
	songs  []songRow
	mp3    bool
}

func (c *call) search() (searchHits, error) {
	var h searchHits
	cat, err := c.cat()
	if err != nil {
		return h, err
	}
	frag, err := c.folderFrag()
	if err != nil {
		return h, err
	}
	if h.mp3, err = c.mp3Default(); err != nil {
		return h, err
	}
	p := planSearch(c.str("query"))
	if p.none {
		return h, nil
	}
	if n := c.intParam("artistCount", 20, maxCount); p.artist && n > 0 && c.intParam("artistOffset", 0, 1<<30) == 0 {
		ar, err := c.artistFor(cat, frag)
		if err != nil {
			return h, err
		}
		h.artist = []ArtistID3{ar.ArtistID3}
	}
	if n := c.intParam("albumCount", 20, maxCount); n > 0 {
		if h.albums, err = cat.searchAlbums(c.ctx, frag, p.fts, n, c.intParam("albumOffset", 0, 1<<30)); err != nil {
			return h, err
		}
	}
	if n := c.intParam("songCount", 20, maxCount); n > 0 {
		if h.songs, err = cat.searchSongs(c.ctx, frag, p.fts, n, c.intParam("songOffset", 0, 1<<30)); err != nil {
			return h, err
		}
	}
	return h, nil
}

func (s *server) registerSearch() {
	s.add("search3", false, func(c *call) error {
		h, err := c.search()
		if err != nil {
			return err
		}
		r := &SearchResult3{Artist: h.artist}
		if len(h.albums) > 0 {
			r.Album = toAlbums(h.albums)
		}
		if len(h.songs) > 0 {
			r.Song = toChildren(h.songs, h.mp3)
		}
		c.resp.SearchResult3 = r
		return nil
	})
	s.add("search2", false, func(c *call) error {
		h, err := c.search()
		if err != nil {
			return err
		}
		r := &SearchResult2{}
		for _, a := range h.artist {
			r.Artist = append(r.Artist, Artist{ID: a.ID, Name: a.Name})
		}
		if len(h.albums) > 0 {
			r.Album = albumDirs(h.albums)
		}
		if len(h.songs) > 0 {
			r.Song = toChildren(h.songs, h.mp3)
		}
		c.resp.SearchResult2 = r
		return nil
	})
}
