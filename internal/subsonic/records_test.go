package subsonic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// The Record albums of L2 (C126.7, D-L2-35) over the FixtureL2 world: R1 "Synthetic Record One" is published with five slots
// of which four are listable (a1, b1, the pinned c2, e1: slot D is an empty slot), R2 is an agent's draft.

var recHuman = auth.Principal{Kind: auth.KindHuman, ID: "op", Name: "op", Via: "local"}

type recEnv struct {
	t *testing.T
	r *servertest.Rig
	c *subsonictest.Client
}

func newRecEnv(t *testing.T) *recEnv {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true, M4a: true, Files: true})
	c := r.Subsonic(t, subsonictest.ClientOpts{Name: "Record Client", Username: "recclient", Write: true})
	return &recEnv{t: t, r: r, c: c}
}

// call answers the JSON reply of one request.
func (e *recEnv) call(endpoint string, kv ...string) *subsonictest.Response {
	e.t.Helper()
	return e.c.Call(e.t, endpoint, qv(kv...), subsonictest.CallOpts{JSON: true})
}

func (e *recEnv) ok(endpoint string, kv ...string) map[string]any {
	e.t.Helper()
	resp := e.call(endpoint, kv...)
	if resp.HTTP != 200 || resp.Status != "ok" {
		e.t.Fatalf("%s %v: http %d status %q code %d: %.300s", endpoint, kv, resp.HTTP, resp.Status, resp.Code, resp.Body)
	}
	return resp.Doc
}

func (e *recEnv) code(endpoint string, kv ...string) int {
	e.t.Helper()
	return e.call(endpoint, kv...).Code
}

func (e *recEnv) sql(q string, args ...any) {
	e.t.Helper()
	if _, err := e.r.Store.W.ExecContext(context.Background(), q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *recEnv) one(q string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.r.Store.R.QueryRowContext(context.Background(), q, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return s
}

func (e *recEnv) record(title string) string {
	return e.one(`SELECT id FROM records WHERE title = ?`, title)
}

func (e *recEnv) publish(id string, on bool) {
	e.t.Helper()
	rev := 0
	if err := e.r.Store.R.QueryRowContext(context.Background(), `SELECT revision FROM records WHERE id = ?`, id).Scan(&rev); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.r.Services.Records.Publish(context.Background(), recHuman, id, rev, on); err != nil {
		e.t.Fatalf("publish %v: %v", on, err)
	}
}

func list(d map[string]any, key string) []map[string]any {
	box, _ := d[key].(map[string]any)
	var out []map[string]any
	for _, v := range box[map[string]string{"albumList2": "album", "albumList": "album"}[key]].([]any) {
		out = append(out, v.(map[string]any))
	}
	return out
}

func ids(rows []map[string]any) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i], _ = r["id"].(string)
	}
	return out
}

func (e *recEnv) albumIDs(typ string, kv ...string) []string {
	e.t.Helper()
	return ids(list(e.ok("getAlbumList2", append([]string{"type", typ, "size", "500"}, kv...)...), "albumList2"))
}

func songsOf(d map[string]any, key string) []map[string]any {
	box := d[key].(map[string]any)
	var out []map[string]any
	for _, v := range box["song"].([]any) {
		out = append(out, v.(map[string]any))
	}
	return out
}

