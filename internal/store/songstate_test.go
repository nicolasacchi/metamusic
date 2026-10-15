package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestSongStateSQL runs the shared state fragment over a settled library: one row per Song, the four states of D-L1-18,
// and a human rating moves a rendered Song to judged.
func TestSongStateSQL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	librarytest.FixtureL1(t, st, librarytest.FixedNow)
	prov := provenance.New(provenance.Options{Store: st, Now: func() time.Time { return librarytest.FixedNow }})
	if _, err := prov.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	states := func() map[string]int {
		rows, err := st.R.QueryContext(ctx, `SELECT `+store.SongStateCase+`, COUNT(*) FROM (`+store.SongStateSQL+`) st GROUP BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]int{}
		for rows.Next() {
			var s string
			var n int
			if err := rows.Scan(&s, &n); err != nil {
				t.Fatal(err)
			}
			out[s] = n
		}
		return out
	}
	var songs int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM songs`).Scan(&songs); err != nil {
		t.Fatal(err)
	}
	before := states()
	total := 0
	for _, n := range before {
		total += n
	}
	if total != songs || before["draft"] == 0 || before["rendered"]+before["judged"] == 0 {
		t.Fatalf("states %v over %d Songs", before, songs)
	}
	// rate a take of a rendered Song: it becomes judged
	var track string
	if err := st.R.QueryRowContext(ctx, `SELECT ts.track_id FROM take_songs ts JOIN (`+store.SongStateSQL+`) st ON st.song_id = ts.song_id
		WHERE st.takes > 0 AND st.judged = 0 LIMIT 1`).Scan(&track); err != nil {
		t.Fatal(err)
	}
	librarytest.NewScene(t, st, librarytest.FixedNow).Rate(track, 4)
	after := states()
	if after["judged"] != before["judged"]+1 || after["rendered"] != before["rendered"]-1 {
		t.Errorf("rating a take: %v -> %v, want one Song to move from rendered to judged", before, after)
	}
}
