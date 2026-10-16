package subsonic

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

func (r *rig) status(id string) string {
	r.t.Helper()
	var s string
	if err := r.st.R.QueryRow(`SELECT status FROM tracks WHERE id = ?`, id).Scan(&s); err != nil {
		r.t.Fatal(err)
	}
	return s
}

func (r *rig) auditRows() []audit.Row {
	r.t.Helper()
	page, err := r.auditStore.List(context.Background(), audit.Filter{})
	if err != nil {
		r.t.Fatal(err)
	}
	return page.Items
}

func TestSubsonic_StarUnstar(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	eq(t, "star", x.callCode("star", "id", luna, "albumId", al(citta), "artistId", artistID), 0)
	eq(t, "status final", x.status(luna), "final")
	first := dig(t, x.call("getSong", "id", luna), "song", "starred")
	if first == nil || first == "" {
		t.Fatalf("starred = %v", first)
	}
	if x.status(citta) != "draft" {
		t.Error("an album id changed a song")
	}
	if ids := ids(list(t, x.call("getStarred2"), "starred2", "song")); len(ids) != 1 || ids[0] != luna {
		t.Errorf("getStarred2 songs = %v", ids)
	}
	// already final: a no-op that keeps the timestamp
	x.clk.advance(time.Hour)
	eq(t, "star again", x.callCode("star", "id", luna), 0)
	eq(t, "starred unchanged", dig(t, x.call("getSong", "id", luna), "song", "starred"), first)
	// unstar: final -> to_relisten; others no-op
	eq(t, "unstar", x.callCode("unstar", "id", luna, "id", citta), 0)
	eq(t, "to_relisten", x.status(luna), "to_relisten")
	eq(t, "draft stays", x.status(citta), "draft")
	if v := dig(t, x.call("getSong", "id", luna), "song").(map[string]any)["starred"]; v != nil {
		t.Errorf("still starred: %v", v)
	}
	eq(t, "unstar again", x.callCode("unstar", "id", luna), 0)
	eq(t, "still to_relisten", x.status(luna), "to_relisten")
	// errors
	eq(t, "unknown", x.callCode("star", "id", "no-such"), 70)
	eq(t, "none", x.callCode("star"), 10)
	eq(t, "read-only", x.get("star", keyParams(x.roKey, "id", luna), libIP).code(t), 50)
	// several ids at once
	eq(t, "star many", x.callCode("star", "id", twinA, "id", twinB), 0)
	eq(t, "twinA", x.status(twinA), "final")
	eq(t, "twinB", x.status(twinB), "final")
}

// failingJudger answers UpdateTrack with err; everything else is the real service.
type failingJudger struct {
	judger
	err error
}

func (f failingJudger) UpdateTrack(context.Context, auth.Principal, string, *string, *string) error {
	return f.err
}

func TestSubsonic_StatusErrorsAreError0(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.srv.judg = failingJudger{judger: x.judg, err: &library.ValidationError{Field: "status", Msg: "secret detail"}}
	w := x.get("star", x.rwq("id", luna), libIP)
	eq(t, "http", w.Code, 200)
	eq(t, "code", w.code(t), 0)
	e := dig(t, w.doc(t), "error").(map[string]any)
	eq(t, "message", e["message"], "status change not allowed")
	if strings.Contains(w.Body.String(), "secret detail") {
		t.Error("the validation text leaks")
	}
	x.setStatus(twinA, "final", x.clk.now())
	x.srv.judg = failingJudger{judger: x.judg, err: library.ErrNotFound}
	eq(t, "not found", x.callCode("unstar", "id", twinA), 70)
	x.srv.judg = failingJudger{judger: x.judg, err: errors.New("disk on fire /srv/x")}
	w = x.get("star", x.rwq("id", citta), libIP)
	eq(t, "internal", dig(t, w.doc(t), "error", "message"), "internal error")
	// every failed write is one audit row carrying SUBSONIC_0 or SUBSONIC_70; none claims success
	var zero, seventy int
	for _, row := range x.auditRows() {
		switch row.ErrorCode {
		case "":
			t.Errorf("success row for a failed write: %+v", row)
		case "SUBSONIC_0":
			zero++
		case "SUBSONIC_70":
			seventy++
		}
	}
	eq(t, "SUBSONIC_0 rows", zero, 2)
	eq(t, "SUBSONIC_70 rows", seventy, 1)
}

