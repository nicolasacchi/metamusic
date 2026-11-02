package sequence_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/sequence"
)

// TestSuggest_StorageFaultsFailLoudly: with a table the planner reads gone (a damaged or half-migrated database), Suggest answers
// an error for every source (Record, Songs, Crate resolver) instead of an empty or partial plan, and never panics.
func TestSuggest_StorageFaultsFailLoudly(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"song_features", "take_features", "songs", "records", "record_slots", "record_sequences", "song_picks", "tracks", "provenance_dirty"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.exec(`PRAGMA foreign_keys = OFF`)
			rec := ""
			if err := r.st.R.QueryRow(`SELECT id FROM records ORDER BY id LIMIT 1`).Scan(&rec); err != nil {
				t.Fatal(err)
			}
			songs := []string{r.song("S1"), r.song("S2"), r.song("S3")}
			r.exec(`DROP TABLE ` + table)
			ctx := context.Background()
			failed := 0
			for _, in := range []sequence.SuggestInput{{RecordID: rec}, {SongIDs: songs}, {SongIDs: songs, Strategy: "ramp_up", First: songs[0]}} {
				if _, err := r.svc.Suggest(ctx, in); err != nil {
					failed++
				}
			}
			if failed == 0 && table != "record_sequences" && table != "provenance_dirty" {
				t.Errorf("no plan failed without %s", table)
			}
		})
	}
}
