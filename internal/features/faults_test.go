package features_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestFeatures_StorageFaultsFailLoudly: when a table the feature layer reads or writes is gone (a damaged or half-migrated
// database), Verify answers an error naming its step instead of an empty success, and ComputeTx and SweepStaleTx never panic
// (the transaction of a failing step is rolled back by the caller). One run per table, each on its own settled library.
func TestFeatures_StorageFaultsFailLoudly(t *testing.T) {
	t.Parallel()
	// scores and recipes are read by the writers only, not by Verify
	for _, table := range []string{"song_features", "take_features", "record_sequences", "record_changes", "taxonomy_proposals",
		"sound_taxonomy_versions", "song_sounds", "take_songs", "tracks", "analyses", "recipes", "songs", "scores", "song_picks", "recipe_drafts"} {
		verifyFails := table != "scores" && table != "recipes"
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			w, info := newL3World(t)
			ctx := context.Background()
			w.exec(`PRAGMA foreign_keys = OFF`)
			if table == "take_songs" { // a view over take_sources
				w.exec(`DROP VIEW take_songs`)
			} else {
				w.exec(`DROP TABLE ` + table)
			}
			if _, err := w.svc.Verify(ctx); err == nil && verifyFails {
				t.Errorf("Verify without %s answered no error", table)
			}
			tracks, songs := []string{info.Takes["T1"], info.Takes["u1"]}, []string{info.Songs["T1"], info.Songs["d1"]}
			for _, ids := range []struct{ tracks, songs []string }{{tracks, nil}, {nil, songs}, {tracks, songs}} {
				tx, err := w.st.W.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = w.svc.ComputeTx(ctx, tx, store.Env{Now: w.now()}, ids.tracks, ids.songs)
				_ = tx.Rollback()
			}
			tx, err := w.st.W.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.svc.SweepStaleTx(ctx, tx, store.Env{Now: w.now()}, 50)
			_ = tx.Rollback()
		})
	}
}
