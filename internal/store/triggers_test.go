package store

import "testing"

func TestTriggers_Immutable(t *testing.T) {
	s, _ := open(t)
	r := insertRecipe(t, s, "a", 1)
	insertRun(t, s, "run-done", "completed")
	insertRun(t, s, "run-live", "running")
	insertTrack(t, s, "t1", r, "T", "f", 100)
	mustExec(t, s, `INSERT INTO recipe_sources(recipe_id,source_path,line_no,raw,raw_sha256,raw_kind,import_run_id,created_at) VALUES(?,?,1,x'00','z','jsonl_line','run-live',?)`, r, "a.jsonl", ts)
	mustExec(t, s, `INSERT INTO recipe_refs(recipe_id,source,ref_json,created_at) VALUES(?,?,'{}',?)`, r, "i.csv", ts)
	mustExec(t, s, `INSERT INTO track_style_terms(track_id,term) VALUES('t1','pop')`)
	mustExec(t, s, `INSERT INTO mutations(id,child_track_id,operator,inferred,principal_id,created_at) VALUES('m1','t1','inferred_sibling',1,'p',?)`, ts)
	mustExec(t, s, `INSERT INTO renditions(track_id,kind,sha256,bytes,path,params_json,created_at) VALUES('t1','mp3_320','x',1,'p','{}',?)`, ts)
	mustExec(t, s, `INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES('t1','loudness','v','0','{}',?)`, ts)
	mustExec(t, s, `INSERT INTO artifacts(track_id,name,sha256,bytes,path,created_at) VALUES('t1','request.json','x',1,'p',?)`, ts)
	mustExec(t, s, `INSERT INTO scores(track_id,abc_path,abc_sha256,bars,meter,aligned,source,created_at) VALUES('t1','p','x',1,'4/4',0,'import',?)`, ts)
	mustExec(t, s, `INSERT INTO sections(track_id,source,idx,kind,label,raw_label) VALUES('t1','lyrics',0,'verse','verse 1','[Verse 1]')`)
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,request_id,request_json,created_at,updated_at) VALUES('d1','import_backlog','dr','{}',?,?)`, ts, ts)
	mustExec(t, s, `INSERT INTO device_profiles(device_id,label,user_agent,quality,quality_source,flac_can_play,probe_version,created_by,updated_by,created_at,updated_at)
		VALUES('device-01','l','ua','auto','default','',1,'p','p',?,?)`, ts, ts)

	for _, tbl := range []string{"recipes", "recipe_sources", "tracks", "mutations", "renditions", "analyses", "artifacts", "scores", "sections",
		"recipe_drafts", "recipe_refs", "track_style_terms", "device_profiles", "import_runs"} {
		err := exec(t, s, `DELETE FROM `+tbl)
		if err == nil {
			t.Errorf("DELETE on %s succeeded", tbl)
			continue
		}
		if msg := err.Error(); !contains(msg, "no physical delete") && !contains(msg, "append-only") {
			t.Errorf("DELETE on %s: unexpected error %v", tbl, err)
		}
	}

	updates := []struct{ name, q, want string }{
		{"recipes", `UPDATE recipes SET style='x'`, "recipes: immutable"},
		{"recipe_sources", `UPDATE recipe_sources SET line_no=2`, "immutable"},
		{"recipe_refs", `UPDATE recipe_refs SET source='x'`, "immutable"},
		{"track_style_terms", `UPDATE track_style_terms SET term='x'`, "immutable"},
		{"mutations", `UPDATE mutations SET rationale='x'`, "immutable"},
		{"analyses", `UPDATE analyses SET tier='1'`, "immutable"},
		{"artifacts", `UPDATE artifacts SET bytes=2`, "immutable"},
		{"scores", `UPDATE scores SET bars=2`, "immutable"},
		{"sections", `UPDATE sections SET label='x'`, "immutable"},
		{"tracks master", `UPDATE tracks SET master_sha256='zz'`, "tracks: frozen column"},
		{"tracks title unchanged frozen", `UPDATE tracks SET title='T', recorded_at=recorded_at`, "tracks: frozen column"},
		{"drafts origin", `UPDATE recipe_drafts SET origin='x'`, "recipe_drafts: frozen column"},
		{"device created_at", `UPDATE device_profiles SET created_at='2027-01-01T00:00:00Z'`, "device_profiles: frozen column"},
		{"finished run", `UPDATE import_runs SET error='x' WHERE id='run-done'`, "import_runs: finished runs are immutable"},
		{"run frozen col", `UPDATE import_runs SET plan_digest='q' WHERE id='run-live'`, "import_runs: frozen column"},
	}
	for _, u := range updates {
		expectErr(t, exec(t, s, u.q), u.want)
	}

	// allowed updates, each run twice
	for i := 0; i < 2; i++ {
		mustExec(t, s, `UPDATE tracks SET title='New' WHERE id='t1'`)
		mustExec(t, s, `UPDATE tracks SET status='final', status_set_by='x', slug='y' WHERE id='t1'`)
		mustExec(t, s, `UPDATE import_runs SET heartbeat_at='2026-01-01T00:00:30Z' WHERE id='run-live'`)
		mustExec(t, s, `UPDATE recipe_drafts SET updated_at='2026-02-01T00:00:00Z', status='archived' WHERE id='d1'`)
		mustExec(t, s, `UPDATE device_profiles SET label='n', updated_at='2026-02-01T00:00:00Z' WHERE device_id='device-01'`)
		mustExec(t, s, `UPDATE renditions SET sha256='y' WHERE track_id='t1'`)
	}
	mustExec(t, s, `UPDATE import_runs SET state='completed', finished_at=? WHERE id='run-live' AND state='running'`, ts)
	expectErr(t, exec(t, s, `UPDATE import_runs SET heartbeat_at='x' WHERE id='run-live'`), "finished runs are immutable")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
