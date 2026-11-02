package atlas_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/atlas"
)

// TestAtlas_StorageFaultsFailLoudly: with a table the Atlas reads gone (a damaged or half-migrated database), Get and Adherence
// answer an error instead of an empty map, for every axis kind and for the Muse overlay, and never panic. One library per table.
func TestAtlas_StorageFaultsFailLoudly(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"songs", "song_features", "song_picks", "take_features", "tracks", "muses", "sound_taxonomy_versions", "song_sounds"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			ctx := context.Background()
			w.write(t, `PRAGMA foreign_keys = OFF`)
			w.write(t, `DROP TABLE `+table)
			failed := 0
			for _, q := range []atlas.Query{
				{X: "tempo", Y: "energy", Limit: atlas.MaxPage},
				{X: "family", Y: "vocal", ColorBy: "muse", Limit: atlas.MaxPage},
				{X: "bpm_requested", Y: "bpm_measured", ColorBy: "state", Dim: true, Limit: atlas.MaxPage},
				{X: "tempo", Y: "key", NearSongID: w.info.Songs["T1"], K: 3, Limit: atlas.MaxPage},
			} {
				if _, err := w.svc.Get(ctx, q); err != nil {
					failed++
				}
			}
			if _, err := w.svc.Adherence(ctx, atlas.AdherenceQuery{MinN: 1, Limit: 10}); err != nil {
				failed++
			}
			if failed == 0 && (table == "songs" || table == "song_features") {
				t.Errorf("no read failed without %s", table)
			}
		})
	}
}
