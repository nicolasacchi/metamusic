package subsonic

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

const libIP = "198.51.100.77"

var (
	luna, citta, twinA, twinB, short = librarytest.TrackLuna, librarytest.TrackCitta, librarytest.TrackTwinA, librarytest.TrackTwinB, librarytest.TrackShort
	fixtureOrder                     = []string{luna, citta, twinA, twinB, short} // created_at order set by newLibRig
)

// newLibRig is a rig over the five-track fixture library: albums Luna Park, Città lenta, Twin A (+ sibling Twin B)
// and Cut Short, all drafts, created one minute apart in fixtureOrder.
func newLibRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, rigOpts{})
	librarytest.FixtureV2(t, r.st, r.clk.now())
	// created_at and recorded_at are frozen columns; the fixture database is private to this test.
	r.exec("DROP TRIGGER tracks_frozen")
	for i, id := range fixtureOrder {
		at := r.clk.now().Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339)
		r.exec("UPDATE tracks SET created_at = ?, status_set_at = ? WHERE id = ?", at, at, id)
	}
	return r
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.st.W.ExecContext(context.Background(), q, args...); err != nil {
		r.t.Fatalf("exec %q: %v", q, err)
	}
}

func (r *rig) setStatus(id, status string, at time.Time) {
	r.t.Helper()
	r.exec("UPDATE tracks SET status = ?, status_set_at = ? WHERE id = ?", status, at.UTC().Format(time.RFC3339), id)
}

func (r *rig) rate(id string, stars int) {
	r.t.Helper()
	at := r.clk.now().UTC().Format(time.RFC3339)
	r.exec(`INSERT INTO ratings(track_id, source, stars, principal_kind, principal_id, principal_name, created_at, updated_at)
		VALUES (?, 'human', ?, 'human', 'human:alice', 'alice', ?, ?)`, id, stars, at, at)
}

var evSeq atomic.Int64

func (r *rig) listen(id string, at time.Time) {
	r.t.Helper()
	n := evSeq.Add(1)
	r.exec(`INSERT INTO listen_events(id, track_id, session_id, kind, from_ms, source, principal_kind, principal_id, client_at, received_at)
		VALUES (?, ?, ?, 'start', 0, 'web', 'device', 'device:d', ?, ?)`, fmt.Sprintf("00000000-0000-4000-8000-%012d", n), id,
		"session-12345678", at.UTC().Format(time.RFC3339), at.UTC().Format(time.RFC3339))
}

// do answers the JSON document of one authenticated call as the read-write client.
func (r *rig) call(endpoint string, kv ...string) map[string]any {
	r.t.Helper()
	return r.get(endpoint, keyParams(r.rwKey, kv...), libIP).doc(r.t)
}

func (r *rig) callCode(endpoint string, kv ...string) int {
	r.t.Helper()
	return r.get(endpoint, keyParams(r.rwKey, kv...), libIP).code(r.t)
}

// dig walks nested maps ("album", "song") and returns the value.
func dig(t *testing.T, d map[string]any, path ...string) any {
	t.Helper()
	var cur any = d
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("dig %v: %T at %q (doc %v)", path, cur, p, d)
		}
		cur = m[p]
	}
	return cur
}

func list(t *testing.T, d map[string]any, path ...string) []map[string]any {
	t.Helper()
	v := dig(t, d, path...)
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		out = append(out, e.(map[string]any))
	}
	return out
}

func ids(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i["id"].(string))
	}
	return out
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func al(root string) string { return "al-" + root }

