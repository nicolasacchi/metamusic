package acceptance_test

import (
	"bytes"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// subsonicBanned are the keys a Listener's Subsonic answer never carries: the average rating (D-U1-16), the style comment
// (a raw style), and the master's own facts.
var subsonicBanned = []string{"averageRating", "song.comment", "child.comment", "entry.comment", "bitDepth", "seed", "request_id", "master_sha256", "muse_id"}

var idRe = regexp.MustCompile(`"id":"([^"]+)"`)

// subsonicSweep calls every Subsonic endpoint the Listener's client may reach, as JSON and as XML, with ids it discovers by
// browsing plus the discarded and the unknown ones, and scans every answer for the sensitive values.
func (w *intbWorld) subsonicSweep(t *testing.T, name string, c *subsonictest.Client, secrets []intbSecret, banned []string) int {
	t.Helper()
	seen := map[string]bool{}
	var calls int
	call := func(ep string, q url.Values) {
		for _, js := range []bool{true, false} {
			resp := c.Call(t, ep, q, subsonictest.CallOpts{JSON: js})
			calls++
			if resp.HTTP >= 500 && !w.noFiles(t, []string{q.Get("id")}) {
				t.Errorf("%s %s %v: HTTP %d", name, ep, q, resp.HTTP)
			}
			body := resp.Body
			if !js {
				body = []byte(html.UnescapeString(string(body)))
			}
			scrub := []string{q.Get("id"), q.Get("query"), q.Get("title"), q.Get("artist")}
			ct := resp.Header.Get("Content-Type")
			var hits []intbHit
			switch {
			case strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "application/octet") || strings.HasPrefix(ct, "image/"):
				for _, s := range secrets {
					if !s.Num && bytes.Contains(body, []byte(s.Value)) {
						hits = append(hits, intbHit{"file", s.Kind + " " + s.Value})
					}
				}
				if bytes.Contains(body, []byte("MM_")) {
					hits = append(hits, intbHit{"file", "an MM_ tag"})
				}
			case js:
				hits = intbScan("subsonic", body, secrets, banned, scrub)
			default:
				hits = intbScanText("subsonic", string(body), secrets, scrub, "xml")
				for _, b := range banned { // the XML and JSON answers come from one struct: dotted (path) keys are checked on the JSON
					if !strings.Contains(b, ".") && strings.Contains(string(body), " "+b+"=") {
						hits = append(hits, intbHit{"xml", "banned attribute " + b})
					}
				}
			}
			for _, h := range hits {
				key := ep + "|" + h.Where + "|" + h.What
				if !seen[key] {
					seen[key] = true
					t.Errorf("%s %s %v (json=%v): leak at %s: %s", name, ep, q, js, h.Where, h.What)
				}
			}
			for _, m := range idRe.FindAllStringSubmatch(string(resp.Body), -1) {
				discovered[m[1]] = true
			}
		}
	}
	discovered = map[string]bool{}
	ids := append([]string{"ar-metamusic", "unf"}, w.Discarded...)
	ids = append(ids, w.Visible[:min(12, len(w.Visible))]...)
	for id := range w.Taint {
		ids = append(ids, id)
	}
	for _, rid := range w.Records {
		ids = append(ids, "rec-"+rid)
	}
	ids = append(ids, "rec-"+w.Draft, "al-"+w.Discarded[0], "al-01900000-0000-7000-8000-00000000dead", "mus-"+strings.Join(intbSQLStrings(t, w.R, `SELECT id FROM muses LIMIT 1`), ""))
	for _, ep := range []string{"ping", "getLicense", "getOpenSubsonicExtensions", "tokenInfo", "getMusicFolders", "getScanStatus", "getGenres", "getArtists",
		"getStarred", "getStarred2", "getNowPlaying", "getBookmarks", "getPlayQueue", "getPlaylists", "getRandomSongs"} {
		call(ep, url.Values{})
	}
	for _, f := range []string{"1", "2", "3"} {
		for _, ep := range []string{"getIndexes", "getRandomSongs", "getAlbumList2", "getAlbumList"} {
			call(ep, url.Values{"musicFolderId": {f}, "type": {"newest"}, "size": {"500"}})
		}
	}
	for _, ty := range []string{"newest", "random", "alphabeticalByName", "alphabeticalByArtist", "recent", "frequent", "highest", "starred", "byYear", "byGenre"} {
		call("getAlbumList2", url.Values{"type": {ty}, "size": {"500"}, "fromYear": {"2000"}, "toYear": {"2100"}, "genre": {"dance"}})
	}
	for _, q := range []string{"", "*", "a", "twin", "synthetic", canaryMuse, canaryNote, "Muse", "yue2"} {
		call("search3", url.Values{"query": {q}, "songCount": {"500"}, "albumCount": {"500"}, "artistCount": {"50"}})
		call("search2", url.Values{"query": {q}})
	}
	for _, g := range []string{"dance", "reggae", "Synthetic"} {
		call("getSongsByGenre", url.Values{"genre": {g}, "count": {"500"}})
	}
	call("getUser", url.Values{"username": {c.Username}})
	call("getLyrics", url.Values{"artist": {"metamusic"}, "title": {"Synthetic"}})
	call("getTopSongs", url.Values{"artist": {"metamusic"}, "count": {"500"}})
	// browse: every id we know, then every id the answers named, a level deep
	each := func(ids []string) {
		for _, id := range ids {
			for _, ep := range []string{"getSong", "getAlbum", "getArtist", "getMusicDirectory", "getPlaylist", "getLyricsBySongId", "getSimilarSongs", "getSimilarSongs2",
				"getArtistInfo", "getArtistInfo2", "getAlbumInfo", "getAlbumInfo2", "getCoverArt"} {
				call(ep, url.Values{"id": {id}})
			}
		}
	}
	each(ids)
	var found []string
	for id := range discovered {
		if !strings.HasPrefix(id, "sweep") {
			found = append(found, id)
		}
	}
	if len(found) > 120 {
		found = found[:120]
	}
	each(found)
	// the audio: both formats and the master spellings, for every take we know
	for _, id := range append(ids, found...) {
		for _, f := range []string{"", "raw", "flac", "mp3", "original"} {
			q := url.Values{"id": {id}}
			if f != "" {
				q.Set("format", f)
			}
			call("stream", q)
		}
		call("download", url.Values{"id": {id}})
	}
	// state-changing endpoints a read-only client may still answer (all refused or harmless; never a leak)
	for _, id := range ids[:6] {
		call("setRating", url.Values{"id": {id}, "rating": {"4"}})
		call("star", url.Values{"id": {id}})
		call("unstar", url.Values{"id": {id}})
		call("scrobble", url.Values{"id": {id}, "submission": {"true"}})
		call("createBookmark", url.Values{"id": {id}, "position": {"1000"}})
	}
	return calls
}

var discovered map[string]bool

// TestIntBListenerSubsonicSweep is the Subsonic half of the Listener sweep: a REAL Listener client, JSON and XML.
func TestIntBListenerSubsonicSweep(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	lsec, _ := w.secrets(t)
	for _, write := range []bool{false, true} {
		c := w.R.SubsonicClientFor(t, "carol", subsonictest.ClientOpts{Name: "Carol sweep app", Write: write, Username: map[bool]string{false: "carol-ro", true: "carol-rw"}[write]})
		n := w.subsonicSweep(t, "carol-subsonic", c, lsec, subsonicBanned)
		if n < 500 {
			t.Errorf("the Subsonic sweep made only %d calls", n)
		}
	}
}
