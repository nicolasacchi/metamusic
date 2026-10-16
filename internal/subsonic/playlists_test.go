package subsonic

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/playlists"
)

const offlineSetID = "00000000-0000-7000-8000-0000000000f1"

func plByName(t *testing.T, d map[string]any, name string) map[string]any {
	t.Helper()
	for _, p := range list(t, d, "playlists", "playlist") {
		if p["name"] == name {
			return p
		}
	}
	return nil
}

func TestSubsonic_PlaylistsCRUD(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	// the offline set is a playlist like any other
	off := plByName(t, x.call("getPlaylists"), "Offline set")
	if off == nil {
		t.Fatal("offline set missing from getPlaylists")
	}
	eq(t, "offline id", off["id"], offlineSetID)
	eq(t, "offline owner", off["owner"], "rwclient")
	eq(t, "offline public", off["public"], false)
	eq(t, "offline count", off["songCount"], 0)
	// create
	d := x.call("createPlaylist", "name", "Mix", "songId", luna, "songId", citta)
	pl := dig(t, d, "playlist").(map[string]any)
	id := pl["id"].(string)
	eq(t, "name", pl["name"], "Mix")
	eq(t, "songCount", pl["songCount"], 2)
	eq(t, "duration", pl["duration"], 18)
	eq(t, "entries", strings.Join(ids(list(t, d, "playlist", "entry")), ","), luna+","+citta)
	eq(t, "owner", pl["owner"], "rwclient")
	for _, k := range []string{"created", "changed"} {
		if pl[k] == nil {
			t.Errorf("%s missing", k)
		}
	}
	mix := plByName(t, x.call("getPlaylists"), "Mix")
	if mix == nil || mix["id"] != id {
		t.Fatalf("getPlaylists: %v", mix)
	}
	eq(t, "list count", mix["songCount"], 2)
	// getPlaylist
	d = x.call("getPlaylist", "id", id)
	eq(t, "get entries", strings.Join(ids(list(t, d, "playlist", "entry")), ","), luna+","+citta)
	// updatePlaylist: add, remove by index, rename and comment
	eq(t, "add", x.callCode("updatePlaylist", "playlistId", id, "songIdToAdd", twinA), 0)
	eq(t, "after add", strings.Join(ids(list(t, x.call("getPlaylist", "id", id), "playlist", "entry")), ","), luna+","+citta+","+twinA)
	eq(t, "remove", x.callCode("updatePlaylist", "playlistId", id, "songIndexToRemove", "0"), 0)
	eq(t, "after remove", strings.Join(ids(list(t, x.call("getPlaylist", "id", id), "playlist", "entry")), ","), citta+","+twinA)
	eq(t, "remove+add", x.callCode("updatePlaylist", "playlistId", id, "songIndexToRemove", "1", "songIdToAdd", short, "songIdToAdd", luna), 0)
	eq(t, "after both", strings.Join(ids(list(t, x.call("getPlaylist", "id", id), "playlist", "entry")), ","), citta+","+short+","+luna)
	eq(t, "rename", x.callCode("updatePlaylist", "playlistId", id, "name", "Renamed", "comment", "hello", "public", "true"), 0)
	d = x.call("getPlaylist", "id", id)
	eq(t, "renamed", dig(t, d, "playlist", "name"), "Renamed")
	eq(t, "comment", dig(t, d, "playlist", "comment"), "hello")
	eq(t, "still private", dig(t, d, "playlist", "public"), false)
	eq(t, "index out of range is ignored", x.callCode("updatePlaylist", "playlistId", id, "songIndexToRemove", "99"), 0)
	eq(t, "nothing to change", x.callCode("updatePlaylist", "playlistId", id), 0)
	eq(t, "unchanged", len(list(t, x.call("getPlaylist", "id", id), "playlist", "entry")), 3)
	// createPlaylist with playlistId replaces the content
	d = x.call("createPlaylist", "playlistId", id, "songId", twinB)
	eq(t, "replaced", strings.Join(ids(list(t, d, "playlist", "entry")), ","), twinB)
	eq(t, "replaced id", dig(t, d, "playlist", "id"), id)
	// delete hides
	eq(t, "delete", x.callCode("deletePlaylist", "id", id), 0)
	if plByName(t, x.call("getPlaylists"), "Renamed") != nil {
		t.Error("deleted playlist still listed")
	}
	eq(t, "get deleted", x.callCode("getPlaylist", "id", id), 70)
	var hidden string
	if err := x.st.R.QueryRow(`SELECT COALESCE(hidden_at, '') FROM playlists WHERE id = ?`, id).Scan(&hidden); err != nil || hidden == "" {
		t.Errorf("row not hidden: %q %v", hidden, err)
	}
	// the offline set cannot be deleted
	eq(t, "delete offline", x.callCode("deletePlaylist", "id", offlineSetID), 50)
	if plByName(t, x.call("getPlaylists"), "Offline set") == nil {
		t.Error("offline set disappeared")
	}
	// errors
	eq(t, "get unknown", x.callCode("getPlaylist", "id", "no-such"), 70)
	eq(t, "update unknown", x.callCode("updatePlaylist", "playlistId", "no-such", "name", "x"), 70)
	eq(t, "delete unknown", x.callCode("deletePlaylist", "id", "no-such"), 70)
	eq(t, "create nothing", x.callCode("createPlaylist"), 10)
	eq(t, "get missing", x.callCode("getPlaylist"), 10)
	eq(t, "update missing", x.callCode("updatePlaylist", "name", "x"), 10)
	eq(t, "delete missing", x.callCode("deletePlaylist"), 10)
	w := x.get("createPlaylist", x.rwq("name", "Bad", "songId", "no-such"), libIP)
	eq(t, "unknown song", w.code(t), 70)
	// an unusable index changes nothing and answers ok; a missing playlist still answers 70
	pid := dig(t, x.get("createPlaylist", x.rwq("name", "Idx", "songId", luna), libIP).doc(t), "playlist", "id").(string)
	eq(t, "bad index", x.callCode("updatePlaylist", "playlistId", pid, "songIndexToRemove", "x"), 0)
	eq(t, "bad index unknown playlist", x.callCode("updatePlaylist", "playlistId", "no-such", "songIndexToRemove", "x"), 70)
	eq(t, "unknown song to add", x.callCode("updatePlaylist", "playlistId", pid, "songIdToAdd", "no-such"), 70)
	eq(t, "empty name", x.callCode("createPlaylist", "name", "  "), 0)
	// users: only the caller's own
	eq(t, "own username", x.get("getPlaylists", x.rwq("username", "rwclient"), libIP).code(t), 0)
	eq(t, "other username", x.get("getPlaylists", x.rwq("username", "someone"), libIP).code(t), 50)
	// read-only clients read but cannot write
	eq(t, "ro read", x.get("getPlaylists", keyParams(x.roKey), libIP).code(t), 0)
	for _, ep := range []string{"createPlaylist", "updatePlaylist", "deletePlaylist"} {
		eq(t, "ro "+ep, x.get(ep, keyParams(x.roKey, "name", "x", "playlistId", "x", "id", "x"), libIP).code(t), 50)
	}
}

