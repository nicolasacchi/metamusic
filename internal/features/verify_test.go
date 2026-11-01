package features_test

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/perftest"
)

// plant writes a fault with raw SQL and empties the Reconciler queue the write triggered, so the queue-gated checks run.
func (w *world) plant(q string, args ...any) {
	w.t.Helper()
	w.exec(q, args...)
	w.exec(`DELETE FROM provenance_dirty`)
}

func codeSet(an []features.Anomaly) []string {
	var out []string
	for _, a := range an {
		out = append(out, a.Code)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// TestVerify_L3_DetectsEachAnomaly: a consistent library verifies empty; each of F1 to F6 has a planted fault that is found
// under its own code, and nothing else is reported.
func TestVerify_L3_DetectsEachAnomaly(t *testing.T) {
	t.Parallel()
	w, info := newL3World(t)
	w.verifyEmpty()
	ctx := context.Background()
	rec := w.str(`SELECT id FROM records LIMIT 1`)
	if rec == "" {
		t.Fatal("the L2 fixture has no Record")
	}
	h64 := strings.Repeat("a", 64)
	// F1
	w.plant(`DELETE FROM song_features WHERE song_id = ?`, info.Songs["T1"])                                     // song_features_missing
	w.plant(`UPDATE song_features SET subject_id = 'x' WHERE song_id = ?`, info.Songs["T2"])                     // song_features_subject
	w.plant(`UPDATE song_features SET subject_note = 'all_takes_discarded' WHERE song_id = ?`, info.Songs["T3"]) // song_features_note
	w.plant(`INSERT INTO song_features(song_id,subject_kind,subject_id,digest,feature_def,computed_at)
		SELECT s.id,'draft','x',?,'mm-features/1','t' FROM songs s WHERE s.retired_at IS NULL AND NOT EXISTS (SELECT 1 FROM song_features f WHERE f.song_id = s.id)
		AND NOT EXISTS (SELECT 1 FROM take_songs ts WHERE ts.song_id = s.id) AND s.draft_id IS NULL LIMIT 1`, h64) // song_features_extra (if such a Song exists)
	// F2
	w.plant(`DELETE FROM take_features WHERE track_id = ?`, info.Takes["T4"])                       // take_features_missing, song_features_take_missing
	w.plant(`UPDATE take_features SET bpm_basis = 'measured' WHERE track_id = ?`, info.Takes["u1"]) // take_feature_unbacked
	w.plant(`UPDATE take_features SET bpm_octave = 1 WHERE track_id = ?`, info.Takes["T5"])         // bpm_octave_invalid (+ copy)
	w.plant(`UPDATE take_features SET bpm_measured_x100 = NULL, bpm_octave = 1 WHERE track_id = ?`, info.Takes["T6"])
	// F3
	w.plant(`UPDATE song_features SET bpm_x100 = bpm_x100 + 100 WHERE song_id = ?`, info.Songs["T7"]) // song_features_copy
	// F4
	w.plant(`INSERT INTO record_sequences(id,record_id,record_revision,strategy,first_song_id,last_song_id,order_before_json,order_after_json,exact,cost_input,cost_output,coverage_json,digest,explanation_json,actor,actor_kind,actor_name,at)
		VALUES('01900000-0000-7000-8000-00000000f401',?,1,'arc',NULL,NULL,'["a","b"]','["a","c"]',1,1,5,'{}',?,'{}','human:x','human','x','2026-10-06T12:00:00Z')`, rec, h64)
	w.plant(`INSERT INTO record_changes(id,record_id,seq,at,kind,detail_json,actor,actor_kind,actor_name)
		VALUES('01900000-0000-7000-8000-00000000f402',?,(SELECT COALESCE(MAX(seq),0)+1 FROM record_changes WHERE record_id = ?),'2026-10-06T12:00:00Z','renamed','{"sequence_id":"nope"}','human:x','human','x')`, rec, rec)
	// F5
	w.plant(`INSERT INTO taxonomy_proposals(id,state,base_version,rationale,mappings_json,mappings_digest,impact_json,principal_id,principal_kind,principal_name,created_at,decided_at,decided_by,accepted_json,applied_version)
		VALUES('01900000-0000-7000-8000-00000000f501','accepted',1,'r','[{"term":"a","facet":"mood","value":"dark","op":"add"}]',?,'{}','agent:1','agent','a','2026-10-06T12:00:00Z','2026-10-06T12:00:00Z','human:x','[0]',99)`, h64)
	w.plant(`INSERT INTO taxonomy_proposals(id,state,base_version,rationale,mappings_json,mappings_digest,impact_json,principal_id,principal_kind,principal_name,created_at)
		VALUES('01900000-0000-7000-8000-00000000f502','open',1,'r','[{"term":1}]',?,'{}','agent:2','agent','a','2026-10-06T12:00:00Z')`, strings.Repeat("b", 64))
	for i := range 50 {
		w.plant(fmt.Sprintf(`INSERT INTO taxonomy_proposals(id,state,base_version,rationale,mappings_json,mappings_digest,impact_json,principal_id,principal_kind,principal_name,created_at)
			VALUES('01900000-0000-7000-8000-0000000f60%02d','open',1,'r','[{"term":"t","facet":"mood","value":"dark","op":"add"}]',?,'{}','agent:%d','agent','a','2026-10-06T12:00:00Z')`, i, i), fmt.Sprintf("%064d", i))
	}
	// F6
	w.plant(`UPDATE song_sounds SET ord = 0 WHERE song_id = ?`, info.Songs["T8"])

	an, err := w.svc.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := codeSet(an)
	want := []string{"bpm_octave_invalid", "proposal_malformed", "proposal_version_missing", "proposals_open_over_limit", "sequence_change_orphan", "sequence_cost_regressed",
		"sequence_not_permutation", "song_features_copy", "song_features_missing", "song_features_note", "song_features_subject", "song_features_take_missing",
		"sound_ord_duplicate", "take_feature_unbacked", "take_features_missing"}
	for _, c := range want {
		if !slices.Contains(got, c) {
			t.Errorf("code %s not found; got %v", c, got)
		}
	}
	for _, c := range got {
		if !slices.Contains(want, c) && c != "song_features_extra" {
			t.Errorf("unexpected code %s", c)
		}
	}
	for _, a := range an {
		if a.Subject == "" {
			t.Errorf("anomaly %s has no subject", a.Code)
		}
	}
	// the queue-gated checks stay quiet while the queue is not empty; F4 and F5 do not
	w.exec(`INSERT INTO provenance_dirty(kind, id, at) VALUES('track', 'x', 'now')`)
	an, err = w.svc.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := codeSet(an); slices.Contains(got, "song_features_missing") || !slices.Contains(got, "sequence_not_permutation") {
		t.Errorf("with a non-empty queue: %v", got)
	}
}

// TestVerify_L3_EmptyOnPlainLibrary: a library with no L3 rows verifies empty.
func TestVerify_L3_EmptyOnPlainLibrary(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	w.verifyEmpty()
}

// TestPerf_Features_Settle: deriving the rows of 5 000 takes and 5 000 Songs in one transaction stays inside the budget.
func TestPerf_Features_Settle(t *testing.T) {
	budget := perftest.BudgetRace(t, 6*time.Second, 25)
	w := newWorld(t, true)
	const n = 5000
	tx, err := w.st.W.Begin()
	if err != nil {
		t.Fatal(err)
	}
	const cte = `WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM s WHERE i < ?) `
	styles := `CASE i % 5 WHEN 0 THEN 'reggae, male vocal, 76bpm' WHEN 1 THEN 'hip hop, female vocal, in A minor, 95bpm' WHEN 2 THEN 'rock, 120bpm'
		WHEN 3 THEN 'jazz, nostalgic, 140bpm' ELSE 'synthpop, dreamy, 170bpm' END`
	for _, q := range []string{
		cte + `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
			SELECT printf('r%08d', i), 'yue2', printf('q%08d', i), json_object('id', printf('q%08d', i), 'seed', i), ` + styles + `, 'x', i, 'full', printf('h%08d', i), printf('c%08d', i), 'import', '2026-10-06T12:00:00Z' FROM s`,
		cte + `INSERT INTO tracks(id,slug,title,recipe_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
			master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
			SELECT printf('t%08d', i),'slug','Synthetic',printf('r%08d', i),printf('t%08d', i),0,'import','import',10.0,44100,16,2,i,
			printf('m/%08d.flac', i),printf('ms%08d', i),1,'s',printf('f%08d', i),'m','slug','2026-10-06T12:00:00Z','2026-10-06T12:00:00Z' FROM s`,
		cte + `INSERT INTO songs(id,root_track_id,title,origin,created_by,created_at,updated_by,updated_at)
			SELECT printf('s%08d', i), printf('t%08d', i), 'Synthetic', 'root', 'system:provenance', '2026-10-06T12:00:00Z', 'system:provenance', '2026-10-06T12:00:00Z' FROM s`,
		cte + `INSERT INTO song_roots(root_track_id,song_id,via,attached_by,attached_at)
			SELECT printf('t%08d', i), printf('s%08d', i), 'own', 'system:provenance', '2026-10-06T12:00:00Z' FROM s`,
	} {
		if _, err := tx.Exec(q, n); err != nil {
			_ = tx.Rollback()
			t.Fatalf("bulk setup: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	w.exec(`DELETE FROM provenance_dirty`)
	var takes, songs []string
	for i := 1; i <= n; i++ {
		takes, songs = append(takes, fmt.Sprintf("t%08d", i)), append(songs, fmt.Sprintf("s%08d", i))
	}
	w.inTx(func(ctx context.Context, tx *sql.Tx, env features.Env) error {
		_, err := w.sd.ComputeTx(ctx, tx, env, takes, songs)
		return err
	})
	sw := perftest.Start()
	var ch features.Changes
	w.inTx(func(ctx context.Context, tx *sql.Tx, env features.Env) (err error) {
		ch, err = w.svc.ComputeTx(ctx, tx, env, takes, songs)
		return err
	})
	t.Logf("derived %d takes and %d Songs in %v", n, n, sw.Elapsed())
	if el := sw.Elapsed(); el > budget {
		t.Errorf("deriving %d takes and %d Songs took %v, budget %v", n, n, el, budget)
	}
	if ch.Takes != n || ch.Songs != n {
		t.Errorf("changes %+v, want %d and %d", ch, n, n)
	}
}