func TestSubsonic_RecordAlbum_List(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	r1, r2 := "rec-"+e.record("Synthetic Record One"), "rec-"+e.record("Synthetic Record Two")
	lineage := e.one(`SELECT 'al-' || id FROM tracks WHERE id = root_track_id AND title = 'Synthetic Take A1'`)
	for _, typ := range []string{"newest", "alphabeticalByName", "alphabeticalByArtist", "highest", "frequent", "recent", "starred", "random"} {
		got := e.albumIDs(typ)
		switch typ {
		case "highest", "frequent", "recent", "starred":
			continue // filtered or ordered by listening facts the fixture does not have; covered below
		}
		if !slices.Contains(got, r1) || !slices.Contains(got, lineage) {
			t.Errorf("%s: %v lacks the published Record or a lineage album", typ, got)
		}
		if slices.Contains(got, r2) {
			t.Errorf("%s lists the draft Record", typ)
		}
	}
	// byYear uses the year of first_published_at (the rig clock)
	year := e.one(`SELECT substr(first_published_at, 1, 4) FROM records WHERE title = 'Synthetic Record One'`)
	if got := e.albumIDs("byYear", "fromYear", year, "toYear", year); !slices.Contains(got, r1) {
		t.Errorf("byYear %s: %v", year, got)
	}
	if got := e.albumIDs("byYear", "fromYear", "1999", "toYear", "2000"); slices.Contains(got, r1) {
		t.Errorf("byYear 1999-2000 lists the Record: %v", got)
	}
	// newest orders by created: the Record's first_published_at against the lineage albums' created
	newest := e.albumIDs("newest")
	recAt := e.one(`SELECT first_published_at FROM records WHERE title = 'Synthetic Record One'`)
	pos := slices.Index(newest, r1)
	for i, id := range newest {
		if !strings.HasPrefix(id, "al-") {
			continue
		}
		created := e.one(`SELECT created_at FROM tracks WHERE id = ?`, strings.TrimPrefix(id, "al-"))
		if (i < pos) != (created > recAt || created == recAt && id < r1) {
			t.Errorf("newest: %s (created %s) sits at %d, the Record (first published %s) at %d", id, created, i, recAt, pos)
		}
	}
	// paging cuts the merged list: offset and size agree with the full list
	full := e.albumIDs("alphabeticalByName")
	if got := ids(list(e.ok("getAlbumList2", "type", "alphabeticalByName", "size", "2", "offset", "1"), "albumList2")); !slices.Equal(got, full[1:3]) {
		t.Errorf("page = %v, want %v", got, full[1:3])
	}
	// getAlbumList (ID3-less) lists the Record as a directory child
	old := ids(list(e.ok("getAlbumList", "type", "alphabeticalByName", "size", "500"), "albumList"))
	if !slices.Equal(old, full) {
		t.Errorf("getAlbumList = %v, getAlbumList2 = %v", old, full)
	}
	// the folder rule applies to the Takes: folder 2 (drafts) holds only drafts, so the Record's four Takes (all drafts) stay
	// and folder 1 (final or to_relisten) drops those that are not
	if got := e.albumIDs("alphabeticalByName", "musicFolderId", "3"); slices.Contains(got, r1) {
		t.Errorf("the Discarded folder lists the Record: %v", got)
	}
	// unpublishing removes it at once
	e.publish(e.record("Synthetic Record One"), false)
	for _, typ := range []string{"newest", "alphabeticalByName", "random"} {
		if got := e.albumIDs(typ); slices.Contains(got, r1) {
			t.Errorf("%s still lists the unpublished Record", typ)
		}
	}
}

