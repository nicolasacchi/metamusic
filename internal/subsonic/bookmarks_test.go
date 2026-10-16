package subsonic

import (
	"strings"
	"testing"
	"time"
)

type cmRow struct {
	Body, Source, Kind string
	At                 float64
}

func (r *rig) comments(track string) []cmRow {
	r.t.Helper()
	rows, err := r.st.R.Query(`SELECT body, source, kind, COALESCE(at_second, -1) FROM comments WHERE track_id = ? ORDER BY created_at, id`, track)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []cmRow
	for rows.Next() {
		var c cmRow
		if err := rows.Scan(&c.Body, &c.Source, &c.Kind, &c.At); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func TestSubsonic_BookmarkCommentOnce(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t) // luna lasts 12 s
	eq(t, "create", x.callCode("createBookmark", "id", luna, "position", "6000", "comment", "check the bridge"), 0)
	cs := x.comments(luna)
	eq(t, "one comment", len(cs), 1)
	eq(t, "body", cs[0].Body, "check the bridge")
	eq(t, "at_second", cs[0].At, 6.0)
	eq(t, "source", cs[0].Source, "subsonic")
	eq(t, "kind", cs[0].Kind, "note")
	// the same call again: no new comment
	x.clk.advance(time.Minute)
	eq(t, "again", x.callCode("createBookmark", "id", luna, "position", "7000", "comment", "check the bridge"), 0)
	eq(t, "still one", len(x.comments(luna)), 1)
	// new text: a second comment
	eq(t, "new text", x.callCode("createBookmark", "id", luna, "position", "7000", "comment", "and the chorus"), 0)
	cs = x.comments(luna)
	eq(t, "two", len(cs), 2)
	eq(t, "second at", cs[1].At, 7.0)
	// no comment: none created, the position is stored
	eq(t, "no comment", x.callCode("createBookmark", "id", luna, "position", "8000"), 0)
	eq(t, "no new", len(x.comments(luna)), 2)
	bm := list(t, x.call("getBookmarks"), "bookmarks", "bookmark")
	eq(t, "bookmarks", len(bm), 1)
	eq(t, "position", bm[0]["position"], 8000)
	eq(t, "username", bm[0]["username"], "rwclient")
	eq(t, "entry", dig(t, bm[0], "entry", "id"), luna)
	if bm[0]["created"] == nil || bm[0]["changed"] == nil {
		t.Errorf("timestamps missing: %v", bm[0])
	}
	if c := bm[0]["comment"]; c != nil && c != "" {
		t.Errorf("comment %v after a comment-less update", c)
	}
	// clamped and rounded to 0.1 s
	for _, tc := range []struct {
		pos, text string
		want      float64
	}{{"99000", "past the end", 6.0}, {"3040", "round down", 3.0}, {"3060", "round up", 3.1}} {
		eq(t, tc.text, x.callCode("createBookmark", "id", citta, "position", tc.pos, "comment", tc.text), 0)
		cs := x.comments(citta)
		eq(t, tc.text+" at_second", cs[len(cs)-1].At, tc.want)
	}
	// delete: the bookmark disappears, the comments stay
	eq(t, "delete", x.callCode("deleteBookmark", "id", luna), 0)
	for _, b := range list(t, x.call("getBookmarks"), "bookmarks", "bookmark") {
		if dig(t, b, "entry", "id") == luna {
			t.Error("deleted bookmark still listed")
		}
	}
	eq(t, "comments kept", len(x.comments(luna)), 2)
	var deleted string
	if err := x.st.R.QueryRow(`SELECT COALESCE(deleted_at, '') FROM subsonic_bookmarks WHERE track_id = ?`, luna).Scan(&deleted); err != nil || deleted == "" {
		t.Errorf("row not soft-deleted: %q %v", deleted, err)
	}
	eq(t, "delete again", x.callCode("deleteBookmark", "id", luna), 0)
	// re-creating revives it; the previous comment is the one stored last, so a repeat of it adds nothing
	eq(t, "revive", x.callCode("createBookmark", "id", luna, "position", "1000"), 0)
	eq(t, "revived", len(list(t, x.call("getBookmarks"), "bookmarks", "bookmark")), 2)
	eq(t, "no comment growth", len(x.comments(luna)), 2)
}

func TestSubsonic_BookmarkOrderAndErrors(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.callCode("createBookmark", "id", luna, "position", "1000")
	x.clk.advance(time.Minute)
	x.callCode("createBookmark", "id", citta, "position", "2000")
	x.clk.advance(time.Minute)
	x.callCode("createBookmark", "id", luna, "position", "3000") // luna updated last
	var got []string
	for _, b := range list(t, x.call("getBookmarks"), "bookmarks", "bookmark") {
		got = append(got, dig(t, b, "entry", "id").(string))
	}
	eq(t, "newest first", strings.Join(got, ","), luna+","+citta)
	eq(t, "unknown", x.callCode("createBookmark", "id", "no-such", "position", "1"), 70)
	eq(t, "album id", x.callCode("createBookmark", "id", al(luna), "position", "1"), 70)
	eq(t, "missing id", x.callCode("createBookmark", "position", "1"), 10)
	eq(t, "missing position", x.callCode("createBookmark", "id", luna), 10)
	eq(t, "bad position", x.callCode("createBookmark", "id", luna, "position", "-5"), 0)
	eq(t, "delete unknown", x.callCode("deleteBookmark", "id", "no-such"), 70)
	eq(t, "delete missing", x.callCode("deleteBookmark"), 10)
	eq(t, "read-only create", x.get("createBookmark", keyParams(x.roKey, "id", luna, "position", "1"), libIP).code(t), 50)
	eq(t, "read-only delete", x.get("deleteBookmark", keyParams(x.roKey, "id", luna), libIP).code(t), 50)
	eq(t, "read-only list", x.get("getBookmarks", keyParams(x.roKey), libIP).code(t), 0)
	// an over-long comment is refused with the fixed text
	w := x.get("createBookmark", x.rwq("id", luna, "position", "1", "comment", strings.Repeat("x", 4001)), libIP)
	eq(t, "long comment", w.code(t), 0)
	eq(t, "long comment message", dig(t, w.doc(t), "error", "message"), "bookmark not allowed")
}

func TestSubsonic_PlayQueue(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	other := x.newClient(t, "other", false, true)
	// none yet: an element without entries
	d := x.call("getPlayQueue")
	pq := dig(t, d, "playQueue").(map[string]any)
	eq(t, "username", pq["username"], "rwclient")
	eq(t, "no entries", len(list(t, d, "playQueue", "entry")), 0)
	// save with an unknown id (dropped), a current and a position
	eq(t, "save", x.callCode("savePlayQueue", "id", luna, "id", "no-such", "id", citta, "id", twinA, "current", citta, "position", "1500"), 0)
	d = x.call("getPlayQueue")
	eq(t, "entries", strings.Join(ids(list(t, d, "playQueue", "entry")), ","), luna+","+citta+","+twinA)
	eq(t, "current", dig(t, d, "playQueue", "current"), citta)
	eq(t, "position", dig(t, d, "playQueue", "position"), 1500)
	eq(t, "changedBy", dig(t, d, "playQueue", "changedBy"), "testapp")
	if dig(t, d, "playQueue", "changed") == nil {
		t.Error("no changed")
	}
	// one row for every client; the last writer wins
	q := keyParams(other, "id", short, "current", short, "position", "10")
	q.Set("c", "OtherApp")
	eq(t, "other saves", x.get("savePlayQueue", q, libIP).code(t), 0)
	var n int
	if err := x.st.R.QueryRow(`SELECT COUNT(*) FROM subsonic_play_queue`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d (%v)", n, err)
	}
	d = x.call("getPlayQueue")
	eq(t, "replaced", strings.Join(ids(list(t, d, "playQueue", "entry")), ","), short)
	eq(t, "changedBy other", dig(t, d, "playQueue", "changedBy"), "OtherApp")
	// a current outside the saved ids is dropped
	x.callCode("savePlayQueue", "id", luna, "current", citta)
	if v := dig(t, x.call("getPlayQueue"), "playQueue").(map[string]any)["current"]; v != nil && v != "" {
		t.Errorf("current = %v", v)
	}
	// a queue holds at most 1000 ids
	args := make([]string, 0, 2002)
	for range 1001 {
		args = append(args, "id", luna)
	}
	eq(t, "too many", x.callCode("savePlayQueue", args...), 0)
	eq(t, "at the limit", x.callCode("savePlayQueue", args[:2000]...), 0)
	eq(t, "1000 entries", len(list(t, x.call("getPlayQueue"), "playQueue", "entry")), 1000)
	// no ids clears the queue
	eq(t, "clear", x.callCode("savePlayQueue"), 0)
	eq(t, "cleared", len(list(t, x.call("getPlayQueue"), "playQueue", "entry")), 0)
	// the read-only client reads but cannot save
	eq(t, "read-only get", x.get("getPlayQueue", keyParams(x.roKey), libIP).code(t), 0)
	eq(t, "read-only save", x.get("savePlayQueue", keyParams(x.roKey, "id", luna), libIP).code(t), 50)
}