func TestSubsonic_AlbumIsLineage(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	d := r.call("getAlbum", "id", al(twinA))
	eq(t, "name", dig(t, d, "album", "name"), "Twin A")
	eq(t, "songCount", dig(t, d, "album", "songCount"), 2)
	songs := list(t, d, "album", "song")
	if !slices.Equal(ids(songs), []string{twinA, twinB}) {
		t.Fatalf("songs = %v", ids(songs))
	}
	eq(t, "track 1", songs[0]["track"], 1)
	eq(t, "track 2", songs[1]["track"], 2)
	for _, s := range songs {
		eq(t, "albumId", s["albumId"], al(twinA))
		eq(t, "parent", s["parent"], al(twinA))
		eq(t, "album", s["album"], "Twin A")
	}
	// the sibling's own getSong shows the lineage position
	sd := r.call("getSong", "id", twinB)
	eq(t, "getSong track", dig(t, sd, "song", "track"), 2)
	eq(t, "getSong albumId", dig(t, sd, "song", "albumId"), al(twinA))

	// update_track renames the root: the album shows the new name
	r.exec("UPDATE tracks SET title = ? WHERE id = ?", "Twin Renamed", twinA)
	d = r.call("getAlbum", "id", al(twinA))
	eq(t, "renamed", dig(t, d, "album", "name"), "Twin Renamed")
	eq(t, "renamed song album", list(t, d, "album", "song")[1]["album"], "Twin Renamed")
	// a root alone is a one-track album named after it
	eq(t, "luna", dig(t, r.call("getAlbum", "id", al(luna)), "album", "name"), "Luna Park")
}

func TestSubsonic_Folders(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	r.setStatus(short, "discarded", r.clk.now())
	r.setStatus(luna, "final", r.clk.now())
	songs := func(extra ...string) []string {
		d := r.call("search3", append([]string{"songCount", "100"}, extra...)...)
		return ids(list(t, d, "searchResult3", "song"))
	}
	if got := songs(); !slices.Equal(got, []string{luna, citta, twinA, twinB}) {
		t.Errorf("default folders = %v", got)
	}
	if got := songs("musicFolderId", "1"); !slices.Equal(got, []string{luna}) {
		t.Errorf("folder 1 = %v", got)
	}
	if got := songs("musicFolderId", "2"); !slices.Equal(got, []string{citta, twinA, twinB}) {
		t.Errorf("folder 2 = %v", got)
	}
	if got := songs("musicFolderId", "3"); !slices.Equal(got, []string{short}) {
		t.Errorf("folder 3 = %v", got)
	}
	if c := r.callCode("search3", "musicFolderId", "9"); c != 70 {
		t.Errorf("unknown folder = %d", c)
	}
	// a to_relisten track is in the Library folder
	r.setStatus(citta, "to_relisten", r.clk.now())
	if got := songs("musicFolderId", "1"); !slices.Equal(got, []string{luna, citta}) {
		t.Errorf("folder 1 with to_relisten = %v", got)
	}
	// a discarded take stays reachable by id
	eq(t, "getSong discarded", dig(t, r.call("getSong", "id", short), "song", "id"), short)
	d := r.call("getAlbum", "id", al(short))
	eq(t, "getAlbum discarded", dig(t, d, "album", "songCount"), 1)
	// getAlbumList2 and getMusicDirectory follow the rule
	if got := ids(list(t, r.call("getAlbumList2", "type", "newest", "size", "50"), "albumList2", "album")); slices.Contains(got, al(short)) {
		t.Errorf("discarded album listed by default: %v", got)
	}
	if got := ids(list(t, r.call("getAlbumList2", "type", "newest", "musicFolderId", "3"), "albumList2", "album")); !slices.Equal(got, []string{al(short)}) {
		t.Errorf("folder 3 albums = %v", got)
	}
}

func TestSubsonic_Search3Empty(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	for _, q := range [][]string{nil, {"query", ""}, {"query", `""`}} {
		d := r.call("search3", append([]string{"songCount", "50", "albumCount", "50"}, q...)...)
		if got := ids(list(t, d, "searchResult3", "song")); !slices.Equal(got, fixtureOrder) {
			t.Errorf("query %v songs = %v", q, got)
		}
		eq(t, "albums", len(list(t, d, "searchResult3", "album")), 4)
		eq(t, "artist", ids(list(t, d, "searchResult3", "artist")), []string{"ar-metamusic"})
	}
	// songCount=2 pages through every song once
	var got []string
	for off := 0; off < 6; off += 2 {
		d := r.call("search3", "songCount", "2", "songOffset", fmt.Sprint(off), "albumCount", "0", "artistCount", "0")
		page := ids(list(t, d, "searchResult3", "song"))
		if len(page) > 2 {
			t.Fatalf("page %d = %v", off, page)
		}
		got = append(got, page...)
	}
	if !slices.Equal(got, fixtureOrder) {
		t.Errorf("paged songs = %v", got)
	}
	// beyond the end => nothing
	d := r.call("search3", "songCount", "2", "songOffset", "99", "albumCount", "0", "artistCount", "0")
	if _, has := dig(t, d, "searchResult3").(map[string]any)["song"]; has {
		t.Errorf("offset beyond the end: %v", d)
	}
}