func TestSubsonic_RecordAlbum_ListAggregates(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	r1 := "rec-" + e.record("Synthetic Record One")
	a1 := e.one(`SELECT id FROM tracks WHERE title = 'Synthetic Take A1'`)
	e.sql(`INSERT INTO ratings(track_id, source, stars, principal_kind, principal_id, principal_name, created_at, updated_at)
		VALUES (?, 'human', 5, 'human', 'human:op', 'op', '2026-10-15T10:00:00Z', '2026-10-15T10:00:00Z')`, a1)
	e.sql(`INSERT INTO listen_events(id, track_id, session_id, kind, from_ms, source, principal_kind, principal_id, client_at, received_at)
		VALUES ('00000000-0000-4000-8000-000000000501', ?, 'session-12345678', 'start', 0, 'web', 'device', 'device:d', '2999-01-01T00:00:00Z', '2999-01-01T00:00:00Z')`, a1)
	// the Record's Takes hold the newest listen and a 5-star rating: the aggregates are those of its Takes (the same
	// expressions as the lineage albums)
	if got := e.albumIDs("recent"); !slices.Contains(got, r1) {
		t.Errorf("recent = %v, want the Record", got)
	}
	if got := e.albumIDs("frequent"); !slices.Contains(got, r1) {
		t.Errorf("frequent = %v, want the Record", got)
	}
	alb := e.ok("getAlbum", "id", r1)["album"].(map[string]any)
	lin := e.ok("getAlbum", "id", "al-"+e.one(`SELECT root_track_id FROM tracks WHERE id = ?`, a1))["album"].(map[string]any)
	if alb["userRating"].(float64) != 5 || alb["playCount"].(float64) < 1 || alb["played"] != "2999-01-01T00:00:00.000Z" {
		t.Errorf("aggregates: playCount %v userRating %v played %v", alb["playCount"], alb["userRating"], alb["played"])
	}
	if lin["played"] != alb["played"] {
		t.Errorf("the lineage of the listened Take has played %v, the Record %v", lin["played"], alb["played"])
	}
	// the merged list follows the SQL order of the lineage albums: highest = stars desc
	got := e.albumIDs("highest")
	stars := func(id string) int {
		d := e.ok("getAlbum", "id", id)["album"].(map[string]any)
		n, _ := d["userRating"].(float64)
		return int(n)
	}
	for i := 1; i < len(got); i++ {
		if stars(got[i-1]) < stars(got[i]) {
			t.Fatalf("highest is not ordered by stars at %d (%s=%d, %s=%d): %v", i, got[i-1], stars(got[i-1]), got[i], stars(got[i]), got)
		}
	}
	// starred: a Take marked final puts its albums in the starred list
	e.sql(`UPDATE tracks SET status = 'final', status_set_at = '2026-10-15T11:00:00Z' WHERE id = ?`, a1)
	if got := e.albumIDs("starred"); !slices.Contains(got, r1) {
		t.Errorf("starred = %v, want the Record", got)
	}
}

func TestSubsonic_RecordAlbum_Get(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	rec := e.record("Synthetic Record One")
	r1 := "rec-" + rec
	// two sides: slot B is on side B, slot E on side D
	e.sql(`UPDATE record_slots SET side = 'B' WHERE record_id = ? AND song_id = (SELECT id FROM songs WHERE title = 'Synthetic Song S3')`, rec)
	e.sql(`UPDATE record_slots SET side = 'D' WHERE record_id = ? AND song_id = (SELECT id FROM songs WHERE title = 'Synthetic Song S5')`, rec)
	want := []string{"Synthetic Take A1", "Synthetic Take B1", "Synthetic Take C2", "Synthetic Take E1"}
	check := func(endpoint string, songs []map[string]any) {
		t.Helper()
		if len(songs) != len(want) {
			t.Fatalf("%s: %d songs, want %d", endpoint, len(songs), len(want))
		}
		for i, s := range songs {
			if s["title"] != want[i] {
				t.Errorf("%s[%d] = %v, want %s", endpoint, i, s["title"], want[i])
			}
			if s["albumId"] != r1 || s["parent"] != r1 || s["album"] != "Synthetic Record One" {
				t.Errorf("%s[%d]: albumId %v parent %v album %v", endpoint, i, s["albumId"], s["parent"], s["album"])
			}
			if int(s["track"].(float64)) != i+1 {
				t.Errorf("%s[%d]: track %v", endpoint, i, s["track"])
			}
			disc := []float64{1, 2, 1, 4}[i]
			if s["discNumber"].(float64) != disc {
				t.Errorf("%s[%d]: discNumber %v, want %v", endpoint, i, s["discNumber"], disc)
			}
		}
	}
	d := e.ok("getAlbum", "id", r1)
	alb := d["album"].(map[string]any)
	if alb["id"] != r1 || alb["name"] != "Synthetic Record One" || alb["songCount"].(float64) != 4 || alb["coverArt"] != r1 || alb["artist"] != "metamusic" {
		t.Errorf("album = %v", alb)
	}
	check("getAlbum", songsOf(d, "album"))
	md := e.ok("getMusicDirectory", "id", r1)["directory"].(map[string]any)
	var kids []map[string]any
	for _, v := range md["child"].([]any) {
		kids = append(kids, v.(map[string]any))
	}
	check("getMusicDirectory", kids)
	if md["id"] != r1 || md["parent"] != "ar-metamusic" {
		t.Errorf("directory = %v", md)
	}
	e.ok("getAlbumInfo2", "id", r1)
	e.ok("getAlbumInfo", "id", r1)
	// a Song of the Record keeps its lineage ids through getSong (M4a is unchanged)
	song := e.ok("getSong", "id", kids[0]["id"].(string))["song"].(map[string]any)
	if !strings.HasPrefix(song["albumId"].(string), "al-") || !strings.HasPrefix(song["parent"].(string), "al-") {
		t.Errorf("getSong albumId %v parent %v: lineage ids must not change", song["albumId"], song["parent"])
	}
	if song["discNumber"] != nil || song["replayGain"].(map[string]any)["albumGain"] != nil {
		t.Errorf("getSong carries Record fields: %v", song)
	}
	// the folder rule filters the resolved Takes: folder 1 holds neither of the draft Takes
	if code := e.code("getAlbum", "id", r1, "musicFolderId", "1"); code != 70 {
		t.Errorf("getAlbum in a folder without any of its Takes = %d, want 70", code)
	}
	e.sql(`UPDATE tracks SET status = 'final', status_set_at = '2026-10-15T11:00:00Z' WHERE title = 'Synthetic Take B1'`)
	d = e.ok("getAlbum", "id", r1, "musicFolderId", "1")
	if s := songsOf(d, "album"); len(s) != 1 || s[0]["title"] != "Synthetic Take B1" || s[0]["track"].(float64) != 2 {
		t.Errorf("folder 1 = %v", s)
	}
	// a Take discarded after the fact leaves the album
	e.sql(`UPDATE tracks SET status = 'discarded' WHERE title = 'Synthetic Take E1'`)
	if n := len(songsOf(e.ok("getAlbum", "id", r1), "album")); n != 3 {
		t.Errorf("after discarding one Take: %d songs, want 3", n)
	}
	// ids that name nothing
	for _, id := range []string{"rec-nope", "rec-" + e.record("Synthetic Record Two")} {
		for _, ep := range []string{"getAlbum", "getMusicDirectory", "getAlbumInfo2", "getCoverArt"} {
			if code := e.code(ep, "id", id); code != 70 {
				t.Errorf("%s %s = %d, want 70", ep, id, code)
			}
		}
	}
	if code := e.code("getAlbum", "id", "rec-"); code != 70 {
		t.Errorf("getAlbum rec- = %d", code)
	}
	// a hidden Record is gone, too
	e.sql(`UPDATE records SET hidden_at = '2026-10-15T12:00:00Z', hidden_by = 'human:op' WHERE id = ?`, rec)
	if code := e.code("getAlbum", "id", r1); code != 70 {
		t.Errorf("hidden: getAlbum = %d, want 70", code)
	}
}

