package songs_test

import (
	"context"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// TestFilterHideDiscardedPick: a Pick that names a take discarded later is no oracle for a Listener: has_pick, pick_decided
// and the take-level filters (duration here) never read the hidden take.
func TestFilterHideDiscardedPick(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	yes := true
	var dur float64
	var take string
	if err := w.st.R.QueryRow(`SELECT t.id, t.duration_s FROM song_picks p JOIN tracks t ON t.id = p.take_id WHERE p.song_id = ?`, w.fx.Songs["S5"]).Scan(&take, &dur); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, take)
	has := func(ctx context.Context, f songs.ListFilter) bool {
		f.Limit = 100
		p, err := w.svc.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(p.Items, func(it songs.Summary) bool { return it.ID == w.fx.Songs["S5"] })
	}
	carol := auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "carol", User: "carol", Role: auth.RoleListener})
	filters := map[string]songs.ListFilter{
		"pick_decided": {PickDecided: &yes},
		"has_pick":     {HasPick: &yes},
		"duration":     {MinDurationS: fp(dur), MaxDurationS: fp(dur)},
	}
	for name, f := range filters {
		if !has(context.Background(), f) { // control: the Owner's filter still sees the pinned discarded take
			t.Errorf("%s: the Owner lost the Song", name)
		}
		if has(carol, f) {
			t.Errorf("%s: a Listener's filter matched through a discarded Pick", name)
		}
	}
}
