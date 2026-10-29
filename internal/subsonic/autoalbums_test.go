package subsonic_test

import (
	"bytes"
	"context"
	"image/png"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// The Muse auto-albums of L2 (A2/S2): over the FixtureL2 world Muse 1 holds one Song outside the published Record (S1), Muse 2
// holds none (S4 and S5 are in the Record), and the Muse-less S6 is the "Unfiled" album.

func (e *recEnv) muse(title string) string {
	return "mus-" + e.one(`SELECT id FROM muses WHERE title = ?`, title)
}

func (e *recEnv) names(on bool) {
	e.t.Helper()
	if _, err := e.r.Services.Curation.Set(context.Background(), recHuman, curation.SettingsPatch{SubsonicMuseNames: &on}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *recEnv) albumRow(id string) map[string]any {
	e.t.Helper()
	return e.ok("getAlbum", "id", id)["album"].(map[string]any)
}

func TestSubsonic_AutoAlbum_List(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	m1, m2 := e.muse(librarytest.L2Muse1Title), e.muse(librarytest.L2Muse2Title)
	for _, typ := range []string{"newest", "alphabeticalByName", "alphabeticalByArtist", "random", "byGenre"} {
		kv := []string{}
		if typ == "byGenre" {
			continue
		}
		got := e.albumIDs(typ, kv...)
		if !slices.Contains(got, m1) || !slices.Contains(got, "unf") {
			t.Errorf("%s: %v lacks the Muse album or unf", typ, got)
		}
		if slices.Contains(got, m2) {
			t.Errorf("%s lists the empty Muse album", typ)
		}
	}
	// names are neutral, and the lists carry them
	byID := map[string]map[string]any{}
	for _, a := range list(e.ok("getAlbumList2", "type", "alphabeticalByName", "size", "500"), "albumList2") {
		byID[a["id"].(string)] = a
	}
	if byID[m1]["name"] != "Inspiration 001" || byID["unf"]["name"] != "Unfiled" {
		t.Errorf("names = %v / %v", byID[m1]["name"], byID["unf"]["name"])
	}
	if byID[m1]["songCount"].(float64) != 1 || byID[m1]["coverArt"] != m1 {
		t.Errorf("muse album row = %v", byID[m1])
	}
	// getAlbumList (the older form) and paging agree with getAlbumList2
	full := e.albumIDs("alphabeticalByName")
	if old := ids(list(e.ok("getAlbumList", "type", "alphabeticalByName", "size", "500"), "albumList")); !slices.Equal(old, full) {
		t.Errorf("getAlbumList = %v, getAlbumList2 = %v", old, full)
	}
	if got := ids(list(e.ok("getAlbumList2", "type", "alphabeticalByName", "size", "3", "offset", "1"), "albumList2")); !slices.Equal(got, full[1:4]) {
		t.Errorf("page = %v, want %v", got, full[1:4])
	}
	// byYear: the year of the newest song
	year := e.one(`SELECT substr(MAX(created_at), 1, 4) FROM tracks WHERE title = 'Synthetic Take S1A'`)
	if got := e.albumIDs("byYear", "fromYear", year, "toYear", year); !slices.Contains(got, m1) {
		t.Errorf("byYear %s: %v", year, got)
	}
	// publishing the only Song of unf moves it out; the album disappears
	e.sql(`UPDATE songs SET hidden_at = '2026-10-28T10:00:00.000Z', hidden_by = 'x' WHERE title = 'Synthetic Song S6'`)
	if got := e.albumIDs("newest"); slices.Contains(got, "unf") {
		t.Errorf("unf listed after its only Song was hidden: %v", got)
	}
}

func TestSubsonic_AutoAlbum_GetAndBrowse(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	m1 := e.muse(librarytest.L2Muse1Title)
	for _, c := range []struct{ id, title string }{{m1, "Synthetic Take S1A"}, {"unf", "Synthetic Take S6A"}} {
		songs := songsOf(e.ok("getAlbum", "id", c.id), "album")
		if len(songs) != 1 || songs[0]["title"] != c.title {
			t.Fatalf("%s songs = %v", c.id, songs)
		}
		s := songs[0]
		if s["albumId"] != c.id || s["parent"] != c.id || s["track"].(float64) != 1 || s["discNumber"].(float64) != 1 {
			t.Errorf("%s song: albumId %v parent %v track %v disc %v", c.id, s["albumId"], s["parent"], s["track"], s["discNumber"])
		}
		dir, _ := e.ok("getMusicDirectory", "id", c.id)["directory"].(map[string]any)["child"].([]any)
		if len(dir) != 1 || dir[0].(map[string]any)["parent"] != c.id {
			t.Errorf("%s directory = %v", c.id, dir)
		}
		e.ok("getAlbumInfo2", "id", c.id)
		e.ok("getAlbumInfo", "id", c.id)
		// getSong keeps the lineage id outside the album
		if song := e.ok("getSong", "id", s["id"].(string))["song"].(map[string]any); strings.HasPrefix(song["albumId"].(string), "mus-") || song["albumId"] == "unf" {
			t.Errorf("getSong albumId = %v", song["albumId"])
		}
	}
	// the artist directory and the indexes list the auto-albums
	ar := e.ok("getArtist", "id", "ar-metamusic")["artist"].(map[string]any)
	var got []string
	for _, a := range ar["album"].([]any) {
		got = append(got, a.(map[string]any)["id"].(string))
	}
	if !slices.Contains(got, m1) || !slices.Contains(got, "unf") {
		t.Errorf("getArtist = %v", got)
	}
	if kids, _ := e.ok("getMusicDirectory", "id", "ar-metamusic")["directory"].(map[string]any)["child"].([]any); len(kids) == 0 {
		t.Error("empty artist directory")
	}
	// the Pick moves with the rating: unpublishing the Record fills Muse 2 and extends Muse 1 in Song order
	e.publish(e.record("Synthetic Record One"), false)
	if n := len(songsOf(e.ok("getAlbum", "id", m1), "album")); n < 2 {
		t.Errorf("after unpublishing, Muse 1 holds %d songs", n)
	}
	songs := songsOf(e.ok("getAlbum", "id", m1), "album")
	for i, s := range songs {
		if int(s["track"].(float64)) != i+1 || s["discNumber"].(float64) != 1 {
			t.Errorf("song %d: track %v disc %v", i, s["track"], s["discNumber"])
		}
	}
	if !slices.Contains(e.albumIDs("newest"), e.muse(librarytest.L2Muse2Title)) {
		t.Error("Muse 2 is not listed once its Songs left the Record")
	}
}

func TestSubsonic_AutoAlbum_Errors(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	m1, m2 := e.muse(librarytest.L2Muse1Title), e.muse(librarytest.L2Muse2Title)
	endpoints := []string{"getAlbum", "getMusicDirectory", "getAlbumInfo2", "getCoverArt"}
	for _, ep := range endpoints {
		for _, id := range []string{"mus-nope", m2} { // unknown, and empty (all its Songs are in the Record)
			if code := e.code(ep, "id", id); code != 70 {
				t.Errorf("%s %s = %d, want 70", ep, id, code)
			}
		}
		if code := e.code(ep, "id", "mus-"); code != 70 { // a malformed id is "not found", as for rec-
			t.Errorf("%s mus- = %d, want 70", ep, code)
		}
	}
	e.sql(`UPDATE muses SET hidden_at = '2026-10-28T11:00:00.000Z', hidden_by = 'x', updated_at = '2026-10-28T11:00:00.000Z' WHERE id = ?`, strings.TrimPrefix(m1, "mus-"))
	for _, ep := range endpoints {
		if code := e.code(ep, "id", m1); code != 70 {
			t.Errorf("hidden Muse: %s = %d, want 70", ep, code)
		}
	}
	if slices.Contains(e.albumIDs("newest"), m1) {
		t.Error("a hidden Muse's album is listed")
	}
	// a hidden published Record does not take its Songs out of the auto-albums (A2 seam)
	e.sql(`UPDATE records SET hidden_at = '2026-10-28T11:00:00.000Z', hidden_by = 'x' WHERE title = 'Synthetic Record One'`)
	if !slices.Contains(e.albumIDs("newest"), e.muse(librarytest.L2Muse2Title)) {
		t.Error("a hidden Record keeps its Songs out of the Muse albums")
	}
}

func TestSubsonic_AutoAlbum_Cover(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	for _, id := range []string{e.muse(librarytest.L2Muse1Title), "unf"} {
		resp := e.c.Call(t, "getCoverArt", qv("id", id, "size", "96"), subsonictest.CallOpts{})
		if resp.HTTP != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/png") {
			t.Fatalf("%s: http %d type %q", id, resp.HTTP, resp.Header.Get("Content-Type"))
		}
		img, err := png.Decode(bytes.NewReader(resp.Body))
		if err != nil || img.Bounds().Dx() != 96 {
			t.Errorf("%s: %v %v", id, err, img.Bounds())
		}
		if a := e.albumRow(id); a["coverArt"] != id {
			t.Errorf("coverArt = %v", a["coverArt"])
		}
	}
}

func TestSubsonic_AutoAlbum_Search(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	m1 := e.muse(librarytest.L2Muse1Title)
	albumsOf := func(ep, q string) []string {
		var out []string
		res := e.ok(ep, "query", q)[map[string]string{"search2": "searchResult2", "search3": "searchResult3"}[ep]].(map[string]any)
		albs, _ := res["album"].([]any)
		for _, a := range albs {
			out = append(out, a.(map[string]any)["id"].(string))
		}
		return out
	}
	for _, ep := range []string{"search2", "search3"} {
		if got := albumsOf(ep, "inspiration 001"); !slices.Contains(got, m1) {
			t.Errorf("%s inspiration: %v", ep, got)
		}
		if got := albumsOf(ep, "UNFILED"); !slices.Contains(got, "unf") {
			t.Errorf("%s unfiled: %v", ep, got)
		}
	}
	// the Muse's title is not searchable while the flag is off
	if got := albumsOf("search3", "curation muse 1"); slices.Contains(got, m1) {
		t.Errorf("a Muse title found an album with the flag off: %v", got)
	}
}

func TestSubsonic_AutoAlbum_Token(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	last := func() float64 { return e.ok("getIndexes")["indexes"].(map[string]any)["lastModified"].(float64) }
	l0 := last()
	if l1 := last(); l1 != l0 {
		t.Errorf("lastModified moved without a change: %v -> %v", l0, l1)
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
	m1 := strings.TrimPrefix(e.muse(librarytest.L2Muse1Title), "mus-")
	step("hide the Muse", func() {
		e.sql(`UPDATE muses SET hidden_at = '2026-10-28T11:00:00.000Z', hidden_by = 'x', updated_at = '2026-10-28T11:00:00.000Z' WHERE id = ?`, m1)
	})
	step("opt in to names", func() { e.names(true) })
}

func TestSubsonic_AutoAlbum_OptInNames(t *testing.T) {
	t.Parallel()
	e := newRecEnv(t)
	m1 := e.muse(librarytest.L2Muse1Title)
	if n := e.albumRow(m1)["name"]; n != "Inspiration 001" {
		t.Fatalf("name = %v", n)
	}
	e.names(true)
	n, _ := e.albumRow(m1)["name"].(string)
	if !strings.Contains(n, librarytest.L2Muse1Title) {
		t.Errorf("opted-in name = %q", n)
	}
	found := false
	for _, a := range list(e.ok("getAlbumList2", "type", "alphabeticalByName", "size", "500"), "albumList2") {
		if a["id"] == m1 {
			found = a["name"] == n
		}
	}
	if !found {
		t.Error("the list does not carry the opted-in name")
	}
	if a := e.albumRow("unf"); a["name"] != "Unfiled" {
		t.Errorf("unf renamed: %v", a["name"])
	}
	// searchable by the Muse title only now
	if got := e.ok("search3", "query", "curation muse 1")["searchResult3"].(map[string]any)["album"]; got == nil {
		t.Error("the opted-in name is not searchable")
	}
	e.names(false)
	if n := e.albumRow(m1)["name"]; n != "Inspiration 001" {
		t.Errorf("flag off again: %v", n)
	}
}
