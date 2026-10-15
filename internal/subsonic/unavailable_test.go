package subsonic

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
	"github.com/nicolasacchi/metamusic/internal/playlists"
)

// s3calls is one valid call of every S3 endpoint.
var s3calls = []struct {
	ep string
	kv []string
}{
	{"stream", []string{"id", luna}}, {"download", []string{"id", luna}}, {"getCoverArt", []string{"id", luna}},
	{"star", []string{"id", luna}}, {"setRating", []string{"id", luna, "rating", "3"}},
	{"scrobble", []string{"id", luna}}, {"reportPlayback", []string{"mediaId", luna, "state", "starting"}},
	{"getBookmarks", nil}, {"createBookmark", []string{"id", luna, "position", "1000", "comment", "x"}}, {"deleteBookmark", []string{"id", luna}},
	{"getPlayQueue", nil}, {"savePlayQueue", []string{"id", luna}},
	{"getPlaylists", nil}, {"getPlaylist", []string{"id", "p"}}, {"createPlaylist", []string{"name", "n"}},
	{"updatePlaylist", []string{"playlistId", "p", "name", "n"}}, {"deletePlaylist", []string{"id", "p"}},
	{"getLyrics", []string{"title", "luna"}}, {"getLyricsBySongId", []string{"id", luna}},
}

func TestSubsonic_S3WithoutServices(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.srv.cat, x.srv.o.Store = nil, nil
	x.srv.judg, x.srv.listen, x.srv.plist, x.srv.resolve, x.srv.covers, x.srv.dls = nil, nil, nil, nil, nil, nil
	for _, c := range s3calls {
		w := x.get(c.ep, x.rwq(c.kv...), libIP)
		eq(t, c.ep+" code", w.code(t), 0)
		eq(t, c.ep+" message", dig(t, w.doc(t), "error", "message"), "service unavailable")
	}
}

func TestSubsonic_S3WithoutCollaborators(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.writeFixtureFiles(t)
	unavailable := func(ep string, kv ...string) {
		t.Helper()
		w := x.get(ep, x.rwq(kv...), libIP)
		eq(t, ep+" message", dig(t, w.doc(t), "error", "message"), "service unavailable")
	}
	pid := dig(t, x.call("createPlaylist", "name", "P", "songId", luna), "playlist", "id").(string)
	x.srv.judg = nil
	unavailable("star", "id", luna)
	unavailable("setRating", "id", luna, "rating", "3")
	unavailable("createBookmark", "id", luna, "position", "1", "comment", "hello")
	x.srv.listen = nil
	unavailable("scrobble", "id", luna)
	unavailable("reportPlayback", "mediaId", luna, "state", "starting", "positionMs", "0")
	x.srv.plist = nil
	unavailable("createPlaylist", "name", "n")
	unavailable("updatePlaylist", "playlistId", pid, "name", "n")
	unavailable("deletePlaylist", "id", pid)
	x.srv.resolve, x.srv.covers, x.srv.dls = nil, nil, nil
	unavailable("stream", "id", luna)
	unavailable("download", "id", luna)
	unavailable("getCoverArt", "id", luna)
	x.srv.o.Store = nil
	unavailable("getBookmarks")
	unavailable("savePlayQueue", "id", luna)
}

type errResolver struct{ err error }

func (e errResolver) Resolve(context.Context, string, string) (serve.Object, error) {
	return serve.Object{}, e.err
}

type errCovers struct{ err error }

func (e errCovers) EnsureTrack(context.Context, string, int) (serve.Object, string, error) {
	return serve.Object{}, "", e.err
}

func (e errCovers) EnsureAlbum(context.Context, string, int) (serve.Object, string, error) {
	return serve.Object{}, "", e.err
}

type errDownloads struct{ err error }

func (e errDownloads) Ensure(context.Context, string) (tagged.Download, error) {
	return tagged.Download{}, e.err
}

