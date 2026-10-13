package store

import (
	"context"
	"strings"
	"testing"
)

const ts = "2026-01-01T00:00:00Z"

func exec(t *testing.T, s *Store, q string, args ...any) error {
	t.Helper()
	_, err := s.W.ExecContext(context.Background(), q, args...)
	return err
}

func mustExec(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if err := exec(t, s, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func expectErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("want error containing %q, got %v", sub, err)
	}
}

func recipeJSON(id string, seed int) string {
	return `{"cot":"full","id":"` + id + `","lyrics":"[Verse]\ncittà la","seed":` + itoa(seed) + `,"style":"pop"}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func insertRecipe(t *testing.T, s *Store, rid string, seed int) string {
	t.Helper()
	id := "r-" + rid
	mustExec(t, s, `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, "yue2", rid, recipeJSON(rid, seed), "pop", "[Verse]\ncittà la", seed, "full", "h-"+rid, "c-"+rid, "import", ts)
	return id
}

func insertRun(t *testing.T, s *Store, id, state string) {
	t.Helper()
	mustExec(t, s, `INSERT INTO import_runs(id,started_at,heartbeat_at,state,principal,inbox_label,plan_digest) VALUES(?,?,?,?,?,?,?)`,
		id, ts, ts, state, "cli", "inbox", "d")
}

func insertTrack(t *testing.T, s *Store, id, recipeID, title, frames string, total int) {
	t.Helper()
	mustExec(t, s, `INSERT INTO tracks(id,slug,title,recipe_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
		VALUES(?,?,?,?,?,0,'import','import',10.0,44100,16,2,?,?,?,1,'s',?,'m','r',?,?)`,
		id, "slug", title, recipeID, id, total, "masters/"+id, "sha-"+id, frames, ts, ts)
}

func TestMigration0002_Applies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	if v, err := s.UserVersion(ctx); err != nil || v < 2 {
		t.Fatalf("version=%d err=%v", v, err)
	}
	want := map[string][]string{
		"table": {"import_runs", "recipes", "recipe_sources", "recipe_refs", "tracks", "track_style_terms", "mutations", "renditions",
			"analyses", "artifacts", "scores", "sections", "recipe_drafts", "draft_collections", "device_profiles", "tracks_fts", "drafts_fts"},
		"index": {"import_runs_started", "recipes_request_id", "recipes_core_hash", "recipe_sources_recipe", "tracks_audio_identity",
			"tracks_source_sha", "tracks_root", "tracks_parent", "tracks_recipe", "tracks_recorded", "tracks_duration", "track_style_terms_term",
			"mutations_child", "renditions_sha", "recipe_drafts_import_once", "recipe_drafts_status_group", "draft_collections_name"},
		"trigger": {"import_runs_no_delete", "import_runs_final", "import_runs_frozen", "recipes_immutable", "recipes_no_delete",
			"recipe_sources_immutable", "recipe_sources_no_delete", "recipe_refs_immutable", "recipe_refs_no_delete", "tracks_no_delete",
			"tracks_frozen", "track_style_terms_immutable", "track_style_terms_no_delete", "mutations_immutable", "mutations_no_delete",
			"renditions_no_delete", "analyses_immutable", "analyses_no_delete", "artifacts_immutable", "artifacts_no_delete",
			"scores_immutable", "scores_no_delete", "sections_immutable", "sections_no_delete", "recipe_drafts_no_delete",
			"recipe_drafts_frozen", "device_profiles_no_delete", "device_profiles_frozen", "tracks_fts_ai", "tracks_fts_au",
			"drafts_fts_ai", "drafts_fts_au"},
	}
	for typ, names := range want {
		for _, n := range names {
			var c int
			if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, typ, n).Scan(&c); err != nil || c != 1 {
				t.Errorf("%s %s missing", typ, n)
			}
		}
	}
}

