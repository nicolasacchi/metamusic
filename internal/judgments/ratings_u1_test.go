package judgments_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

var (
	bobEditor    = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", Name: "bob", User: "bob", Scopes: auth.ScopesFor(auth.RoleEditor)}
	carolListen  = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener, ID: "carol", Name: "carol", User: "carol", Scopes: auth.ScopesFor(auth.RoleListener)}
	systemSource = auth.Principal{Kind: auth.KindSystem, ID: "cli", Name: "cli"}
)

// TestRate_PerRater: two users rate one track independently, the Owner's rating (and its device-origin updates) and the
// critic are untouched, each rate call is an upsert on its own (track, source, rater) key, and a principal that cannot rate
// is refused without a write.
func TestRate_PerRater(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	id := lt.TrackLuna // fixture: owner 5 stars, critic 2
	if _, err := e.svc.Rate(ctx, bobEditor, id, ip(3), sp("bob verdict")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Rate(ctx, carolListen, id, ip(1), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Rate(ctx, bobEditor, id, ip(4), sp("bob again")); err != nil { // the same user again updates its row
		t.Fatal(err)
	}
	type row struct {
		Stars   int
		Verdict string
	}
	got := map[string]row{}
	rows, err := e.st.R.Query(`SELECT source||'/'||rater, COALESCE(stars,0), COALESCE(verdict,'') FROM ratings WHERE track_id = ?`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var r row
		if err := rows.Scan(&k, &r.Stars, &r.Verdict); err != nil {
			t.Fatal(err)
		}
		got[k] = r
	}
	if len(got) != 4 || got["human/owner"].Stars != 5 || got["agent/critic"].Stars != 2 || got["human/bob"] != (row{4, "bob again"}) || got["human/carol"].Stars != 1 {
		t.Fatalf("rows = %+v", got)
	}
	// the Owner's device rates as the Owner: it overwrites the owner row, never adds a rater
	if _, err := e.svc.Rate(ctx, device, id, ip(2), nil); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, e, `SELECT COUNT(*) FROM ratings WHERE track_id = ? AND source = 'human'`, id); n != 3 {
		t.Errorf("%d human rows after a device rating, want 3", n)
	}
	if n := scalarInt(t, e, `SELECT stars FROM ratings WHERE track_id = ? AND rater = 'owner'`, id); n != 2 {
		t.Errorf("owner stars = %d after the device rating", n)
	}
	// an agent rates as the critic whatever the humans did
	if _, err := e.svc.Rate(ctx, agent, id, ip(1), nil); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, e, `SELECT COUNT(*) FROM ratings WHERE track_id = ? AND rater = 'critic' AND stars = 1`, id); n != 1 {
		t.Error("agent rating did not update the critic row")
	}
	// a principal with no rater key cannot rate and writes nothing
	before := scalarInt(t, e, `SELECT COUNT(*) FROM ratings`)
	if _, err := e.svc.Rate(ctx, systemSource, id, ip(3), nil); err == nil {
		t.Error("a system principal rated")
	}
	if after := scalarInt(t, e, `SELECT COUNT(*) FROM ratings`); after != before {
		t.Errorf("refused rating wrote rows: %d -> %d", before, after)
	}
	// the stored set shaped per viewer (C306.4)
	raw, _ := e.svc.Ratings(ctx, id)
	if own := library.ShapeRating(carolListen, raw); own.Mine == nil || *own.Mine.Stars != 1 || own.Average != nil || own.Others != nil || own.Critic != nil {
		t.Errorf("listener view %+v", own)
	}
	if own := library.ShapeRating(bobEditor, raw); *own.Mine.Stars != 4 || own.Average == nil || own.Others != nil || own.Critic == nil {
		t.Errorf("editor view %+v", own)
	}
	if own := library.ShapeRating(human, raw); *own.Mine.Stars != 2 || len(own.Others) != 2 || own.Average == nil || own.Average.N != 3 {
		t.Errorf("owner view %+v", own)
	}
}

func scalarInt(t *testing.T, e *env, q string, a ...any) int {
	t.Helper()
	var n int
	if err := e.st.R.QueryRow(q, a...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// TestOrphanRaters: a rater that is no longer configured is reported (name and row count), the Owner and configured users
// are not, and the call only reads.
func TestOrphanRaters(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	for _, p := range []auth.Principal{bobEditor, carolListen} {
		if _, err := e.svc.Rate(ctx, p, lt.TrackLuna, ip(3), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.Rate(ctx, bobEditor, lt.TrackCitta, ip(2), nil); err != nil {
		t.Fatal(err)
	}
	known := func(n string) bool { return n == "carol" }
	got, err := judgmentsOrphans(ctx, e, known)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "bob" || got[0].Rows != 2 {
		t.Fatalf("orphans = %+v", got)
	}
	if got, _ := judgmentsOrphans(ctx, e, func(string) bool { return true }); len(got) != 0 {
		t.Errorf("everyone configured: %+v", got)
	}
}

func judgmentsOrphans(ctx context.Context, e *env, known func(string) bool) ([]judgments.OrphanRater, error) {
	return judgments.OrphanRaters(ctx, e.st.R, known)
}
