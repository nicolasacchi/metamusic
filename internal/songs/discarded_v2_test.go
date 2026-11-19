package songs_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

func listener() context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "carol", User: "carol", Role: auth.RoleListener})
}

func (w *l2w) summary(ctx context.Context, id string) (songs.Summary, bool) {
	w.t.Helper()
	p, err := w.svc.List(ctx, songs.ListFilter{Limit: 100})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, it := range p.Items {
		if it.ID == id {
			return it, true
		}
	}
	return songs.Summary{}, false
}

// TestListHidesAllDiscardedSongs pins the V1 hand-off (a): a Song whose takes are all discarded does not list, count or
// total for a Listener, while a Song with a visible take and a Song with no take at all still do; the Owner keeps all.
func TestListHidesAllDiscardedSongs(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	song := w.fx.Songs["S5"]
	w.exec(`UPDATE tracks SET status = 'discarded' WHERE id IN (SELECT track_id FROM take_songs WHERE song_id = ?)`, song)
	if _, ok := w.summary(context.Background(), song); !ok {
		t.Fatalf("the Owner lost the Song")
	}
	if _, ok := w.summary(listener(), song); ok {
		t.Errorf("a Listener lists a Song of discarded takes")
	}
	owner, err := w.svc.Count(context.Background(), songs.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := w.svc.Count(listener(), songs.ListFilter{}); err != nil || n != owner-1 {
		t.Errorf("Listener count %d (%v), Owner %d", n, err, owner)
	}
	// a Song that has no take at all stays listed (a draft is not a hidden take)
	var draftSong string
	if err := w.st.R.QueryRow(`SELECT s.id FROM songs s WHERE s.retired_at IS NULL AND NOT EXISTS (SELECT 1 FROM take_songs ts WHERE ts.song_id = s.id) LIMIT 1`).Scan(&draftSong); err != nil {
		t.Fatalf("no take-less Song in the fixture: %v", err)
	}
	if _, ok := w.summary(listener(), draftSong); !ok {
		t.Errorf("a take-less Song vanished for a Listener")
	}
	// one visible take among discarded ones keeps the Song, with its visible count
	var keep string
	if err := w.st.R.QueryRow(`SELECT s.id FROM songs s JOIN take_songs ts ON ts.song_id = s.id WHERE s.id <> ? GROUP BY s.id HAVING COUNT(*) >= 2 LIMIT 1`, song).Scan(&keep); err == nil {
		w.exec(`UPDATE tracks SET status = 'discarded' WHERE id = (SELECT track_id FROM take_songs WHERE song_id = ? ORDER BY track_id LIMIT 1)`, keep)
		o, _ := w.summary(context.Background(), keep)
		l, ok := w.summary(listener(), keep)
		if !ok || l.Takes != o.Takes-1 {
			t.Errorf("a Song with a visible take: listed %v, takes %d vs %d", ok, l.Takes, o.Takes)
		}
	}
}

// TestListHidesDiscardedPickAndRoot pins that no Summary field of a Listener names a discarded take: a Pick on one is
// dropped, a discarded root is null, and the state is computed over visible takes.
func TestListHidesDiscardedPickAndRoot(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	var song, pick, root string
	if err := w.st.R.QueryRow(`SELECT s.id, p.take_id, s.root_track_id FROM songs s JOIN song_picks p ON p.song_id = s.id
		WHERE p.take_id IS NOT NULL AND (SELECT COUNT(*) FROM take_songs ts WHERE ts.song_id = s.id) >= 2 ORDER BY s.id LIMIT 1`).Scan(&song, &pick, &root); err != nil {
		t.Fatal(err)
	}
	o, _ := w.summary(context.Background(), song)
	if o.Pick == nil || o.Pick.TakeID != pick || o.RootTrackID == nil {
		t.Fatalf("setup: %+v", o)
	}
	w.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, pick)
	l, ok := w.summary(listener(), song)
	if !ok {
		t.Fatalf("the Song vanished though a take is visible")
	}
	if l.Pick != nil {
		t.Errorf("a Listener's Summary names the discarded Pick take: %+v", l.Pick)
	}
	if l.State == songs.State("picked") {
		t.Errorf("a Listener's state derives from the discarded Pick")
	}
	if pick == root && l.RootTrackID != nil {
		t.Errorf("a Listener's Summary names the discarded root: %v", *l.RootTrackID)
	}
	if o2, _ := w.summary(context.Background(), song); o2.Pick == nil || o2.Pick.TakeID != pick {
		t.Errorf("the Owner's Pick vanished")
	}
	// the root, discarded on its own (when it is not the Pick take)
	if pick != root {
		w.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, root)
		if l, ok := w.summary(listener(), song); ok && l.RootTrackID != nil {
			t.Errorf("a Listener's Summary names the discarded root: %v", *l.RootTrackID)
		}
	}
}