func TestSubsonic_Search3Query(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	d := r.call("search3", "query", "citta")
	eq(t, "citta", ids(list(t, d, "searchResult3", "song")), []string{citta})
	eq(t, "citta album", ids(list(t, d, "searchResult3", "album")), []string{al(citta)})
	if _, has := dig(t, d, "searchResult3").(map[string]any)["artist"]; has {
		t.Errorf("citta must not match the artist: %v", d)
	}
	// the root title matches an album although the song is its sibling
	d = r.call("search3", "query", "twin")
	eq(t, "twin albums", ids(list(t, d, "searchResult3", "album")), []string{al(twinA)})
	// the artist matches by prefix
	d = r.call("search3", "query", "meta")
	eq(t, "artist", ids(list(t, d, "searchResult3", "artist")), []string{"ar-metamusic"})
	// FTS operators are plain text
	for _, q := range []string{`"unbalanced`, `luna OR (`, `NEAR(a b)`, `title:x AND y`, `-luna`, `luna*`, `a" OR "b`, "***", `)`, `{title}: x`} {
		if c := r.callCode("search3", "query", q); c != 0 {
			t.Errorf("query %q => error %d", q, c)
		}
	}
	// symbols only => no hits
	d = r.call("search3", "query", "***")
	if _, has := dig(t, d, "searchResult3").(map[string]any)["song"]; has {
		t.Errorf("symbol-only query matched: %v", d)
	}
	eq(t, "luna by style", ids(list(t, r.call("search3", "query", "dream"), "searchResult3", "song")), []string{luna})
}

func TestSubsonic_Search2Shape(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	d := r.call("search2", "query", "twin")
	res := dig(t, d, "searchResult2").(map[string]any)
	albums := list(t, d, "searchResult2", "album")
	eq(t, "album id", albums[0]["id"], al(twinA))
	eq(t, "album isDir", albums[0]["isDir"], true)
	eq(t, "album title", albums[0]["title"], "Twin A")
	eq(t, "song count", len(list(t, d, "searchResult2", "song")), 2)
	if _, has := res["artist"]; has {
		t.Errorf("twin must not match the artist: %v", res)
	}
	d = r.call("search2")
	artists := list(t, d, "searchResult2", "artist")
	eq(t, "artist", artists[0], map[string]any{"id": "ar-metamusic", "name": "metamusic"})
	eq(t, "song isDir", list(t, d, "searchResult2", "song")[0]["isDir"], false)
}