func TestSubsonic_RecordAlbum_Browse(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	r1 := "rec-" + e.record("Synthetic Record One")
	idx := e.ok("getIndexes")["indexes"].(map[string]any)
	var found bool
	for _, g := range idx["index"].([]any) {
		for _, a := range g.(map[string]any)["artist"].([]any) {
			if a.(map[string]any)["id"] == r1 {
				found = true
				if g.(map[string]any)["name"] != "S" {
					t.Errorf("Record indexed under %v", g.(map[string]any)["name"])
				}
			}
		}
	}
	if !found {
		t.Error("getIndexes does not list the Record")
	}
	artist := e.ok("getArtist", "id", "ar-metamusic")["artist"].(map[string]any)
	albs := artist["album"].([]any)
	var in bool
	for _, a := range albs {
		in = in || a.(map[string]any)["id"] == r1
	}
	if !in || int(artist["albumCount"].(float64)) != len(albs) {
		t.Errorf("getArtist: in=%v albumCount=%v albums=%d", in, artist["albumCount"], len(albs))
	}
	arts := e.ok("getArtists")["artists"].(map[string]any)["index"].([]any)[0].(map[string]any)["artist"].([]any)[0].(map[string]any)
	if int(arts["albumCount"].(float64)) != len(albs) {
		t.Errorf("getArtists albumCount = %v, want %d", arts["albumCount"], len(albs))
	}
	dir := e.ok("getMusicDirectory", "id", "ar-metamusic")["directory"].(map[string]any)["child"].([]any)
	var dirIDs []string
	for _, c := range dir {
		dirIDs = append(dirIDs, c.(map[string]any)["id"].(string))
	}
	if !slices.Contains(dirIDs, r1) || len(dirIDs) != len(albs) {
		t.Errorf("artist directory = %v", dirIDs)
	}
	// search: the title matches as a plain substring
	for _, ep := range []string{"search2", "search3"} {
		box := strings.Replace(ep, "search", "searchResult", 1)
		d := e.ok(ep, "query", "RECORD one", "albumCount", "10")[box].(map[string]any)
		al, _ := d["album"].([]any)
		if len(al) != 1 || al[0].(map[string]any)["id"] != r1 {
			t.Errorf("%s: albums %v", ep, al)
		}
		d = e.ok(ep, "query", "record", "albumCount", "10")[box].(map[string]any)
		if al, _ = d["album"].([]any); len(al) != 1 {
			t.Errorf("%s record: albums %v (the draft must not match)", ep, al)
		}
		d = e.ok(ep, "query", "", "albumCount", "50")[box].(map[string]any)
		var all []string
		for _, a := range d["album"].([]any) {
			all = append(all, a.(map[string]any)["id"].(string))
		}
		if !slices.Contains(all, r1) {
			t.Errorf("%s (empty query): %v", ep, all)
		}
	}
	// the album offset cuts the merged list
	d := e.ok("search3", "query", "", "albumCount", "1", "albumOffset", "1")["searchResult3"].(map[string]any)
	if n := len(d["album"].([]any)); n != 1 {
		t.Errorf("album page = %d", n)
	}
}

