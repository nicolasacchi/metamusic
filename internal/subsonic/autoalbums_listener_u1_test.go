package subsonic_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// TestListenerClientHasNoMuseAlbums (U1-INT-B): the Muse and Unfiled auto-albums group Songs by Muse, which is Muse structure
// a Listener never sees (C308.3), and with subsonic_muse_names on they carry the Muse's title. A Listener's client lists
// neither, cannot open them by id (error 70, the answer of an unknown album) and no artist, index or song names a Muse; an
// Owner's client keeps both albums and the Muse name.
func TestListenerClientHasNoMuseAlbums(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	e.names(true)
	owner := e.c
	muse := e.muse(librarytest.L2Muse1Title)
	listener := e.r.SubsonicClientFor(t, servertest.UserListener, subsonictest.ClientOpts{Name: "Listener album app", Write: false})
	for _, typ := range []string{"newest", "alphabeticalByName", "alphabeticalByArtist", "random"} {
		got := e.albumIDsWith(owner, typ)
		if !slices.Contains(got, muse) || !slices.Contains(got, "unf") {
			t.Fatalf("setup: the Owner's %s list lacks the Muse album: %v", typ, got)
		}
		for _, id := range e.albumIDsWith(listener, typ) {
			if strings.HasPrefix(id, "mus-") || id == "unf" {
				t.Errorf("%s: a Listener's client lists the auto-album %s", typ, id)
			}
		}
	}
	for _, id := range []string{muse, "unf"} {
		for _, ep := range []string{"getAlbum", "getMusicDirectory"} {
			if resp := listener.Call(t, ep, qv("id", id), subsonictest.CallOpts{JSON: true}); resp.Code != 70 {
				t.Errorf("%s %s for a Listener's client: code %d, want 70", ep, id, resp.Code)
			}
			if resp := owner.Call(t, ep, qv("id", id), subsonictest.CallOpts{JSON: true}); resp.Code != 0 {
				t.Errorf("%s %s for the Owner's client: code %d", ep, id, resp.Code)
			}
		}
	}
	for _, ep := range []string{"getArtists", "getIndexes", "getMusicDirectory"} {
		kv := []string{}
		if ep == "getMusicDirectory" {
			kv = []string{"id", "ar-metamusic"}
		}
		body := string(listener.Call(t, ep, qv(kv...), subsonictest.CallOpts{JSON: true}).Body)
		for _, bad := range []string{"mus-", "unf", librarytest.L2Muse1Title, "Synthetic Curation Muse"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s for a Listener's client names %q", ep, bad)
			}
		}
	}
}

// albumIDsWith is albumIDs for another client of the same rig.
func (e *recEnv) albumIDsWith(c *subsonictest.Client, typ string) []string {
	e.t.Helper()
	resp := c.Call(e.t, "getAlbumList2", qv("type", typ, "size", "500"), subsonictest.CallOpts{JSON: true})
	if resp.Status != "ok" {
		e.t.Fatalf("getAlbumList2 %s: %d %s", typ, resp.Code, resp.Body)
	}
	return ids(list(resp.Doc, "albumList2"))
}