func TestSubsonic_AlbumListTypes(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	base := r.clk.now()
	// recorded years: Luna 2020, Citta 2022, TwinA 2024 (TwinB 2025), Short 2025
	for id, y := range map[string]int{luna: 2020, citta: 2022, twinA: 2024, twinB: 2025, short: 2025} {
		r.exec("UPDATE tracks SET recorded_at = ? WHERE id = ?", fmt.Sprintf("%d-03-01T12:00:00Z", y), id)
	}
	r.rate(luna, 3)
	r.rate(citta, 5)
	r.rate(twinB, 4) // Twin A's album rates through its best member
	r.setStatus(luna, "final", base.Add(10*time.Minute))
	r.setStatus(twinB, "final", base.Add(20*time.Minute))
	r.listen(luna, base.Add(time.Hour))
	r.listen(luna, base.Add(2*time.Hour))
	r.listen(citta, base.Add(3*time.Hour))
	r.listen(twinA, base.Add(30*time.Minute))
	// a play by an agent does not count
	r.exec(`INSERT INTO listen_events(id, track_id, session_id, kind, from_ms, source, principal_kind, principal_id, client_at, received_at)
		VALUES ('00000000-0000-4000-8000-0000000000aa', ?, 'session-agent01', 'start', 0, 'mcp', 'agent', 'agent:x', ?, ?)`, short,
		base.Add(5*time.Hour).UTC().Format(time.RFC3339), base.Add(5*time.Hour).UTC().Format(time.RFC3339))

	order := func(kv ...string) []string {
		d := r.call("getAlbumList2", append([]string{"size", "50"}, kv...)...)
		return ids(list(t, d, "albumList2", "album"))
	}
	albums := func(roots ...string) []string {
		out := make([]string, len(roots))
		for i, x := range roots {
			out[i] = al(x)
		}
		return out
	}
	for typ, want := range map[string][]string{
		"newest":               albums(short, twinA, citta, luna),
		"highest":              albums(citta, twinA, luna, short), // 5, 4, 3, unrated last
		"frequent":             albums(luna, citta, twinA, short),
		"recent":               albums(citta, luna, twinA),
		"alphabeticalByName":   albums(citta, short, luna, twinA),
		"alphabeticalByArtist": albums(citta, short, luna, twinA),
		"starred":              albums(twinA, luna),
	} {
		if got := order("type", typ); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", typ, got, want)
		}
	}
	if got := order("type", "byYear", "fromYear", "2021", "toYear", "2024"); !slices.Equal(got, albums(citta, twinA)) {
		t.Errorf("byYear = %v", got)
	}
	if got := order("type", "byYear", "fromYear", "2024", "toYear", "2021"); !slices.Equal(got, albums(twinA, citta)) {
		t.Errorf("byYear reversed = %v", got)
	}
	if got := order("type", "byGenre", "genre", "Rock"); !slices.Equal(got, albums(twinA)) {
		t.Errorf("byGenre = %v", got)
	}
	if got := order("type", "newest", "size", "2", "offset", "1"); !slices.Equal(got, albums(twinA, citta)) {
		t.Errorf("newest page = %v", got)
	}
	// unknown, missing and incomplete types are error 10
	for _, kv := range [][]string{{"type", "bogus"}, nil, {"type", "byGenre"}} {
		if c := r.callCode("getAlbumList2", kv...); c != 10 {
			t.Errorf("getAlbumList2 %v => %d", kv, c)
		}
	}
	// getAlbumList answers directories with the album fields
	d := r.call("getAlbumList", "type", "newest", "size", "1")
	first := list(t, d, "albumList", "album")[0]
	eq(t, "dir id", first["id"], al(short))
	eq(t, "dir isDir", first["isDir"], true)
	eq(t, "dir parent", first["parent"], "ar-metamusic")
	// plays and ratings surface on the album
	a := list(t, r.call("getAlbumList2", "type", "frequent", "size", "1"), "albumList2", "album")[0]
	eq(t, "playCount", a["playCount"], 2)
	eq(t, "userRating", a["userRating"], 3)
	eq(t, "year", a["year"], 2020)
}

func TestSubsonic_RandomDeterministicWithRand(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	var n atomic.Uint64
	r.srv.rand = func() uint64 { n.Add(1); return 0 } // Fisher-Yates with j = 0 every time
	// albums by id: 101, 102, 103, 105 => [102, 103, 105, 101]
	want := []string{al(citta), al(twinA), al(short), al(luna)}
	for i := 0; i < 2; i++ {
		d := r.call("getAlbumList2", "type", "random", "size", "10")
		if got := ids(list(t, d, "albumList2", "album")); !slices.Equal(got, want) {
			t.Fatalf("random albums (run %d) = %v, want %v", i, got, want)
		}
	}
	// songs by id: 101..105 => i=4: [105,102,103,104,101]; i=3: [104,102,103,105,101]; i=2: [103,102,104,105,101]; i=1: [102,103,104,105,101]
	d := r.call("getRandomSongs", "size", "3")
	if got := ids(list(t, d, "randomSongs", "song")); !slices.Equal(got, []string{citta, twinA, twinB}) {
		t.Errorf("random songs = %v", got)
	}
	// filters apply before the shuffle
	d = r.call("getRandomSongs", "genre", "ambient")
	got := ids(list(t, d, "randomSongs", "song"))
	slices.Sort(got)
	if !slices.Equal(got, []string{citta, short}) {
		t.Errorf("random ambient songs = %v", got)
	}
	// the default source is not constant
	r2 := newLibRig(t)
	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		d := r2.call("getRandomSongs", "size", "1")
		seen[ids(list(t, d, "randomSongs", "song"))[0]] = true
	}
	if len(seen) < 2 {
		t.Errorf("default Rand returned one song 40 times: %v", seen)
	}
}

