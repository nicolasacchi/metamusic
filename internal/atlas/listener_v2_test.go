package atlas_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestAtlas_ListenerHidesDiscarded pins the V1 hand-off (a) for the Atlas: a Song whose takes are all discarded is not
// plotted or counted for a Listener (also with dim, and as near_song_id), and the state of every other Song derives from
// its visible takes only; the Owner's Atlas is unchanged.
func TestAtlas_ListenerHidesDiscarded(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	carol := auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "carol", User: "carol", Role: auth.RoleListener})
	q := atlas.Query{X: "family", Y: "vocal", Limit: atlas.MaxPage, Filter: songs.ListFilter{IncludeHidden: true, IncludeArchived: true}}
	get := func(ctx context.Context, q atlas.Query) atlas.Atlas {
		t.Helper()
		a, err := w.svc.Get(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	before := get(context.Background(), q)

	// every take of the first plotted Song, and the first take of a second Song that has several
	var gone, partial string
	for _, p := range before.Points {
		var n int
		if err := w.st.R.QueryRow(`SELECT COUNT(*) FROM take_songs WHERE song_id = ?`, p.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		switch {
		case gone == "" && n >= 1:
			gone = p.ID
		case partial == "" && n >= 2:
			partial = p.ID
		}
	}
	if gone == "" {
		t.Fatal("the fixture has no Song with a take")
	}
	w.write(t, `UPDATE tracks SET status = 'discarded' WHERE id IN (SELECT track_id FROM take_songs WHERE song_id = ?)`, gone)
	if partial != "" {
		w.write(t, `UPDATE tracks SET status = 'discarded' WHERE id = (SELECT track_id FROM take_songs WHERE song_id = ? ORDER BY track_id LIMIT 1)`, partial)
	}

	owner := get(context.Background(), q)
	if len(owner.Points) != len(before.Points) {
		t.Errorf("the Owner's Atlas changed: %d -> %d points", len(before.Points), len(owner.Points))
	}
	l := get(carol, q)
	have := map[string]atlas.Point{}
	for _, p := range l.Points {
		have[p.ID] = p
	}
	if _, ok := have[gone]; ok {
		t.Errorf("a Song of discarded takes is plotted for a Listener")
	}
	if len(l.Points) != len(owner.Points)-1 || l.Totals.ScopeTotal != owner.Totals.ScopeTotal-1 {
		t.Errorf("points %d/%d, scope total %d/%d", len(l.Points), len(owner.Points), l.Totals.ScopeTotal, owner.Totals.ScopeTotal)
	}
	// the state equals the visible state fragment for every plotted Song
	want := map[string]int{}
	rows, err := w.st.R.Query(`SELECT st.song_id, CASE WHEN st.picked = 1 THEN 3 WHEN st.judged = 1 THEN 2 WHEN st.takes > 0 THEN 1 ELSE 0 END FROM (` + store.SongStateSQLVisible + `) st`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			t.Fatal(err)
		}
		want[id] = n
	}
	rows.Close()
	for _, p := range l.Points {
		if want[p.ID] != p.ST {
			t.Errorf("%s: a Listener's state %d, the visible state says %d", p.ID, p.ST, want[p.ID])
		}
	}
	// with dim the base scope hides it too
	dq := q
	dq.Dim = true
	for _, p := range get(carol, dq).Points {
		if p.ID == gone {
			t.Errorf("dim: a Song of discarded takes is plotted")
		}
	}
	// near_song_id of such a Song is unknown to a Listener, known to the Owner
	nq := q
	nq.NearSongID = gone
	if _, err := w.svc.Get(carol, nq); err == nil {
		t.Errorf("near_song_id names a Song of discarded takes for a Listener")
	}
	if _, err := w.svc.Get(context.Background(), nq); err != nil {
		t.Errorf("the Owner's near_song_id: %v", err)
	}
}
