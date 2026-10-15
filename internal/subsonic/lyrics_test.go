package subsonic

import (
	"strings"
	"testing"
)

func TestSubsonic_Lyrics(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.exec("DROP TRIGGER recipes_immutable") // this test database is private
	// Luna Park: [Verse 1] la la luna / [Chorus] sing it loud / [Outro] fade away
	d := x.call("getLyricsBySongId", "id", luna)
	sl := list(t, d, "lyricsList", "structuredLyrics")
	eq(t, "one entry", len(sl), 1)
	eq(t, "synced", sl[0]["synced"], false)
	eq(t, "offset", sl[0]["offset"], 0)
	eq(t, "displayArtist", sl[0]["displayArtist"], "metamusic")
	eq(t, "displayTitle", sl[0]["displayTitle"], "Luna Park")
	var vals []string
	for _, l := range sl[0]["line"].([]any) {
		m := l.(map[string]any)
		if m["start"] != nil {
			t.Errorf("start on unsynced lyrics: %v", m)
		}
		vals = append(vals, m["value"].(string))
	}
	eq(t, "lines", join(vals), "la la luna||sing it loud||fade away")
	// the language comes from M2a's detector, "und" when it cannot tell
	eq(t, "lang und", sl[0]["lang"], "und")
	x.exec(`UPDATE recipes SET lyrics = ? WHERE id = (SELECT recipe_id FROM tracks WHERE id = ?)`,
		"[Verse]\nthe sun is in the sky and the day is warm\nwe are all in the light of the morning\nand it is not the end of the world, you know", luna)
	d = x.call("getLyricsBySongId", "id", luna)
	eq(t, "lang en", list(t, d, "lyricsList", "structuredLyrics")[0]["lang"], "en")
	// a track without lyrics answers an empty list
	x.exec(`UPDATE recipes SET lyrics = '' WHERE id = (SELECT recipe_id FROM tracks WHERE id = ?)`, citta)
	d = x.call("getLyricsBySongId", "id", citta)
	eq(t, "empty list", len(list(t, d, "lyricsList", "structuredLyrics")), 0)
	if dig(t, d, "lyricsList") == nil {
		t.Error("lyricsList element missing")
	}
	// getLyrics: best title match
	x.exec(`UPDATE recipes SET lyrics = ? WHERE id = (SELECT recipe_id FROM tracks WHERE id = ?)`, "[Verse 1]\nla la luna\n[Chorus]\nsing it loud\n[Outro]\nfade away", luna)
	d = x.call("getLyrics", "artist", "metamusic", "title", "luna park")
	eq(t, "title", dig(t, d, "lyrics", "title"), "Luna Park")
	eq(t, "artist", dig(t, d, "lyrics", "artist"), "metamusic")
	eq(t, "value", dig(t, d, "lyrics", "value"), "la la luna\n\nsing it loud\n\nfade away")
	d = x.call("getLyrics", "title", "lun")
	eq(t, "prefix", dig(t, d, "lyrics", "title"), "Luna Park")
	d = x.call("getLyrics", "title", "zzzz")
	eq(t, "no match", dig(t, d, "lyrics", "value"), "")
	d = x.call("getLyrics")
	eq(t, "no title", dig(t, d, "lyrics", "value"), "")
	// the folder rule applies: a discarded take is not found by title
	x.setStatus(luna, "discarded", x.clk.now())
	d = x.call("getLyrics", "title", "luna park")
	eq(t, "discarded", dig(t, d, "lyrics", "value"), "")
	// errors
	eq(t, "unknown", x.callCode("getLyricsBySongId", "id", "no-such"), 70)
	eq(t, "album id", x.callCode("getLyricsBySongId", "id", al(luna)), 70)
	eq(t, "missing", x.callCode("getLyricsBySongId"), 10)
	// the extension is advertised
	found := false
	for _, e := range list(t, x.call("getOpenSubsonicExtensions"), "openSubsonicExtensions") {
		found = found || e["name"] == "songLyrics"
	}
	if !found {
		t.Error("songLyrics not advertised")
	}
}

func join(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += "|"
		}
		out += s
	}
	return out
}

// TestSubsonic_S3XMLShapes: the XML twins of the new elements carry attributes, nested entries and text content.
func TestSubsonic_S3XMLShapes(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	id := dig(t, x.call("createPlaylist", "name", "Xml", "songId", luna), "playlist", "id").(string)
	x.callCode("createBookmark", "id", luna, "position", "2000", "comment", "note")
	x.callCode("savePlayQueue", "id", luna, "current", luna, "position", "5")
	body := func(ep string, kv ...string) string {
		q := x.rwq(kv...)
		q.Set("f", "xml")
		w := x.raw("GET", ep, q, nil)
		statusOK(t, w, 200)
		return w.Body.String()
	}
	for ep, wants := range map[string][]string{
		"getPlaylist":       {`<playlist id="` + id + `" name="Xml"`, `public="false"`, `songCount="1"`, `<entry id="` + luna + `"`},
		"getPlaylists":      {`<playlists>`, `<playlist id=`},
		"getBookmarks":      {`<bookmark position="2000" username="rwclient"`, `comment="note"`, `<entry id="` + luna + `"`},
		"getPlayQueue":      {`<playQueue current="` + luna + `" position="5" username="rwclient"`, `changedBy="testapp"`, `<entry id="` + luna + `"`},
		"getLyrics":         {`<lyrics artist="metamusic" title="Luna Park">la la luna`},
		"getLyricsBySongId": {`<lyricsList>`, `<structuredLyrics displayArtist="metamusic" displayTitle="Luna Park" lang="und" offset="0" synced="false">`, `<line>la la luna</line>`},
	} {
		var b string
		switch ep {
		case "getPlaylist":
			b = body(ep, "id", id)
		case "getLyrics":
			b = body(ep, "title", "luna park")
		case "getLyricsBySongId":
			b = body(ep, "id", luna)
		default:
			b = body(ep)
		}
		for _, w := range wants {
			if !strings.Contains(b, w) {
				t.Errorf("%s lacks %q:\n%s", ep, w, b)
			}
		}
	}
}
