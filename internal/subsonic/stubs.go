package subsonic

// registerStubs adds the endpoints that answer a fixed shape: the library has no top songs, no now-playing list, no
// scan and no artist or album biographies (C109). An info stub still requires an existing id.
func (s *server) registerStubs() {
	s.add("getTopSongs", false, func(c *call) error {
		c.resp.TopSongs = &SongList{Song: []Child{}}
		return nil
	})
	s.add("getNowPlaying", false, func(c *call) error {
		c.resp.NowPlaying = &NowPlaying{Entry: []Child{}}
		return nil
	})
	s.add("getScanStatus", false, func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		frag, _ := folderWhere(0)
		n, err := cat.countSongs(c.ctx, frag)
		if err != nil {
			return err
		}
		c.resp.ScanStatus = &ScanStatus{Scanning: false, Count: n}
		return nil
	})
	artist := func(c *call) error {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		kind, raw := parseID(c.str("id"))
		switch kind {
		case kindBad:
			return c.badID()
		case kindArtist:
			return nil
		case kindAlbum:
			if ok, err := cat.albumExists(c.ctx, raw); err != nil {
				return err
			} else if ok {
				return nil
			}
		case kindSong:
			if _, ok, err := cat.song(c.ctx, raw); err != nil {
				return err
			} else if ok {
				return nil
			}
		}
		return fail(errNotFound, "item not found")
	}
	s.add("getArtistInfo", false, func(c *call) error {
		if err := artist(c); err != nil {
			return err
		}
		c.resp.ArtistInfo = &ArtistInfo{}
		return nil
	})
	s.add("getArtistInfo2", false, func(c *call) error {
		if err := artist(c); err != nil {
			return err
		}
		c.resp.ArtistInfo2 = &ArtistInfo{}
		return nil
	})
	albumInfo := func(c *call) error {
		if err := artist(c); err != nil {
			return err
		}
		c.resp.AlbumInfo = &AlbumInfo{}
		return nil
	}
	s.add("getAlbumInfo", false, albumInfo)
	s.add("getAlbumInfo2", false, albumInfo)
}