func TestSubsonic_RecordAlbum_Cover(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	rec := e.record("Synthetic Record One")
	r1 := "rec-" + rec
	get := func(size string) *subsonictest.Response {
		kv := []string{"id", r1}
		if size != "" {
			kv = append(kv, "size", size)
		}
		return e.c.Call(t, "getCoverArt", qv(kv...), subsonictest.CallOpts{})
	}
	resp := get("128")
	if resp.HTTP != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/png") {
		t.Fatalf("cover: http %d type %q: %.200s", resp.HTTP, resp.Header.Get("Content-Type"), resp.Body)
	}
	img, err := png.Decode(bytes.NewReader(resp.Body))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 128 || b.Dy() != 128 {
		t.Errorf("cover is %dx%d, want 128x128", b.Dx(), b.Dy())
	}
	// the size is clamped: a huge request is served at the cover service's maximum
	big := get("100000")
	if big.HTTP != 200 {
		t.Fatalf("huge size: http %d", big.HTTP)
	}
	bi, err := png.Decode(bytes.NewReader(big.Body))
	if err != nil {
		t.Fatal(err)
	}
	if bi.Bounds().Dx() != 1024 {
		t.Errorf("clamped cover is %d px wide", bi.Bounds().Dx())
	}
	// an album's coverArt id is its album id
	if a := e.ok("getAlbum", "id", r1)["album"].(map[string]any); a["coverArt"] != r1 {
		t.Errorf("coverArt = %v", a["coverArt"])
	}
	// unknown, draft and unpublished ids are error 70
	if code := e.call("getCoverArt", "id", "rec-nope").Code; code != 70 {
		t.Errorf("unknown = %d", code)
	}
	e.publish(rec, false)
	if resp := get("128"); resp.Code != 70 {
		t.Errorf("unpublished: http %d code %d", resp.HTTP, resp.Code)
	}
}