func TestSubsonic_MediaSourceFailures(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	boom := errors.New("boom /srv/secret/path")
	for _, tc := range []struct {
		err  error
		want int
	}{{library.ErrNotFound, 70}, {serve.ErrNotFound, 70}, {library.ErrMediaUnavailable, 70}, {boom, 0}} {
		x.srv.resolve, x.srv.covers, x.srv.dls = errResolver{tc.err}, errCovers{tc.err}, errDownloads{tc.err}
		for _, ep := range []string{"stream", "download", "getCoverArt"} {
			w := x.get(ep, x.rwq("id", luna), libIP)
			eq(t, ep+" "+tc.err.Error(), w.code(t), tc.want)
			if tc.want == 0 {
				eq(t, ep+" message", dig(t, w.doc(t), "error", "message"), "internal error")
			}
		}
	}
	// the fixture files are not on disk: the media handler answers 500 itself
	x.srv.resolve, x.srv.covers, x.srv.dls = x.lib, x.covers, x.dls
	w := x.raw("GET", "stream", x.rwq("id", luna), nil)
	statusOK(t, w, 500)
	// no MP3 rendition: 70 for the mp3 stream, the master still streams
	x.exec(`DROP TRIGGER renditions_no_delete`)
	x.exec(`DELETE FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, luna)
	eq(t, "no rendition", x.get("stream", x.rwq("id", luna, "format", "mp3"), libIP).code(t), 70)
}

type failingListener struct{ err error }

func (f failingListener) Log(context.Context, auth.Principal, string, []listening.Event) (int, int, []listening.Rejected, error) {
	return 0, 0, nil, f.err
}

func TestSubsonic_ListenFailures(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.srv.listen = failingListener{err: errors.New("disk")}
	w := x.get("scrobble", x.rwq("id", luna), libIP)
	eq(t, "scrobble", dig(t, w.doc(t), "error", "message"), "internal error")
	w = x.get("reportPlayback", x.rwq("mediaId", luna, "state", "starting", "positionMs", "0"), libIP)
	eq(t, "reportPlayback", dig(t, w.doc(t), "error", "message"), "internal error")
	eq(t, "bad position", x.callCode("reportPlayback", "mediaId", luna, "state", "playing", "positionMs", "x"), 0)
	eq(t, "negative position", x.callCode("reportPlayback", "mediaId", luna, "state", "playing", "positionMs", "-1"), 0)
	// an event the listening service rejects (a client time of 1970) is dropped, not an error
	x.srv.listen = listening.New(x.st, x.clk.now)
	eq(t, "ancient time", x.callCode("scrobble", "id", luna, "time", "1000"), 0)
	eq(t, "no event", len(x.events(luna)), 0)
	// the position is clamped to the track so a slightly long final report is not lost
	x.callCode("reportPlayback", "mediaId", citta, "state", "starting", "positionMs", "0")
	x.callCode("reportPlayback", "mediaId", citta, "state", "playing", "positionMs", "0")
	x.callCode("reportPlayback", "mediaId", citta, "state", "stopped", "positionMs", "9999999")
	eq(t, "clamped", kinds(x.events(citta)), "start segment[0,6000] complete")
}

type failingPlaylists struct {
	playlister
	err error
}

func (f failingPlaylists) Create(context.Context, auth.Principal, playlists.NewPlaylist) (playlists.Playlist, error) {
	return playlists.Playlist{}, f.err
}

func TestSubsonic_PlaylistEdgeCases(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.srv.plist = failingPlaylists{playlister: x.plist, err: errors.New("disk")}
	w := x.get("createPlaylist", x.rwq("name", "n"), libIP)
	eq(t, "internal", dig(t, w.doc(t), "error", "message"), "internal error")
	x.srv.plist = x.plist
	eq(t, "replace unknown", x.callCode("createPlaylist", "playlistId", "no-such", "songId", luna), 70)
	eq(t, "update unknown without changes", x.callCode("updatePlaylist", "playlistId", "no-such"), 70)
	// replacing with no songs empties the playlist
	id := dig(t, x.call("createPlaylist", "name", "E", "songId", luna), "playlist", "id").(string)
	d := x.call("createPlaylist", "playlistId", id, "name", "E2")
	eq(t, "emptied", len(list(t, d, "playlist", "entry")), 0)
	eq(t, "renamed", dig(t, d, "playlist", "name"), "E2")
	// removing the only entry leaves an empty playlist
	x.callCode("updatePlaylist", "playlistId", id, "songIdToAdd", luna)
	eq(t, "remove only", x.callCode("updatePlaylist", "playlistId", id, "songIndexToRemove", "0"), 0)
	eq(t, "now empty", len(list(t, x.call("getPlaylist", "id", id), "playlist", "entry")), 0)
}

func TestSubsonic_BookmarkAndQueueEdgeCases(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	// non-song ids in a saved queue are dropped; a bad position is refused
	eq(t, "non-song ids", x.callCode("savePlayQueue", "id", al(luna), "id", luna), 0)
	eq(t, "kept", len(list(t, x.call("getPlayQueue"), "playQueue", "entry")), 1)
	eq(t, "bad position", x.callCode("savePlayQueue", "id", luna, "position", "x"), 0)
	// a bookmark whose track vanished from the catalog view is skipped, not an error
	x.callCode("createBookmark", "id", luna, "position", "1000")
	x.exec(`DROP TRIGGER IF EXISTS subsonic_bookmarks_frozen`)
	x.exec(`PRAGMA foreign_keys = OFF`)
	x.exec(`UPDATE subsonic_bookmarks SET track_id = 'gone' WHERE track_id = ?`, luna)
	eq(t, "skipped", len(list(t, x.call("getBookmarks"), "bookmarks", "bookmark")), 0)
}

func TestSubsonic_StorageFailuresAreInternalErrors(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	internal := func(ep string, kv ...string) {
		t.Helper()
		w := x.get(ep, x.rwq(kv...), libIP)
		eq(t, ep+" message", dig(t, w.doc(t), "error", "message"), "internal error")
	}
	x.exec(`DROP TABLE subsonic_bookmarks`)
	internal("getBookmarks")
	internal("createBookmark", "id", luna, "position", "1", "comment", "hello")
	internal("deleteBookmark", "id", luna)
	x.exec(`DROP TABLE subsonic_play_queue`)
	internal("getPlayQueue")
	internal("savePlayQueue", "id", luna)
	x.exec(`DROP TABLE playlists`)
	internal("getPlaylists")
	internal("getPlaylist", "id", "p")
	internal("updatePlaylist", "playlistId", "p", "name", "n")
	internal("deletePlaylist", "id", "p")
}
