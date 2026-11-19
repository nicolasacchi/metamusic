package subsonic

import (
	"context"
	"net/url"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func viewerCtx(role auth.Role) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindDevice, Via: "subsonic", ID: "c1", Role: role, User: "carol", Scopes: []auth.Scope{auth.ScopeListen}})
}

// TestListenerClientNeverReachesDiscarded pins C308.6 for the catalog: for a client that carries a Listener's role a
// discarded take is absent by id, in every list and album, in a playlist's members, count and duration, and folder 3 does
// not exist; the Owner's client and a role-less one keep everything.
func TestListenerClientNeverReachesDiscarded(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	r.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, short)
	at := r.clk.now().UTC().Format("2006-01-02T15:04:05.000Z")
	r.exec(`INSERT INTO playlists(id, name, items_json, items_count, created_by, updated_by, created_at, updated_at) VALUES('pl1', 'Mix', json_array(?, ?), 2, 'human:x', 'human:x', ?, ?)`, luna, short, at, at)
	cat := r.srv.cat
	for _, role := range []auth.Role{"", auth.RoleOwner, auth.RoleEditor} {
		ctx := viewerCtx(role)
		if _, ok, err := cat.song(ctx, short); err != nil || !ok {
			t.Errorf("role %q lost the discarded take by id: %v %v", role, ok, err)
		}
		if ok, _ := cat.albumExists(ctx, short); !ok {
			t.Errorf("role %q: album of a discarded take is gone", role)
		}
	}
	ctx := viewerCtx(auth.RoleListener)
	if _, ok, err := cat.song(ctx, short); err != nil || ok {
		t.Errorf("a Listener's client reads a discarded take by id: %v %v", ok, err)
	}
	if _, ok, err := cat.song(ctx, luna); err != nil || !ok {
		t.Errorf("a Listener's client lost a visible take: %v %v", ok, err)
	}
	rows, err := cat.songsByIDs(ctx, []string{short, luna})
	if err != nil || len(rows) != 1 || rows[0].ID != luna {
		t.Errorf("songsByIDs: %v %v", rows, err)
	}
	for _, frag := range []string{"t.status = 'discarded'", "1 = 1"} {
		songs, err := cat.searchSongs(ctx, frag, "", 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range songs {
			if s.ID == short {
				t.Errorf("searchSongs(%s) lists the discarded take", frag)
			}
		}
	}
	if ok, err := cat.albumExists(ctx, short); err != nil || ok {
		t.Errorf("albumExists for a lineage of only a discarded take: %v %v", ok, err)
	}
	albums, err := cat.searchAlbums(ctx, "1 = 1", "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range albums {
		if a.ID == short {
			t.Errorf("a lineage of only a discarded take is an album for a Listener's client")
		}
	}
	if albums, _ := cat.searchAlbums(viewerCtx(auth.RoleOwner), "t.status = 'discarded'", "", 100, 0); len(albums) == 0 {
		t.Errorf("the Owner's client lost folder 3")
	}

	owner, _, err := cat.playlist(viewerCtx(auth.RoleOwner), "pl1")
	if err != nil || len(owner.Items) != 2 {
		t.Fatalf("the Owner's playlist: %v %v", owner.Items, err)
	}
	pl, ok, err := cat.playlist(ctx, "pl1")
	if err != nil || !ok || len(pl.Items) != 1 || pl.Items[0] != luna || pl.DurationS >= owner.DurationS {
		t.Errorf("a Listener's playlist: %+v %v %v (owner duration %v)", pl, ok, err, owner.DurationS)
	}
	all, err := cat.playlists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if p.ID == "pl1" && len(p.Items) != 1 {
			t.Errorf("a Listener's playlists: %+v", p)
		}
	}
	if _, ok, err := cat.playlist(ctx, "nope"); ok || err != nil {
		t.Errorf("an unknown playlist: %v %v", ok, err)
	}
	empty := playlistRow{}
	if err := cat.hideDiscardedMembers(ctx, &empty); err != nil {
		t.Errorf("an empty playlist: %v", err)
	}
}

func TestListenerClientFolders(t *testing.T) {
	t.Parallel()
	listener, owner := viewerCtx(auth.RoleListener), viewerCtx(auth.RoleOwner)
	eq(t, "listener folders", len(visibleFolders(listener)), 2)
	eq(t, "owner folders", len(visibleFolders(owner)), 3)
	eq(t, "bare folders", len(visibleFolders(context.Background())), 3)
	eq(t, "listener user folders", len(userFolders(listener)), 2)
	eq(t, "owner user folders", len(userFolders(owner)), 3)
	c := func(ctx context.Context, folder string) error {
		cl := &call{ctx: ctx, params: url.Values{"musicFolderId": {folder}}}
		_, err := cl.folderFrag()
		return err
	}
	if err := c(listener, "3"); err == nil {
		t.Errorf("a Listener's client reaches folder 3")
	}
	for _, f := range []string{"1", "2", ""} {
		if err := c(listener, f); err != nil {
			t.Errorf("a Listener's client folder %q: %v", f, err)
		}
	}
	if err := c(owner, "3"); err != nil {
		t.Errorf("the Owner's client folder 3: %v", err)
	}
}

func TestOwnerClientWire(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	doc := r.call("getMusicFolders")
	if f, _ := dig(t, doc, "musicFolders", "musicFolder").([]any); len(f) != 3 {
		t.Errorf("the Owner's client sees %d folders", len(f))
	}
}