func TestSubsonic_Genres(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	genres := func() [][3]any {
		var out [][3]any
		for _, g := range list(t, r.call("getGenres"), "genres", "genre") {
			out = append(out, [3]any{g["value"], g["songCount"], g["albumCount"]})
		}
		return out
	}
	g := genres()
	if len(g) < 4 {
		t.Fatalf("genres = %v", g)
	}
	// by songCount desc, then term: ambient (Città, Cut Short), guitar and rock (Twin A and B, one album)
	wantHead := [][3]any{{"ambient", float64(2), float64(2)}, {"guitar", float64(2), float64(1)}, {"rock", float64(2), float64(1)}}
	if fmt.Sprint(g[:3]) != fmt.Sprint(wantHead) {
		t.Errorf("genres head = %v, want %v", g[:3], wantHead)
	}
	for _, e := range g {
		if e[0] == "120 bpm" || e[0] == "140 bpm" {
			t.Errorf("bpm is not a genre: %v", e)
		}
	}
	// a discarded song leaves the counts
	r.setStatus(short, "discarded", r.clk.now())
	if g := genres(); fmt.Sprint(g[0]) != fmt.Sprint([3]any{"guitar", float64(2), float64(1)}) {
		t.Errorf("genres after discard = %v", g)
	}
	// songs by genre
	d := r.call("getSongsByGenre", "genre", "ROCK", "count", "1", "offset", "1")
	eq(t, "songsByGenre", ids(list(t, d, "songsByGenre", "song")), []string{twinB})
	if c := r.callCode("getSongsByGenre"); c != 10 {
		t.Errorf("getSongsByGenre without genre => %d", c)
	}
}

func TestSubsonic_SimilarSongs(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	eq(t, "song id", ids(list(t, r.call("getSimilarSongs", "id", twinB), "similarSongs", "song")), []string{twinA})
	eq(t, "song id 2", ids(list(t, r.call("getSimilarSongs2", "id", twinA), "similarSongs2", "song")), []string{twinB})
	eq(t, "album id", ids(list(t, r.call("getSimilarSongs", "id", al(twinA)), "similarSongs", "song")), []string{twinA, twinB})
	eq(t, "count", ids(list(t, r.call("getSimilarSongs2", "id", al(twinA), "count", "1"), "similarSongs2", "song")), []string{twinA})
	eq(t, "lonely", len(list(t, r.call("getSimilarSongs", "id", luna), "similarSongs", "song")), 0)
	if c := r.callCode("getSimilarSongs", "id", "no-such"); c != 70 {
		t.Errorf("unknown => %d", c)
	}
}

func TestSubsonic_IndexesIfModifiedSince(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	d := r.call("getIndexes")
	last := int64(dig(t, d, "indexes", "lastModified").(float64))
	if want := r.clk.now().Add(4 * time.Minute).UnixMilli(); last != want {
		t.Fatalf("lastModified = %d, want %d", last, want)
	}
	eq(t, "ignoredArticles", dig(t, d, "indexes", "ignoredArticles"), "")
	idx := list(t, d, "indexes", "index")
	var names []string
	for _, i := range idx {
		names = append(names, i["name"].(string))
	}
	eq(t, "index names", names, []string{"C", "L", "T"})
	cs := list(t, map[string]any{"i": idx[0]}, "i", "artist")
	eq(t, "C entries", len(cs), 2)
	eq(t, "first C", cs[0]["name"], "Città lenta")
	eq(t, "first C id", cs[0]["id"], al(citta))
	// ifModifiedSince at or after lastModified => no entries; before => entries
	for since, wantEntries := range map[int64]bool{last: false, last + 1: false, last - 1: true, 0: true} {
		d := r.call("getIndexes", "ifModifiedSince", fmt.Sprint(since))
		_, has := dig(t, d, "indexes").(map[string]any)["index"]
		if has != wantEntries {
			t.Errorf("ifModifiedSince=%d: entries %v, want %v", since, has, wantEntries)
		}
		eq(t, "lastModified echo", int64(dig(t, d, "indexes", "lastModified").(float64)), last)
	}
	// a status change moves lastModified
	r.setStatus(luna, "final", r.clk.now().Add(time.Hour))
	d = r.call("getIndexes")
	if got := int64(dig(t, d, "indexes", "lastModified").(float64)); got != r.clk.now().Add(time.Hour).UnixMilli() {
		t.Errorf("lastModified after status change = %d", got)
	}
	// symbols and digits group under '#'
	r.exec("UPDATE tracks SET title = '9 Lives' WHERE id = ?", luna)
	d = r.call("getIndexes")
	if first := list(t, d, "indexes", "index")[0]["name"]; first != "#" {
		t.Errorf("first index = %v", first)
	}
}

