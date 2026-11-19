package subsonic

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/clean"
)

var d1Users = map[string]auth.Role{"alice": auth.RoleOwner, "bob": auth.RoleEditor, "carol": auth.RoleListener}

// userClient creates a real Subsonic client owned by bob (editor) or carol (listener) through the store, as that user's own
// login would (D-U1-14), and returns a request builder for it. The principal is not injected: the server authenticates the
// API key and builds the principal from the client row's owner key.
func (x *s3rig) userClient(t *testing.T, user string, write bool) func(kv ...string) url.Values {
	t.Helper()
	x.creds.UseUsers(d1Users)
	by := auth.Principal{Kind: auth.KindHuman, ID: user, Name: user, User: user, Role: d1Users[user], Scopes: auth.ScopesFor(d1Users[user]), Via: "proxy"}
	_, sec, err := x.creds.Create(context.Background(), by, auth.NewSubsonicClient{Name: user + " app", Username: user + ".app", Write: write, APIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	return func(kv ...string) url.Values { return multiParams(sec.APIKey, kv...) }
}

func (r *rig) scalar(q string, args ...any) string {
	r.t.Helper()
	var v sql.NullString
	if err := r.st.R.QueryRow(q, args...).Scan(&v); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return v.String
}

func (x *s3rig) callAs(who func(kv ...string) url.Values, ep string, kv ...string) reply {
	x.t.Helper()
	return x.get(ep, who(kv...), libIP)
}

// TestUserClientsCatalogShape: a Listener's real client sees the clean rendition's suffix, content type and size (or none) and no
// style comment, never the master's size or the seed in the path; the Owner's client and an Editor's keep the master's.
func TestUserClientsCatalogShape(t *testing.T) {
	t.Parallel()
	x, tn := listenerRig(t)
	carol := x.userClient(t, "carol", true)
	bob := x.userClient(t, "bob", true)
	ownerSong := dig(t, x.call("getSong", "id", twinA), "song")
	own := ownerSong.(map[string]any)
	if own["comment"] == nil || own["size"] == nil || !strings.Contains(own["path"].(string), "__") {
		t.Fatalf("the Owner's song lost its shape: %v", own)
	}
	masterSize := own["size"]
	before := dig(t, x.callAs(carol, "getSong", "id", twinA).doc(t), "song").(map[string]any)
	if before["comment"] != nil || before["suffix"] != "flac" || before["contentType"] != "audio/flac" || before["size"] != nil ||
		strings.Contains(before["path"].(string), "__") || before["bitDepth"] != nil {
		t.Errorf("a Listener's song before any rendition: %v", before)
	}
	// streaming builds flac_clean; the catalog then reports that row's size, never the master's
	b := x.raw("GET", "stream", carol("id", twinA, "format", "raw"), nil)
	statusOK(t, b, 200)
	size := float64(b.Body.Len())
	got := dig(t, x.callAs(carol, "getSong", "id", twinA).doc(t), "song").(map[string]any)
	if got["size"] != size || got["size"] == masterSize || got["comment"] != nil {
		t.Errorf("a Listener's song after the build: size %v (rendition %v, master %v) comment %v", got["size"], size, masterSize, got["comment"])
	}
	if sha(b.Body.Bytes()) == tn[twinA].MasterSHA {
		t.Error("the stream is the master")
	}
	// the other catalog paths carry the same shape: no style comment, no master size, no seed in the path
	for name, doc := range map[string]map[string]any{
		"getAlbum":       x.callAs(carol, "getAlbum", "id", al(twinA)).doc(t),
		"search3":        x.callAs(carol, "search3", "query", "twin").doc(t),
		"getRandomSongs": x.callAs(carol, "getRandomSongs", "size", "50").doc(t),
	} {
		raw, _ := json.Marshal(doc)
		if strings.Contains(string(raw), `"comment":`) || strings.Contains(string(raw), "__") || strings.Contains(string(raw), `"size":`+strconv.Itoa(int(masterSize.(float64)))+`,`) {
			t.Errorf("%s: a Listener's reply carries the style, the seed or the master's size: %s", name, raw)
		}
		if !strings.Contains(string(raw), `"suffix":"flac"`) {
			t.Errorf("%s: no songs in the reply: %s", name, raw)
		}
	}
	if e := dig(t, x.callAs(bob, "getSong", "id", twinA).doc(t), "song").(map[string]any); e["size"] != masterSize || e["comment"] == nil {
		t.Errorf("an Editor's song lost the master's shape: %v", e)
	}
}

// TestUserClientsOwnerSideOnlyEndpoints: star, unstar, bookmarks and the play queue are refused (error 50) for an Editor's and a
// Listener's client and change nothing; the Owner's client is unchanged. A Listener's client cannot change playlists; rating
// and scrobbling need only rate and write the user's own rows.
func TestUserClientsOwnerSideOnlyEndpoints(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	carol := x.userClient(t, "carol", true)
	bob := x.userClient(t, "bob", true)
	statusBefore := x.scalar(`SELECT status FROM tracks WHERE id = ?`, luna)
	for who, c := range map[string]func(kv ...string) url.Values{"bob": bob, "carol": carol} {
		for _, call := range [][]string{{"star", "id", luna}, {"unstar", "id", luna}, {"createBookmark", "id", luna, "position", "1000"},
			{"deleteBookmark", "id", luna}, {"getBookmarks"}, {"savePlayQueue", "id", luna}, {"getPlayQueue"}} {
			if code := x.callAs(c, call[0], call[1:]...).code(t); code != 50 {
				t.Errorf("%s %s: code %d, want 50", who, call[0], code)
			}
		}
	}
	eq(t, "status unchanged", x.scalar(`SELECT status FROM tracks WHERE id = ?`, luna), statusBefore)
	eq(t, "no bookmark", x.scalar(`SELECT COUNT(*) FROM subsonic_bookmarks`), "0")
	eq(t, "no play queue", x.scalar(`SELECT COUNT(*) FROM subsonic_play_queue`), "0")
	if code := x.callCode("createBookmark", "id", luna, "position", "1000"); code != 0 { // the Owner's client
		t.Errorf("the Owner's bookmark: code %d", code)
	}
	if code := x.callCode("savePlayQueue", "id", luna); code != 0 {
		t.Errorf("the Owner's play queue: code %d", code)
	}
	// a Listener holds rate but not write: playlists are refused, rating and scrobbling work
	if code := x.callAs(carol, "createPlaylist", "name", "mine", "songId", luna).code(t); code != 50 {
		t.Errorf("Listener createPlaylist: code %d, want 50", code)
	}
	if code := x.callAs(bob, "createPlaylist", "name", "bobs", "songId", luna).code(t); code != 0 {
		t.Errorf("Editor createPlaylist: code %d", code)
	}
	if code := x.callAs(carol, "setRating", "id", luna, "rating", "3").code(t); code != 0 {
		t.Errorf("Listener setRating: code %d", code)
	}
	if code := x.callAs(carol, "scrobble", "id", luna).code(t); code != 0 {
		t.Errorf("Listener scrobble: code %d", code)
	}
}

// TestUserClientSetRatingIsTheUsers: setRating by a Listener's real client writes carol's row and leaves the Owner's rating, the
// Owner's derived listening state and the Owner's star untouched; an Editor's rates as bob.
func TestUserClientSetRatingIsTheUsers(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	carol := x.userClient(t, "carol", true)
	bob := x.userClient(t, "bob", true)
	x.rate(luna, 4) // the Owner's rating
	ownerRow := x.scalar(`SELECT stars || '/' || updated_at FROM ratings WHERE track_id = ? AND source = 'human' AND rater = 'owner'`, luna)
	if code := x.callAs(carol, "setRating", "id", luna, "rating", "1").code(t); code != 0 {
		t.Fatalf("setRating: code %d", code)
	}
	if code := x.callAs(bob, "setRating", "id", luna, "rating", "5").code(t); code != 0 {
		t.Fatalf("setRating: code %d", code)
	}
	eq(t, "carol", x.scalar(`SELECT stars FROM ratings WHERE track_id = ? AND rater = 'carol'`, luna), "1")
	eq(t, "bob", x.scalar(`SELECT stars FROM ratings WHERE track_id = ? AND rater = 'bob'`, luna), "5")
	eq(t, "owner row untouched", x.scalar(`SELECT stars || '/' || updated_at FROM ratings WHERE track_id = ? AND source = 'human' AND rater = 'owner'`, luna), ownerRow)
	// each client reads back its own rating
	eq(t, "carol userRating", dig(t, x.callAs(carol, "getSong", "id", luna).doc(t), "song", "userRating"), 1)
	eq(t, "bob userRating", dig(t, x.callAs(bob, "getSong", "id", luna).doc(t), "song", "userRating"), 5)
	eq(t, "owner userRating", dig(t, x.call("getSong", "id", luna), "song", "userRating"), 4)
	// clearing carol's rating clears only her row
	x.callAs(carol, "setRating", "id", luna, "rating", "0")
	eq(t, "carol cleared", dig(t, x.callAs(carol, "getSong", "id", luna).doc(t), "song", "userRating"), nil)
	eq(t, "owner still", dig(t, x.call("getSong", "id", luna), "song", "userRating"), 4)
}

// TestUserClientScrobbleLeavesOwnerHeat: a Listener client's play counts for nobody on the Owner side.
func TestUserClientScrobbleLeavesOwnerHeat(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.st.SetOwner("alice")
	carol := x.userClient(t, "carol", true)
	before := dig(t, x.call("getSong", "id", luna), "song", "playCount")
	if code := x.callAs(carol, "scrobble", "id", luna).code(t); code != 0 {
		t.Fatalf("scrobble: code %d", code)
	}
	eq(t, "events recorded for the client", x.scalar(`SELECT COUNT(*) FROM listen_events WHERE principal_id LIKE 'device:%'`) != "0", true)
	eq(t, "the Owner's playCount", dig(t, x.call("getSong", "id", luna), "song", "playCount"), before)
}

func TestWriteScope(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]auth.Scope{"setRating": auth.ScopeRate, "scrobble": auth.ScopeRate, "reportPlayback": auth.ScopeRate,
		"star": auth.ScopeWrite, "createPlaylist": auth.ScopeWrite, "createBookmark": auth.ScopeWrite} {
		if got := writeScope(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}

// TestUserClientsLossyMasterShape: for a lossy master a Listener's client reports mp3, audio/mpeg and the mp3_320_clean
// rendition's size once built (none before); an MP3-default client of a lossless track reports the transcoded suffix.
func TestUserClientsLossyMasterShape(t *testing.T) {
	t.Parallel()
	x, lossy, master := newArchiveRig(t)
	x.srv.rends = clean.New(clean.Options{Store: x.st, Data: x.srv.o.Data, Now: x.clk.now})
	carol := x.userClient(t, "carol", true)
	owner := dig(t, x.call("getSong", "id", lossy), "song").(map[string]any)
	if owner["size"] != float64(len(master)) || owner["suffix"] != "mp3" {
		t.Fatalf("the Owner's lossy song: %v", owner)
	}
	before := dig(t, x.callAs(carol, "getSong", "id", lossy).doc(t), "song").(map[string]any)
	if before["suffix"] != "mp3" || before["contentType"] != "audio/mpeg" || before["size"] != nil || before["comment"] != nil {
		t.Errorf("a Listener's lossy song before the build: %v", before)
	}
	w := x.raw("GET", "stream", carol("id", lossy), nil)
	statusOK(t, w, 200)
	after := dig(t, x.callAs(carol, "getSong", "id", lossy).doc(t), "song").(map[string]any)
	if after["size"] != float64(w.Body.Len()) || after["suffix"] != "mp3" {
		t.Errorf("a Listener's lossy song after the build: %v (stream %d bytes)", after, w.Body.Len())
	}
}

// TestToListenerChildMP3Default covers the MP3-default branch of a lossless track: the transcoded fields name mp3 at 320 and the
// size is the mp3_320_clean row's, or none.
func TestToListenerChildMP3Default(t *testing.T) {
	t.Parallel()
	s := songRow{Listener: true, DurationS: 10, FlacCleanBytes: sql.NullInt64{Int64: 1000, Valid: true}, MasterBytes: 999999}
	var ch Child
	if ext := toListenerChild(&ch, s, true); ext != "flac" || ch.TranscodedSuffix != "mp3" || ch.BitRate != 320 || ch.Size != 0 || ch.Suffix != "flac" {
		t.Errorf("no mp3 rendition yet: %+v", ch)
	}
	s.MP3Bytes = sql.NullInt64{Int64: 400, Valid: true}
	ch = Child{}
	if toListenerChild(&ch, s, true); ch.Size != 400 || ch.TranscodedContentType != "audio/mpeg" {
		t.Errorf("with the mp3 rendition: %+v", ch)
	}
	ch = Child{}
	if toListenerChild(&ch, s, false); ch.Size != 1000 || ch.TranscodedSuffix != "" || ch.BitRate != 1 {
		t.Errorf("flac default: %+v", ch)
	}
}

// TestListenerChildHidesOwnerDecision: the keep decision (starred) is Owner-side state a Listener's song never carries.
func TestListenerChildHidesOwnerDecision(t *testing.T) {
	t.Parallel()
	s := songRow{ID: "t1", Status: "final", StatusSetAt: "2026-10-01T10:00:00Z", Listener: true}
	if ch := toChild(s, false); ch.Starred != "" {
		t.Errorf("listener starred: %q", ch.Starred)
	}
	s.Listener = false
	if ch := toChild(s, false); ch.Starred == "" {
		t.Error("owner lost starred")
	}
}