// conflictOnce is a playlister whose first Update loses a race against a REST edit of the same playlist.
type conflictOnce struct {
	playlister
	rest  func()
	calls atomic.Int32
}

func (c *conflictOnce) Update(ctx context.Context, p auth.Principal, id string, in playlists.PlaylistPatch) (playlists.Playlist, error) {
	if c.calls.Add(1) == 1 {
		c.rest() // the concurrent REST update lands between the bridge's read and its write
	}
	return c.playlister.Update(ctx, p, id, in)
}

func TestSubsonic_PlaylistsRetryOnConflict(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	id := dig(t, x.call("createPlaylist", "name", "Race", "songId", luna), "playlist", "id").(string)
	rest := func() {
		pl, err := x.plist.Get(context.Background(), id)
		if err != nil {
			t.Error(err)
			return
		}
		name := "edited over REST"
		if _, err := x.plist.Update(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice"}, id,
			playlists.PlaylistPatch{Revision: pl.Revision, Name: &name}); err != nil {
			t.Error(err)
		}
	}
	co := &conflictOnce{playlister: x.plist, rest: rest}
	x.srv.plist = co
	eq(t, "update retried", x.callCode("updatePlaylist", "playlistId", id, "songIdToAdd", citta), 0)
	eq(t, "two attempts", int(co.calls.Load()), 2)
	d := x.call("getPlaylist", "id", id)
	eq(t, "both edits applied", strings.Join(ids(list(t, d, "playlist", "entry")), ","), luna+","+citta)
	eq(t, "rest edit kept", dig(t, d, "playlist", "name"), "edited over REST")
	// delete retries the same way
	co.calls.Store(0)
	eq(t, "delete retried", x.callCode("deletePlaylist", "id", id), 0)
	if co.calls.Load() != 2 {
		t.Errorf("delete attempts = %d", co.calls.Load())
	}
	// a conflict that never clears gives up after three attempts with the fixed text
	always := &alwaysConflict{playlister: x.plist}
	x.srv.plist = always
	id2 := dig(t, x.call("createPlaylist", "name", "Stuck", "songId", luna), "playlist", "id").(string)
	w := x.get("updatePlaylist", x.rwq("playlistId", id2, "name", "x"), libIP)
	eq(t, "gives up", w.code(t), 0)
	eq(t, "attempts", int(always.calls.Load()), 3)
}

type alwaysConflict struct {
	playlister
	calls atomic.Int32
}

func (a *alwaysConflict) Update(context.Context, auth.Principal, string, playlists.PlaylistPatch) (playlists.Playlist, error) {
	a.calls.Add(1)
	return playlists.Playlist{}, &playlists.Error{Status: 409, Code: "REVISION_CONFLICT", Msg: "changed"}
}