func TestSubsonic_RecordAlbum_ReplayGain(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	r1 := "rec-" + e.record("Synthetic Record One")
	// FixtureL2 loudness (integrated LUFS / true peak): a1 -15.5/-2.0, b1 -13.0/-1.5, c2 -11.8/+0.6, e1 -14.0/-3.0. The median
	// of the four is -13.5, clamped to the target T = -14.0, so albumGain = -18.0 - T = -4.0 and trackGain = gain + albumGain:
	// a1 would need +1.5 but its true peak limits it to +1.0 (-3.0), b1 -1.0 (-5.0), c2 -2.2 (-6.2), e1 0 (-4.0).
	want := map[string][2]float64{
		"Synthetic Take A1": {-4.0, -3.0}, "Synthetic Take B1": {-4.0, -5.0},
		"Synthetic Take C2": {-4.0, -6.2}, "Synthetic Take E1": {-4.0, -4.0},
	}
	d := e.ok("getAlbum", "id", r1)
	for _, s := range songsOf(d, "album") {
		rg := s["replayGain"].(map[string]any)
		w := want[s["title"].(string)]
		if g := rg["albumGain"].(float64); g != w[0] {
			t.Errorf("%v albumGain = %v, want %v", s["title"], g, w[0])
		}
		if g := rg["trackGain"].(float64); fmt.Sprintf("%.2f", g) != fmt.Sprintf("%.2f", w[1]) {
			t.Errorf("%v trackGain = %v, want %.2f", s["title"], g, w[1])
		}
	}
	// a Take without a measurement has no trackGain, the others keep theirs
	e.sql(`DROP TRIGGER analyses_no_delete`)
	e.sql(`DELETE FROM analyses WHERE analyzer = 'loudness' AND track_id = (SELECT id FROM tracks WHERE title = 'Synthetic Take A1')`)
	for _, s := range songsOf(e.ok("getAlbum", "id", r1), "album") {
		rg := s["replayGain"].(map[string]any)
		if s["title"] == "Synthetic Take A1" {
			if rg["trackGain"] != nil || rg["albumGain"] == nil {
				t.Errorf("unmeasured Take: %v", rg)
			}
		} else if rg["trackGain"] == nil {
			t.Errorf("%v lost its trackGain", s["title"])
		}
	}
}

func TestSubsonic_RecordAlbum_Token(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	rec := e.record("Synthetic Record One")
	last := func() float64 {
		return e.ok("getIndexes")["indexes"].(map[string]any)["lastModified"].(float64)
	}
	l0 := last()
	if l0 <= 0 {
		t.Fatalf("lastModified = %v", l0)
	}
	if l1 := last(); l1 != l0 {
		t.Errorf("lastModified moved without a change: %v -> %v", l0, l1)
	}
	// ifModifiedSince at or after the stamp short-circuits to an empty list; before it, the full list
	empty := e.ok("getIndexes", "ifModifiedSince", fmt.Sprint(int64(l0)))["indexes"].(map[string]any)
	if _, has := empty["index"]; has {
		t.Errorf("ifModifiedSince = lastModified still lists: %v", empty)
	}
	if full := e.ok("getIndexes", "ifModifiedSince", "1")["indexes"].(map[string]any); full["index"] == nil {
		t.Error("ifModifiedSince = 1 lists nothing")
	}
	step := func(what string, change func()) {
		t.Helper()
		before := last()
		e.r.Clock.Advance(1_000_000_000 * 60)
		change()
		if after := last(); after <= before {
			t.Errorf("%s: lastModified %v -> %v, want it to move", what, before, after)
		}
	}
	step("unpublish", func() { e.publish(rec, false) })
	step("publish", func() { e.publish(rec, true) })
	s3 := e.one(`SELECT id FROM songs WHERE title = 'Synthetic Song S3'`)
	b2 := e.one(`SELECT id FROM tracks WHERE title = 'Synthetic Take B2'`)
	step("Pick flip", func() {
		if _, err := e.r.Services.Picks.Pin(context.Background(), recHuman, s3, &b2, nil); err != nil {
			t.Fatal(err)
		}
	})
	// the flipped Pick shows in the album: the floating slot follows it
	var titles []string
	for _, s := range songsOf(e.ok("getAlbum", "id", "rec-"+rec), "album") {
		titles = append(titles, s["title"].(string))
	}
	if !slices.Contains(titles, "Synthetic Take B2") || slices.Contains(titles, "Synthetic Take B1") {
		t.Errorf("after the flip: %v", titles)
	}
}

