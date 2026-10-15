package subsonic

import (
	"errors"
	"math"
	"slices"
	"strconv"

	"github.com/nicolasacchi/metamusic/internal/playlists"
)

// offlineRole is the role of the protected offline set (D-M4a-19).
const offlineRole = "offline"

// updateAttempts bounds the read-update retries a revision conflict triggers (C109).
const updateAttempts = 3

func (s *server) registerPlaylists() {
	s.add("getPlaylists", false, (*call).getPlaylists)
	s.add("getPlaylist", false, (*call).getPlaylist)
	s.add("createPlaylist", true, (*call).createPlaylist)
	s.add("updatePlaylist", true, (*call).updatePlaylist)
	s.add("deletePlaylist", true, (*call).deletePlaylist)
}

func (c *call) playlister() (playlister, error) {
	if c.s.plist == nil {
		return nil, fail(errGeneric, "service unavailable")
	}
	return c.s.plist, nil
}

func toPlaylist(p playlistRow, owner string) Playlist {
	return Playlist{ID: p.ID, Name: p.Name, Comment: p.Desc, Owner: owner, SongCount: len(p.Items),
		Duration: int(math.Round(p.DurationS)), Created: iso(p.Created), Changed: iso(p.Changed)}
}

func (c *call) getPlaylists() error {
	cat, err := c.cat()
	if err != nil {
		return err
	}
	cl, err := c.client()
	if err != nil {
		return err
	}
	if u := c.str("username"); u != "" && u != cl.Username {
		return fail(errNotAuthorized, "the client is not authorized to see other users")
	}
	rows, err := cat.playlists(c.ctx)
	if err != nil {
		return err
	}
	out := &PlaylistList{Playlist: make([]Playlist, 0, len(rows))}
	for _, p := range rows {
		out.Playlist = append(out.Playlist, toPlaylist(p, cl.Username))
	}
	c.resp.Playlists = out
	return nil
}

// playlistWithSongs fills resp.Playlist for the playlist id.
func (c *call) playlistWithSongs(id string) error {
	cat, err := c.cat()
	if err != nil {
		return err
	}
	cl, err := c.client()
	if err != nil {
		return err
	}
	mp3, err := c.mp3Default()
	if err != nil {
		return err
	}
	p, ok, err := cat.playlist(c.ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fail(errNotFound, "playlist not found")
	}
	songs, err := cat.songsByIDs(c.ctx, p.Items)
	if err != nil {
		return err
	}
	c.resp.Playlist = &PlaylistWithSongs{Playlist: toPlaylist(p, cl.Username), Entry: toChildren(songs, mp3)}
	return nil
}

func (c *call) getPlaylist() error {
	id := c.str("id")
	if id == "" {
		return fail(errMissingParam, "required parameter is missing: id")
	}
	return c.playlistWithSongs(id)
}

func isConflict(err error) bool {
	var pe *playlists.Error
	return errors.As(err, &pe) && pe.Code == "REVISION_CONFLICT"
}

// updateRetrying reads the playlist's revision, builds the patch from it and updates; a revision conflict (a REST
// edit landed in between) re-reads and tries again, up to updateAttempts times.
func (c *call) updateRetrying(id string, build func(cur playlistRow) playlists.PlaylistPatch) error {
	cat, err := c.cat()
	if err != nil {
		return err
	}
	pl, err := c.playlister()
	if err != nil {
		return err
	}
	for range updateAttempts {
		cur, ok, err := cat.playlist(c.ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return fail(errNotFound, "playlist not found")
		}
		patch := build(cur)
		patch.Revision = cur.Revision
		_, err = pl.Update(c.ctx, c.principal, id, patch)
		if isConflict(err) {
			continue
		}
		return mapService(err, "playlist change not allowed")
	}
	return fail(errGeneric, "playlist change not allowed")
}

func (c *call) createPlaylist() error {
	name, id := c.str("name"), c.str("playlistId")
	if name == "" && id == "" {
		return fail(errMissingParam, "required parameter is missing: name")
	}
	songs := append([]string{}, c.all("songId")...)
	if id == "" {
		pl, err := c.playlister()
		if err != nil {
			return err
		}
		created, err := pl.Create(c.ctx, c.principal, playlists.NewPlaylist{Name: name, TrackIDs: songs})
		if err != nil {
			return mapService(err, "playlist change not allowed")
		}
		return c.playlistWithSongs(created.ID)
	}
	err := c.updateRetrying(id, func(playlistRow) playlists.PlaylistPatch {
		p := playlists.PlaylistPatch{TrackIDs: songs}
		if name != "" {
			p.Name = &name
		}
		return p
	})
	if err != nil {
		return err
	}
	return c.playlistWithSongs(id)
}

func (c *call) updatePlaylist() error {
	id := c.str("playlistId")
	if id == "" {
		return fail(errMissingParam, "required parameter is missing: playlistId")
	}
	adds, removes := c.all("songIdToAdd"), c.all("songIndexToRemove")
	hasName, hasComment := c.params.Has("name"), c.params.Has("comment")
	if !hasName && !hasComment && len(adds) == 0 && len(removes) == 0 {
		cat, err := c.cat()
		if err != nil {
			return err
		}
		if _, ok, err := cat.playlist(c.ctx, id); err != nil {
			return err
		} else if !ok {
			return fail(errNotFound, "playlist not found")
		}
		return nil
	}
	drop := map[int]bool{}
	for _, v := range removes {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			drop[n] = true
		}
	}
	name, comment := c.str("name"), c.str("comment")
	return c.updateRetrying(id, func(cur playlistRow) playlists.PlaylistPatch {
		var p playlists.PlaylistPatch
		if hasName {
			p.Name = &name
		}
		if hasComment {
			p.Description = &comment
		}
		switch {
		case len(drop) > 0:
			keep := make([]string, 0, len(cur.Items)+len(adds))
			for i, t := range cur.Items {
				if !drop[i] {
					keep = append(keep, t)
				}
			}
			p.TrackIDs = append(keep, adds...)
		case len(adds) > 0:
			p.AddTrackIDs = slices.Clone(adds)
		}
		return p
	})
}

// deletePlaylist hides the playlist (nothing is ever deleted); the offline set cannot be hidden (D-M4a-19).
func (c *call) deletePlaylist() error {
	id := c.str("id")
	if id == "" {
		return fail(errMissingParam, "required parameter is missing: id")
	}
	cat, err := c.cat()
	if err != nil {
		return err
	}
	cur, ok, err := cat.playlist(c.ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fail(errNotFound, "playlist not found")
	}
	if cur.Role.String == offlineRole {
		return fail(errNotAuthorized, "the offline set cannot be deleted")
	}
	hide := true
	return c.updateRetrying(id, func(cur playlistRow) playlists.PlaylistPatch {
		return playlists.PlaylistPatch{Hidden: &hide}
	})
}