func TestSubsonic_StubsShape(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	eq(t, "topSongs", dig(t, r.call("getTopSongs", "artist", "x"), "topSongs"), map[string]any{"song": []any{}})
	eq(t, "nowPlaying", dig(t, r.call("getNowPlaying"), "nowPlaying"), map[string]any{"entry": []any{}})
	eq(t, "scanStatus", dig(t, r.call("getScanStatus"), "scanStatus"), map[string]any{"scanning": false, "count": float64(5)})
	for _, ep := range []struct{ name, key string }{{"getArtistInfo", "artistInfo"}, {"getArtistInfo2", "artistInfo2"}, {"getAlbumInfo", "albumInfo"}, {"getAlbumInfo2", "albumInfo"}} {
		for _, id := range []string{"ar-metamusic", al(luna), luna} {
			eq(t, ep.name+" "+id, dig(t, r.call(ep.name, "id", id), ep.key), map[string]any{})
		}
		if c := r.callCode(ep.name, "id", "no-such"); c != 70 {
			t.Errorf("%s unknown => %d", ep.name, c)
		}
		if c := r.callCode(ep.name); c != 10 {
			t.Errorf("%s without id => %d", ep.name, c)
		}
	}
	// XML twins
	q := keyParams(r.rwKey, "id", luna)
	q.Del("f")
	body := r.get("getTopSongs", q, libIP).Body.String()
	if !strings.Contains(body, "<topSongs></topSongs>") && !strings.Contains(body, "<topSongs/>") {
		t.Errorf("topSongs XML = %s", body)
	}
	body = r.get("getScanStatus", q, libIP).Body.String()
	if !strings.Contains(body, `<scanStatus scanning="false" count="5">`) {
		t.Errorf("scanStatus XML = %s", body)
	}
}

func TestSubsonic_UnknownIdsAre70(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	for _, c := range []struct{ ep, id string }{
		{"getSong", "00000000-0000-7000-8000-00000000dead"}, {"getSong", al(luna)}, {"getAlbum", al("nope")}, {"getAlbum", luna},
		{"getMusicDirectory", al("nope")}, {"getMusicDirectory", luna}, {"getArtist", "ar-other"}, {"getArtist", luna},
		{"getSimilarSongs", al("nope")}, {"getAlbum", "al-"}, {"getArtistInfo", "ar-"}, {"getSimilarSongs2", "tr-" + luna},
	} {
		if got := r.callCode(c.ep, "id", c.id); got != 70 {
			t.Errorf("%s %s => %d", c.ep, c.id, got)
		}
	}
	for _, ep := range []string{"getSong", "getAlbum", "getMusicDirectory", "getArtist", "getSimilarSongs"} {
		if got := r.callCode(ep); got != 10 {
			t.Errorf("%s without id => %d", ep, got)
		}
	}
}