// TestSubsonic_LineageUnchanged: with the L2 world loaded and the Record unpublished, every lineage album answers exactly what
// it answered before Records existed; and with a Record published the lineage ids, songs and album fields are untouched.
func TestSubsonic_LineageUnchanged(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	rec := e.record("Synthetic Record One")
	root := e.one(`SELECT id FROM tracks WHERE title = 'Synthetic Take B2'`)
	al := "al-" + root
	pick := func() (string, string) {
		a := e.call("getAlbum", "id", al)
		m := e.call("getMusicDirectory", "id", al)
		return string(a.Body), string(m.Body)
	}
	e.publish(rec, false)
	albBefore, dirBefore := pick()
	listBefore := e.albumIDs("alphabeticalByName")
	e.publish(rec, true)
	albAfter, dirAfter := pick()
	if albBefore != albAfter || dirBefore != dirAfter {
		t.Errorf("a lineage album changed when a Record was published:\n%s\n%s", albBefore, albAfter)
	}
	after := e.albumIDs("alphabeticalByName")
	var without []string
	for _, id := range after {
		if !strings.HasPrefix(id, "rec-") {
			without = append(without, id)
		}
	}
	if !slices.Equal(without, listBefore) {
		t.Errorf("lineage ids changed: %v -> %v", listBefore, without)
	}
	// the lineage songs keep al-<root> as albumId and parent
	for _, s := range songsOf(e.ok("getAlbum", "id", al), "album") {
		if s["albumId"] != al || s["parent"] != al {
			t.Errorf("lineage song: albumId %v parent %v", s["albumId"], s["parent"])
		}
		if _, has := s["discNumber"]; has {
			t.Error("a lineage song gained discNumber")
		}
	}
	// the rig without any L2 data is the golden rig of M4a: TestSubsonic_XMLGolden and the schema sweep run it with the
	// catalog wired, so their byte-for-byte comparison is the proof for "a rig with no Records"
	if r, _, _ := fixtureRig(t); r.Services.Albums == nil {
		t.Fatal("the golden rig must have the album catalog wired")
	}
}

// TestSubsonic_NoMuseText: every Song carries a Muse (the L2 fixture assigns the two canary Muses); with subsonic_muse_names
// off no /rest answer holds a byte of a Muse title or artist.
func TestSubsonic_NoMuseText(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	r1 := "rec-" + e.record("Synthetic Record One")
	e.sql(`UPDATE muses SET title = title || ' canary-muse-title'`) // a marker the fixture text does not contain
	canaries := []string{librarytest.L2Muse1Title, librarytest.L2Muse1Artist, librarytest.L2Muse2Title, librarytest.L2Muse2Artist, "canary-muse-title"}
	calls := []struct {
		ep string
		kv []string
	}{
		{"getIndexes", nil}, {"getArtists", nil}, {"getArtist", []string{"id", "ar-metamusic"}},
		{"getMusicDirectory", []string{"id", "ar-metamusic"}}, {"getMusicDirectory", []string{"id", r1}},
		{"getAlbum", []string{"id", r1}}, {"getAlbumInfo2", []string{"id", r1}}, {"getAlbumInfo", []string{"id", r1}},
		{"getAlbumList2", []string{"type", "newest", "size", "50"}}, {"getAlbumList", []string{"type", "alphabeticalByName", "size", "50"}},
		{"getAlbumList2", []string{"type", "random", "size", "50"}}, {"getStarred2", nil}, {"getGenres", nil},
		{"search2", []string{"query", "synthetic"}}, {"search3", []string{"query", ""}}, {"search3", []string{"query", "inspiration"}},
		{"getRandomSongs", []string{"size", "50"}}, {"getSimilarSongs2", []string{"id", r1}},
	}
	sawRecord := false
	for _, cl := range calls {
		for _, asJSON := range []bool{true, false} {
			resp := e.c.Call(t, cl.ep, qv(cl.kv...), subsonictest.CallOpts{JSON: asJSON})
			for _, can := range canaries {
				if bytes.Contains(bytes.ToLower(resp.Body), []byte(strings.ToLower(can))) {
					t.Errorf("%s %v: the answer contains the Muse text %q: %.300s", cl.ep, cl.kv, can, resp.Body)
				}
			}
			sawRecord = sawRecord || bytes.Contains(resp.Body, []byte(r1))
		}
	}
	if !sawRecord {
		t.Error("no answer carried the Record album: the canary check was vacuous")
	}
}