func TestSubsonic_SetRatingKeepsVerdict(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	verdict := "too long"
	at := x.clk.now().UTC().Format(time.RFC3339)
	x.exec(`INSERT INTO ratings(track_id, source, stars, verdict, principal_kind, principal_id, principal_name, created_at, updated_at)
		VALUES (?, 'human', 3, ?, 'human', 'human:alice', 'alice', ?, ?)`, luna, verdict, at, at)
	stars := func() (int, string) {
		tr, err := x.judg.Ratings(context.Background(), luna)
		if err != nil || tr.Human == nil {
			t.Fatalf("ratings: %v %+v", err, tr)
		}
		s, v := 0, ""
		if tr.Human.Stars != nil {
			s = *tr.Human.Stars
		}
		if tr.Human.Verdict != nil {
			v = *tr.Human.Verdict
		}
		return s, v
	}
	eq(t, "rate 5", x.callCode("setRating", "id", luna, "rating", "5"), 0)
	s, v := stars()
	eq(t, "stars 5", s, 5)
	eq(t, "verdict kept", v, verdict)
	eq(t, "userRating", dig(t, x.call("getSong", "id", luna), "song", "userRating"), 5)
	eq(t, "rate 0", x.callCode("setRating", "id", luna, "rating", "0"), 0)
	s, v = stars()
	eq(t, "stars cleared", s, 0)
	eq(t, "verdict still kept", v, verdict)
	// no human row yet: 0 writes nothing at all
	eq(t, "rate 0 on unrated", x.callCode("setRating", "id", citta, "rating", "0"), 0)
	var n int
	if err := x.st.R.QueryRow(`SELECT COUNT(*) FROM ratings WHERE track_id = ?`, citta).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows for an unrated track = %d (%v)", n, err)
	}
	// a first rating creates the row without a verdict
	eq(t, "rate 4", x.callCode("setRating", "id", citta, "rating", "4"), 0)
	eq(t, "citta rating", dig(t, x.call("getSong", "id", citta), "song", "userRating"), 4)
	// errors
	eq(t, "out of range", x.callCode("setRating", "id", luna, "rating", "6"), 0)
	eq(t, "negative", x.callCode("setRating", "id", luna, "rating", "-1"), 0)
	eq(t, "not a number", x.callCode("setRating", "id", luna, "rating", "x"), 0)
	eq(t, "missing rating", x.callCode("setRating", "id", luna), 10)
	eq(t, "missing id", x.callCode("setRating", "rating", "3"), 10)
	eq(t, "album id", x.callCode("setRating", "id", al(luna), "rating", "3"), 70)
	eq(t, "unknown", x.callCode("setRating", "id", "no-such", "rating", "3"), 70)
	eq(t, "read-only", x.get("setRating", keyParams(x.roKey, "id", luna, "rating", "3"), libIP).code(t), 50)
}

func TestSubsonic_WritesAudited(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	// two calls with different credentials, salts and client names but identical business arguments
	q1 := tokenParams("rwclient", x.rwPW, "salt-aaaaaa", "id", luna)
	q2 := keyParams(x.rwKey, "id", luna)
	q2.Set("c", "other-app")
	if w := x.get("star", q1, libIP); w.code(t) != 0 {
		t.Fatal(w.Body)
	}
	if w := x.get("star", q2, libIP); w.code(t) != 0 {
		t.Fatal(w.Body)
	}
	rows := x.auditRows()
	eq(t, "rows", len(rows), 2)
	if rows[0].ArgsSHA256 != rows[1].ArgsSHA256 {
		t.Errorf("args hash depends on credentials: %s vs %s", rows[0].ArgsSHA256, rows[1].ArgsSHA256)
	}
	clients := map[string]bool{}
	for _, row := range rows {
		eq(t, "op", row.Op, "subsonic:star")
		eq(t, "transport", row.Transport, "subsonic")
		clients[row.Client] = true
	}
	if !clients["subsonic:testapp"] || !clients["subsonic:other-app"] {
		t.Errorf("clients = %v", clients)
	}
	// reads write no row
	before := len(x.auditRows())
	x.call("getSong", "id", luna)
	statusOK(t, x.raw("GET", "getCoverArt", x.rwq("id", luna), nil), 200)
	eq(t, "rows after reads", len(x.auditRows()), before)
	// every other write endpoint writes exactly one row of its own op
	steps := []struct {
		ep string
		kv []string
	}{
		{"unstar", []string{"id", luna}}, {"setRating", []string{"id", luna, "rating", "2"}},
		{"scrobble", []string{"id", luna}}, {"reportPlayback", []string{"mediaId", luna, "state", "starting", "positionMs", "0"}},
		{"createBookmark", []string{"id", luna, "position", "1000"}}, {"deleteBookmark", []string{"id", luna}},
		{"savePlayQueue", []string{"id", luna}}, {"createPlaylist", []string{"name", "audited"}},
	}
	for _, s := range steps {
		before := len(x.auditRows())
		eq(t, s.ep, x.callCode(s.ep, s.kv...), 0)
		rows := x.auditRows()
		if len(rows) != before+1 {
			t.Fatalf("%s wrote %d rows", s.ep, len(rows)-before)
		}
		var found bool
		for _, row := range rows {
			found = found || row.Op == "subsonic:"+s.ep
		}
		if !found {
			t.Errorf("no row for %s", s.ep)
		}
	}
}
