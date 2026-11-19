package songview_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/songs/songstest"
	"github.com/nicolasacchi/metamusic/internal/songview"
)

func carol() context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "carol", User: "carol", Role: auth.RoleListener})
}

func discard(t *testing.T, w *songstest.World, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := w.St.W.Exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGetHidesDiscardedForListener pins C308.2 in get_song for the V1 hand-off (a): no field of the view names or derives
// from a discarded take, a Song of only discarded takes does not exist for a Listener, and the Owner's view is whole.
func TestGetHidesDiscardedForListener(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	root, child := l.PickedTakes[0], l.PickedTakes[1] // the child is the pinned Pick
	get := func(ctx context.Context) (songview.View, error) {
		return l.View.Get(ctx, songstest.HumanP, l.Picked, songview.GetOptions{TakeLimit: 10, IncludeEvents: 10})
	}
	discard(t, l.World, child)
	own, err := get(context.Background())
	if err != nil || own.TakesTotal != 2 || own.Pick == nil || own.Pick.TakeID == nil || *own.Pick.TakeID != child || own.Song.State != "picked" {
		t.Fatalf("the Owner's view: %+v %v", own, err)
	}
	v, err := get(carol())
	if err != nil {
		t.Fatal(err)
	}
	if v.TakesTotal != 1 || len(v.Takes) != 1 || v.Takes[0].ID != root {
		t.Errorf("takes %d/%d: %+v", len(v.Takes), v.TakesTotal, v.Takes)
	}
	if v.Pick != nil && v.Pick.TakeID != nil {
		t.Errorf("the Pick names %v", *v.Pick.TakeID)
	}
	if v.Song.State == "picked" {
		t.Errorf("the state derives from the discarded Pick: %s", v.Song.State)
	}

	// the root discarded too: the Song is gone for a Listener, by id and by its take
	discard(t, l.World, root)
	if _, err := get(carol()); problemOf(err) == nil || problemOf(err).Code != "NOT_FOUND" {
		t.Errorf("a Song of discarded takes for a Listener: %v", err)
	}
	if _, err := l.View.Get(carol(), songstest.HumanP, root, songview.GetOptions{By: "track"}); problemOf(err) == nil || problemOf(err).Code != "NOT_FOUND" {
		t.Errorf("by a discarded take: %v", err)
	}
	if v, err := get(context.Background()); err != nil || v.TakesTotal != 2 {
		t.Errorf("the Owner's view of a Song of discarded takes: %v %+v", err, v.TakesTotal)
	}
}

// TestGetHidesDiscardedRoot: a root take that is discarded is not named by the Song core, its root list or its lineage block
// for a Listener, while a visible take keeps the Song.
func TestGetHidesDiscardedRoot(t *testing.T) {
	t.Parallel()
	l := songstest.NewLib(t)
	root, child := l.PickedTakes[0], l.PickedTakes[1]
	discard(t, l.World, root)
	own, err := l.View.Get(context.Background(), songstest.HumanP, l.Picked, songview.GetOptions{TakeLimit: 10})
	if err != nil || own.Song.RootTrackID == nil || *own.Song.RootTrackID != root || own.Lineage == nil {
		t.Fatalf("the Owner's core: %+v %v", own.Song, err)
	}
	v, err := l.View.Get(carol(), songstest.HumanP, l.Picked, songview.GetOptions{TakeLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if v.Song.RootTrackID != nil {
		t.Errorf("the core names the discarded root: %v", *v.Song.RootTrackID)
	}
	for _, id := range v.Song.RootTrackIDs {
		if id == root {
			t.Errorf("the root list names the discarded root")
		}
	}
	if v.Lineage != nil {
		t.Errorf("a lineage block rooted at a discarded take: %+v", v.Lineage)
	}
	if len(v.Takes) != 1 || v.Takes[0].ID != child {
		t.Errorf("takes: %+v", v.Takes)
	}
}