func TestSchema_GrowingEnumsNotChecked(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	mustExec(t, s, `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES('r1','other','a','{"id":"a","seed":1}','s','l',1,'x','h','c','import',?)`, ts)
	insertTrack(t, s, "t1", "r1", "T", "f1", 100)
	mustExec(t, s, `INSERT INTO renditions(track_id,kind,sha256,bytes,path,params_json,created_at) VALUES('t1','spectrogram','x',1,'p','{}',?)`, ts)
	mustExec(t, s, `INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES('t1','clipping','v','D','{}',?)`, ts)
	mustExec(t, s, `INSERT INTO device_profiles(device_id,label,user_agent,quality,quality_source,flac_can_play,probe_version,created_by,updated_by,created_at,updated_at)
		VALUES('device-01','l','ua','opus','default','',1,'p','p',?,?)`, ts, ts)
}

func TestRecipes_Checks(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	ins := func(reqID, js string, seed int64) error {
		return exec(t, s, `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "r"+reqID+js, "yue2", reqID, js, "s", "l", seed, "full", "h"+reqID+js, "c", "import", ts)
	}
	expectErr(t, ins("a", `{"id":"b","seed":1}`, 1), "CHECK")
	expectErr(t, ins("a", `{"id":"a","seed":9007199254740992}`, 9007199254740992), "CHECK")
	expectErr(t, ins("a", `{"id":"a","seed":2}`, 1), "CHECK")
	if err := ins("a", `{"id":"a","seed":9007199254740991}`, 9007199254740991); err != nil {
		t.Fatal(err)
	}
}

func TestTracks_AudioIdentityUnique(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	r := insertRecipe(t, s, "a", 1)
	insertTrack(t, s, "t1", r, "One", "frames", 100)
	insertTrack(t, s, "t2", r, "Two", "frames", 200) // same frames, different total: ok
	err := exec(t, s, `INSERT INTO tracks(id,slug,title,recipe_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
		VALUES('t3','s','T','`+r+`','t3',0,'import','import',1,44100,16,2,100,'mp3','sha3',1,'s','frames','m','r',?,?)`, ts, ts)
	expectErr(t, err, "UNIQUE")
}

func ftsCount(t *testing.T, s *Store, table, q string) int {
	t.Helper()
	var n int
	if err := s.R.QueryRowContext(context.Background(), `SELECT count(*) FROM `+table+` WHERE `+table+` MATCH ?`, q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFTS_SyncedByTriggers(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	r := insertRecipe(t, s, "a", 1)
	insertTrack(t, s, "t1", r, "Vecchio titolo", "f", 100)
	if ftsCount(t, s, "tracks_fts", `"citta"`) != 1 {
		t.Error("lyrics diacritic fold failed")
	}
	if ftsCount(t, s, "tracks_fts", `"città"`) != 1 {
		t.Error("città should match via folding")
	}
	if ftsCount(t, s, "tracks_fts", `title:vecchio`) != 1 {
		t.Error("title not indexed")
	}
	mustExec(t, s, `UPDATE tracks SET title='Nuovo nome' WHERE id='t1'`)
	if ftsCount(t, s, "tracks_fts", `title:vecchio`) != 0 || ftsCount(t, s, "tracks_fts", `title:nuovo`) != 1 {
		t.Error("title update not synced")
	}

	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,request_id,request_json,created_at,updated_at) VALUES('d1','import_backlog','dr',?,?,?)`,
		`{"id":"dr","style":"jazz","lyrics":"alpha beta"}`, ts, ts)
	if ftsCount(t, s, "drafts_fts", `alpha`) != 1 {
		t.Error("draft not indexed")
	}
	mustExec(t, s, `UPDATE recipe_drafts SET request_json=? WHERE id='d1'`, `{"id":"dr","style":"jazz","lyrics":"gamma delta"}`)
	if ftsCount(t, s, "drafts_fts", `alpha`) != 0 || ftsCount(t, s, "drafts_fts", `gamma`) != 1 {
		t.Error("draft update not synced")
	}
}