// TestAcceptance_L2_SubsonicRecordAlbum: the operator publishes a Record and the Subsonic client sees rec-<id> in every list,
// browses it and fetches its cover; unpublishing it is error 70 on the next request.
func TestAcceptance_L2_SubsonicRecordAlbum(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	draft := e.record("Synthetic Record Two")
	id := "rec-" + draft
	if code := e.code("getAlbum", "id", id); code != 70 {
		t.Fatalf("a draft Record: getAlbum = %d, want 70", code)
	}
	if slices.Contains(e.albumIDs("newest"), id) {
		t.Fatal("a draft Record is listed")
	}
	e.publish(draft, true) // the human publishes
	if !slices.Contains(e.albumIDs("newest"), id) || !slices.Contains(e.albumIDs("alphabeticalByName"), id) {
		t.Fatalf("the published Record is not listed: %v", e.albumIDs("newest"))
	}
	songs := songsOf(e.ok("getAlbum", "id", id), "album")
	if len(songs) != 2 || songs[0]["title"] != "Synthetic Take S1A" || songs[1]["title"] != "Synthetic Take S6A" {
		t.Errorf("songs = %v", songs)
	}
	if resp := e.c.Call(t, "getCoverArt", qv("id", id), subsonictest.CallOpts{}); resp.HTTP != 200 || !bytes.HasPrefix(resp.Body, []byte("\x89PNG")) {
		t.Errorf("cover: http %d code %d", resp.HTTP, resp.Code)
	}
	if got := e.ok("search3", "query", "record two")["searchResult3"].(map[string]any)["album"].([]any); len(got) != 1 {
		t.Errorf("search3 = %v", got)
	}
	e.publish(draft, false)
	for _, ep := range []string{"getAlbum", "getMusicDirectory", "getAlbumInfo2", "getCoverArt"} {
		if code := e.code(ep, "id", id); code != 70 {
			t.Errorf("after unpublishing: %s = %d, want 70", ep, code)
		}
	}
	if slices.Contains(e.albumIDs("newest"), id) {
		t.Error("the unpublished Record is still listed")
	}
}

// TestSubsonic_RecordAlbum_SchemaAndXML: the Record answers validate against the vendored OpenSubsonic description (the album
// ones add discNumber and albumGain, both part of it) and the XML twin parses.
func TestSubsonic_RecordAlbum_SchemaAndXML(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	r1 := "rec-" + e.record("Synthetic Record One")
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc openapiDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	defs := convert(doc.Components.Schemas).(map[string]any)
	for _, cl := range []struct {
		ep string
		kv []string
	}{
		{"getAlbum", []string{"id", r1}}, {"getMusicDirectory", []string{"id", r1}}, {"getAlbumList2", []string{"type", "newest", "size", "500"}},
		{"getAlbumList", []string{"type", "newest", "size", "500"}}, {"getIndexes", nil}, {"getArtist", []string{"id", "ar-metamusic"}},
		{"getArtists", nil}, {"search3", []string{"query", "record"}}, {"search2", []string{"query", "record"}},
	} {
		ref := doc.responseSchemaRef(t, cl.ep)
		root := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$ref": strings.Replace(ref, "#/components/schemas/", "#/$defs/", 1), "$defs": reachable(defs, strings.TrimPrefix(ref, "#/components/schemas/"))}
		rb, _ := json.Marshal(root)
		var sch jsonschema.Schema
		if err := json.Unmarshal(rb, &sch); err != nil {
			t.Fatal(err)
		}
		rs, err := sch.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		resp := e.c.Call(t, cl.ep, qv(cl.kv...), subsonictest.CallOpts{JSON: true})
		var env any
		if err := json.Unmarshal(resp.Body, &env); err != nil {
			t.Fatal(err)
		}
		if err := rs.Validate(env); err != nil {
			t.Errorf("%s does not match the schema: %v\n%.500s", cl.ep, err, resp.Body)
		}
		xmlResp := e.c.Call(t, cl.ep, qv(cl.kv...), subsonictest.CallOpts{})
		if xmlResp.Status != "ok" || !bytes.Contains(xmlResp.Body, []byte(r1)) && cl.ep != "getArtist" && cl.ep != "getArtists" {
			t.Errorf("%s XML: status %q", cl.ep, xmlResp.Status)
		}
	}
}