func TestSubsonic_ChildShape(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	s := dig(t, r.call("getSong", "id", luna), "song").(map[string]any)
	want := map[string]any{
		"id": luna, "parent": al(luna), "isDir": false, "title": "Luna Park", "album": "Luna Park", "artist": "metamusic", "track": float64(1),
		"genre": "dream pop", "coverArt": "tr-" + luna, "contentType": "audio/flac", "suffix": "flac", "duration": float64(12),
		"bitDepth": float64(24), "samplingRate": float64(48000), "channelCount": float64(2), "albumId": al(luna), "artistId": "ar-metamusic",
		"type": "music", "mediaType": "song", "bpm": float64(120), "displayArtist": "metamusic", "displayAlbumArtist": "metamusic",
		"musicBrainzId": "", "displayComposer": "", "explicitStatus": "",
		"path": "luna_park/luna_park__101.flac", "sortName": "luna park",
	}
	for k, v := range want {
		if fmt.Sprint(s[k]) != fmt.Sprint(v) {
			t.Errorf("%s = %v, want %v", k, s[k], v)
		}
	}
	for _, k := range []string{"size", "created", "year", "comment"} {
		if s[k] == nil {
			t.Errorf("%s missing", k)
		}
	}
	for _, k := range []string{"starred", "userRating", "played", "transcodedSuffix", "transcodedContentType"} {
		if _, has := s[k]; has {
			t.Errorf("%s present on an unrated, unplayed draft: %v", k, s[k])
		}
	}
	for _, k := range []string{"isrc", "moods", "contributors"} {
		if v, ok := s[k].([]any); !ok || len(v) != 0 {
			t.Errorf("%s = %v, want []", k, s[k])
		}
	}
	eq(t, "genres", s["genres"], []any{map[string]any{"name": "dream pop"}})
	eq(t, "artists", s["artists"], []any{map[string]any{"id": "ar-metamusic", "name": "metamusic"}})
	rg := s["replayGain"].(map[string]any)
	eq(t, "trackGain", rg["trackGain"], -4.0)
	if p, _ := rg["trackPeak"].(float64); p < 0.86 || p > 0.88 {
		t.Errorf("trackPeak = %v", rg["trackPeak"])
	}
	// rated, played and final songs carry the fields
	r.rate(luna, 4)
	r.setStatus(luna, "final", r.clk.now().Add(time.Hour))
	r.listen(luna, r.clk.now().Add(2*time.Hour))
	s = dig(t, r.call("getSong", "id", luna), "song").(map[string]any)
	eq(t, "userRating", s["userRating"], 4)
	eq(t, "playCount", s["playCount"], 1)
	eq(t, "starred", s["starred"], r.clk.now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"))
	eq(t, "played", s["played"], r.clk.now().Add(2*time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"))
	// the style comment is cut to 200 runes
	r.exec("DROP TRIGGER recipes_immutable")
	r.exec("UPDATE recipes SET style = ? WHERE id = (SELECT recipe_id FROM tracks WHERE id = ?)", strings.Repeat("é", 300), citta)
	r.exec("UPDATE recipes SET request_json = json_set(request_json, '$.style', ?) WHERE id = (SELECT recipe_id FROM tracks WHERE id = ?)", strings.Repeat("é", 300), citta)
	if c, _ := dig(t, r.call("getSong", "id", citta), "song", "comment").(string); len([]rune(c)) != 200 {
		t.Errorf("comment length = %d", len([]rune(c)))
	}
}

func TestSubsonic_DefaultFormatMP3(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	human := auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: []auth.Scope{auth.ScopeAdmin}}
	_, sec, err := r.creds.Create(context.Background(), human, auth.NewSubsonicClient{Name: "MP3 phone", Username: "mp3phone", DefaultFormat: "mp3", APIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	var mp3Bytes, masterBytes int64
	if err := r.st.R.QueryRow(`SELECT bytes FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, luna).Scan(&mp3Bytes); err != nil {
		t.Fatal(err)
	}
	if err := r.st.R.QueryRow(`SELECT master_bytes FROM tracks WHERE id = ?`, luna).Scan(&masterBytes); err != nil {
		t.Fatal(err)
	}
	get := func(key string) map[string]any {
		return dig(t, r.get("getSong", keyParams(key, "id", luna), libIP).doc(t), "song").(map[string]any)
	}
	raw := get(r.rwKey)
	mp3 := get(sec.APIKey)
	eq(t, "raw size", raw["size"], masterBytes)
	eq(t, "mp3 size", mp3["size"], mp3Bytes)
	eq(t, "mp3 transcodedSuffix", mp3["transcodedSuffix"], "mp3")
	eq(t, "mp3 transcodedContentType", mp3["transcodedContentType"], "audio/mpeg")
	eq(t, "mp3 bitRate", mp3["bitRate"], 320)
	eq(t, "mp3 stored suffix", mp3["suffix"], "flac")
	if _, has := raw["transcodedSuffix"]; has {
		t.Errorf("raw client sees transcodedSuffix")
	}
}

func TestSubsonic_LossyChild(t *testing.T) {
	t.Parallel()
	s := songRow{ID: "t1", Root: "t1", Title: "Ref", Slug: "ref", Status: "draft", StatusSetAt: "2026-10-15T10:00:00Z", CreatedAt: "2026-10-15T10:00:00Z",
		RecordedAt: "2026-10-14T10:00:00Z", DurationS: 100, SampleRate: 44100, BitsPerSample: 16, Channels: 2, MasterBytes: 4_000_000, Lossy: true,
		AlbumTitle: "Ref", AlbumSlug: "ref", Pos: 1}
	for _, mp3Default := range []bool{false, true} {
		ch := toChild(s, mp3Default)
		eq(t, "contentType", ch.ContentType, "audio/mpeg")
		eq(t, "suffix", ch.Suffix, "mp3")
		eq(t, "size", ch.Size, 4_000_000)
		eq(t, "bitRate", ch.BitRate, 320)
		eq(t, "bitDepth", ch.BitDepth, 0)
		eq(t, "transcoded", ch.TranscodedSuffix+ch.TranscodedContentType, "")
		eq(t, "path", ch.Path, "ref/ref__0.mp3")
	}
}

func TestSubsonic_BrowseArtists(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	d := r.call("getArtists")
	idx := list(t, d, "artists", "index")
	eq(t, "index name", idx[0]["name"], "M")
	a := list(t, map[string]any{"i": idx[0]}, "i", "artist")[0]
	eq(t, "artist", []any{a["id"], a["name"], a["albumCount"], a["coverArt"]}, []any{"ar-metamusic", "metamusic", 4, al(short)})
	d = r.call("getArtist", "id", "ar-metamusic")
	eq(t, "albums", ids(list(t, d, "artist", "album")), []string{al(citta), al(short), al(luna), al(twinA)})
	eq(t, "albumCount", dig(t, d, "artist", "albumCount"), 4)
	// getMusicDirectory: an album, and the artist directory
	d = r.call("getMusicDirectory", "id", al(twinA))
	eq(t, "dir name", dig(t, d, "directory", "name"), "Twin A")
	eq(t, "dir children", ids(list(t, d, "directory", "child")), []string{twinA, twinB})
	d = r.call("getMusicDirectory", "id", "ar-metamusic")
	eq(t, "artist dir children", len(list(t, d, "directory", "child")), 4)
	// getAlbum duration is the sum, rounded
	d = r.call("getAlbum", "id", al(twinA))
	eq(t, "duration", dig(t, d, "album", "duration"), 16)
	eq(t, "artistId", dig(t, d, "album", "artistId"), "ar-metamusic")
	eq(t, "coverArt", dig(t, d, "album", "coverArt"), al(twinA))
}

func TestSubsonic_CatalogXMLShape(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	q := keyParams(r.rwKey, "id", al(twinA))
	q.Del("f")
	body := r.get("getAlbum", q, libIP).Body.String()
	for _, want := range []string{`<album id="al-` + twinA + `" name="Twin A"`, `<song id="` + twinA + `" parent="al-` + twinA + `" isDir="false" title="Twin A"`,
		`<genres name="rock"></genres>`, `<artists id="ar-metamusic" name="metamusic"></artists>`, `<replayGain trackGain=`} {
		if !strings.Contains(body, want) {
			t.Errorf("album XML lacks %q:\n%s", want, body)
		}
	}
	q = keyParams(r.rwKey)
	q.Del("f")
	body = r.get("getGenres", q, libIP).Body.String()
	if !strings.Contains(body, `<genre songCount="2" albumCount="2">ambient</genre>`) {
		t.Errorf("genres XML = %s", body)
	}
}

func TestSubsonic_AlbumListSizeZeroAndByYear(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	l, _ := dig(t, r.call("getAlbumList2", "type", "newest", "size", "0"), "albumList2", "album").([]any)
	eq(t, "size 0", len(l), 0)
	eq(t, "byYear without years", r.callCode("getAlbumList2", "type", "byYear"), 10)
	eq(t, "byYear one year", r.callCode("getAlbumList2", "type", "byYear", "fromYear", "2020"), 10)
	eq(t, "byYear ok", r.callCode("getAlbumList2", "type", "byYear", "fromYear", "2020", "toYear", "2030"), 0)
}

func TestSubsonic_EmptyProbeEndpoints(t *testing.T) {
	t.Parallel()
	r := newLibRig(t)
	for _, n := range []string{"getPodcasts", "getNewestPodcasts", "getInternetRadioStations", "getShares", "getVideos", "getChatMessages", "startScan"} {
		eq(t, n, r.callCode(n), 0)
	}
}
