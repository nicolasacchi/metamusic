package sounds_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/perftest"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

// bulkSongs inserts n draft Songs and n takes (recipes, tracks, own Songs) with plain SQL; returns after clearing the
// dirty queue so nothing else runs.
func bulkLibrary(t *testing.T, w *world, n int) {
	t.Helper()
	tx, err := w.st.W.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	run := func(q string) {
		if _, err := tx.Exec(q, n); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	const cte = `WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM s WHERE i < ?) `
	styles := `CASE i % 5 WHEN 0 THEN 'reggae, male vocal, 76bpm' WHEN 1 THEN 'hip hop, female vocal, 95bpm' WHEN 2 THEN 'rock, 120bpm, wobble ' || (i % 7)
		WHEN 3 THEN 'jazz, nostalgic, 140bpm' ELSE 'synthpop, dreamy, 170bpm' END`
	run(cte + `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		SELECT printf('r%08d', i), 'yue2', printf('q%08d', i), json_object('id', printf('q%08d', i), 'seed', i), ` + styles + `, 'x', i, 'full', printf('h%08d', i), printf('c%08d', i), 'import', '2026-10-06T12:00:00Z' FROM s`)
	run(cte + `INSERT INTO tracks(id,slug,title,recipe_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
		SELECT printf('t%08d', i),'slug','Synthetic',printf('r%08d', i),printf('t%08d', i),0,'import','import',10.0,44100,16,2,i,
		printf('m/%08d.flac', i),printf('ms%08d', i),1,'s',printf('f%08d', i),'m','slug','2026-10-06T12:00:00Z','2026-10-06T12:00:00Z' FROM s`)
	run(cte + `INSERT INTO songs(id,root_track_id,title,origin,created_by,created_at,updated_by,updated_at)
		SELECT printf('s%08d', i), printf('t%08d', i), 'Synthetic', 'root', 'system:provenance', '2026-10-06T12:00:00Z', 'system:provenance', '2026-10-06T12:00:00Z' FROM s`)
	run(cte + `INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at)
		SELECT printf('t%08d', i), printf('s%08d', i), 'own', 'system:provenance', '2026-10-06T12:00:00Z' FROM s`)
	if _, err := tx.Exec(`DELETE FROM provenance_dirty`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPerf_TaxonomyRecompute(t *testing.T) {
	budget := perftest.Budget(t, 5*time.Second)
	// serial on purpose: a wall-clock budget measured next to other heavy parallel tests of the package is noise
	w := newWorld(t, true)
	bulkLibrary(t, w, 5000) // 5 000 takes + 5 000 Songs = 10 000 subjects, none computed yet
	start := time.Now()
	res, err := w.sv.Map(context.Background(), agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{{Term: "wobble 3", Facet: "mood", Value: "dark", Op: "add"}}})
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > budget {
		t.Errorf("recomputing 10 000 subjects took %v, budget %v", el, budget)
	}
	if res.RecomputedTakes != 5000 || res.RecomputedSongs != 5000 {
		t.Errorf("recomputed %+v", res)
	}
}

func TestPerf_SearchSounds(t *testing.T) {
	budget := perftest.Budget(t, 300*time.Millisecond)
	// serial on purpose: a wall-clock budget measured next to other heavy parallel tests of the package is noise
	w := newWorld(t, true)
	bulkLibrary(t, w, 10000)
	if _, err := w.sv.Map(context.Background(), agentP, sounds.MapRequest{Mappings: []sounds.MapEntry{{Term: "wobble 3", Facet: "mood", Value: "dark", Op: "add"}}}); err != nil {
		t.Fatal(err)
	}
	f := songs.ListFilter{Sounds: map[string][]string{"genre_family": {"reggae", "jazz"}}}
	start := time.Now()
	fc, err := w.sv.Facets(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > budget {
		t.Errorf("facet counts over 10 000 Songs took %v, budget %v", el, budget)
	}
	if fc.SongsMatching < 3000 {
		t.Errorf("songs_matching %d", fc.SongsMatching)
	}
}
