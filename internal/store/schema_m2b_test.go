package store

import (
	"context"
	"strings"
	"testing"
)

// m2bTracks inserts one recipe and tracks "t1", "t2".
func m2bTracks(t *testing.T, s *Store) {
	t.Helper()
	r := insertRecipe(t, s, "m2b", 1)
	insertTrack(t, s, "t1", r, "T1", "f1", 100)
	insertTrack(t, s, "t2", r, "T2", "f2", 100)
}

func TestMigration0004_Applies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.UserVersion(ctx); err != nil || v < 4 || v != len(ms) {
		t.Fatalf("version=%d migrations=%d err=%v", v, len(ms), err)
	}
	want := map[string][]string{
		"table": {"duels", "duel_verdicts"},
		"index": {"duels_created", "duels_slot_a", "duels_slot_b", "duel_verdicts_one_human", "duel_verdicts_one_critic",
			"duel_verdicts_source", "duel_verdicts_duel"},
		"trigger": {"duels_immutable", "duels_no_delete", "duel_verdicts_winner", "duel_verdicts_immutable", "duel_verdicts_no_delete"},
	}
	for typ, names := range want {
		for _, n := range names {
			var c int
			if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, typ, n).Scan(&c); err != nil || c != 1 {
				t.Errorf("%s %s missing", typ, n)
			}
		}
	}
	// duel_verdicts has exactly two table-level CHECKs (the column CHECKs are inline).
	var ddl string
	if err := s.R.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='duel_verdicts'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	tableChecks := 0
	for _, l := range strings.Split(ddl, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "CHECK (") {
			tableChecks++
		}
	}
	if tableChecks != 2 {
		t.Errorf("duel_verdicts table CHECKs=%d, want 2", tableChecks)
	}
}

func TestSchema_M2bEnumsGoValidated(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m2bTracks(t, s)
	mustExec(t, s, `INSERT INTO duels(id,slot_a_track_id,slot_b_track_id,swapped,blind,from_ms,to_ms,clip_def,tool_version,match,gain_a_cdb,gain_b_cdb,created_by,created_by_kind,created_at,expires_at)
		VALUES('d1','t1','t2',0,1,0,1000,'mm-clip/1','ffmpeg x','other',0,0,'human:h','human',?,?)`, ts, ts)
	mustExec(t, s, `INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES('t1','token_loops','v','A','{}',?)`, ts)
	mustExec(t, s, `INSERT INTO renditions(track_id,kind,sha256,bytes,path,params_json,created_at) VALUES('t1','spectrogram','x',1,'p','{}',?)`, ts)
}
