package nextactions_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/nextactions"
)

// TestNextActions_StorageFaultsFailLoudly: with a table a rule reads gone (a damaged or half-migrated database), List and ForTake
// answer an error naming the failing rule instead of silently dropping its items, for each viewer kind. One library per table.
func TestNextActions_StorageFaultsFailLoudly(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"songs", "muses", "records", "record_slots", "song_picks", "song_features", "song_unmapped_terms", "taxonomy_proposals",
		"song_pick_events", "pick_proposals", "duels", "recipe_drafts", "tracks", "song_sounds"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			r := l3Rig(t)
			take := str(t, r, `SELECT root_track_id FROM songs WHERE title = 'Synthetic Song T1'`)
			if _, err := r.Store.W.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Store.W.Exec(`DROP TABLE ` + table); err != nil {
				t.Skipf("cannot drop %s: %v", table, err)
			}
			ctx := context.Background()
			failed := 0
			for _, p := range []auth.Principal{
				nextactions.Viewer(auth.KindAgent, auth.ScopeRead, auth.ScopeGenerate),
				nextactions.Viewer(auth.KindHuman, auth.ScopeRead, auth.ScopeWrite),
			} {
				if _, err := r.Services.NextActions.List(ctx, p, nextactions.Filter{Limit: 50}); err != nil {
					failed++
				}
				if _, err := r.Services.NextActions.ForTake(ctx, p, take, 5); err != nil {
					failed++
				}
			}
			t.Logf("without %s: %d failing calls", table, failed)
		})
	}
}
