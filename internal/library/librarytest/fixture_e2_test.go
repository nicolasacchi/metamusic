package librarytest

import "testing"

// TestFixtureE2_Shape: the rows of the E2 world by SQL, on top of every real-data shape L3 already holds (draft-only Songs, a
// reference FLAC, a lossy import, Songs with and without Picks).
func TestFixtureE2_Shape(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	fx := FixtureE2(t, st, FixedNow)
	for q, want := range map[string]string{
		`SELECT COUNT(*) FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE r.engine = 'acestep15'`:           "4",
		`SELECT COUNT(*) FROM jobs WHERE engine = 'acestep15' AND backend = 'fake-acestep' AND state = 'completed'`: "4",
		`SELECT COUNT(*) FROM batches WHERE engine = 'acestep15' AND backend = 'fake-acestep'`:                      "4",
		`SELECT COUNT(*) FROM artifacts WHERE name = 'lyrics.lrc' OR name GLOB 'stem-*.flac'`:                       "2",
		`SELECT COUNT(*) FROM mutations WHERE operator IN ('engine_port','reference_render','repaint','compose_new') AND child_track_id IN (SELECT id FROM tracks WHERE job_id IS NOT NULL AND job_id IN (SELECT id FROM jobs WHERE engine='acestep15'))`: "4",
		`SELECT COUNT(*) FROM records WHERE status = 'draft' AND title = 'Synthetic E2 Draft'`:   "1",
		`SELECT COUNT(*) FROM record_slots WHERE record_id = '` + fx.Record + `'`:                "3",
		`SELECT items_count FROM playlists WHERE id = '` + fx.Playlist + `'`:                     "3",
		`SELECT stars FROM ratings WHERE source = 'human' AND track_id = '` + fx.Golden[0] + `'`: "5",
		`SELECT parent_track_id FROM tracks WHERE id = '` + fx.Port + `'`:                        fx.Golden[0],
		`SELECT parent_track_id FROM tracks WHERE id = '` + fx.Repaint + `'`:                     fx.Port,
		`SELECT root_track_id FROM tracks WHERE id = '` + fx.Reference + `'`:                     fx.Root,
	} {
		got := ""
		if err := st.R.QueryRow(q).Scan(&got); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", q, got, want)
		}
	}
	for q, min := range map[string]int{`SELECT COUNT(*) FROM songs WHERE origin = 'draft'`: 5, `SELECT COUNT(*) FROM tracks WHERE import_rule = 'reference' AND lossy = 1`: 1,
		`SELECT COUNT(*) FROM tracks WHERE import_rule = 'reference' AND lossy = 0`: 1} {
		if n := count(t, st, q); n < min {
			t.Errorf("%s = %d, want at least %d", q, n, min)
		}
	}
	if fx.Env != E2Env {
		t.Error("env")
	}
}
