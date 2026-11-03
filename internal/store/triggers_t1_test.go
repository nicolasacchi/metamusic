package store

import (
	"strings"
	"testing"
)

const (
	t1ts = "2026-11-03T12:00:00.000Z"
	t1h  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// t1Recipe inserts a dsp recipe with the shape the migration requires.
func t1Recipe(t *testing.T, s *Store, rid string, mutate func(*[]any)) error {
	t.Helper()
	js := `{"id":"` + rid + `","seed":0,"v":1,"kind":"glacier","dsp_version":"mm-dsp/1"}`
	args := []any{"r-" + rid, "dsp", rid, js, "", "", 0, "", nil, "h-" + rid, "h-" + rid, "agent:a", t1ts}
	if mutate != nil {
		mutate(&args)
	}
	return exec(t, s, `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
}

func t1Transform(t *testing.T, s *Store, id, recipe, specHash, state string) error {
	t.Helper()
	cols := `id,kind,recipe_id,spec_hash,title,principal_id,principal_kind,principal_name,state,expected_cpu_ms,max_cpu_ms,out_seconds_est,mem_mb_est,created_at`
	extra, marks := "", ""
	args := []any{id, "glacier", recipe, specHash, "T " + id, "agent:a", "agent", "a", state, 10, 100, 30.0, 64, t1ts}
	switch state {
	case "running":
		extra, marks = ",owner,lease_expires_at,started_at", ",?,?,?"
		args = append(args, "runner-1", t1ts, t1ts)
	case "done":
		extra, marks = ",pcm_sha256,cpu_ms,finished_at", ",?,?,?"
		args = append(args, t1h, 5, t1ts)
	case "failed", "cancelled":
		extra, marks = ",error_code,cpu_ms,finished_at", ",?,?,?"
		args = append(args, "TRANSFORM_FAILED", 0, t1ts)
	}
	return exec(t, s, `INSERT INTO transforms(`+cols+extra+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?`+marks+`)`, args...)
}

// t1Take inserts the Take of a transform (24-bit stereo, origin job, import_rule transform).
func t1Take(t *testing.T, s *Store, id, recipe, parent string, mutate func(*[]any)) error {
	t.Helper()
	args := []any{id, "slug", "T " + id, recipe, parent, "t-parent", 1, "system:transform", "job", 10.0, 48000, 24, 2, 480000,
		"masters/" + id, "sha-" + id, 1, "s", "f-" + id, "m", "transform", t1ts, t1ts, id}
	if mutate != nil {
		mutate(&args)
	}
	return exec(t, s, `INSERT INTO tracks(id,slug,title,recipe_id,parent_track_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at,transform_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
}

// TestTriggers_Transform: every trigger and CHECK of the T1 migration refuses its bad row and accepts its good one (C201).
func TestTriggers_Transform(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	rYue := insertRecipe(t, s, "src", 1)
	pSha, oSha, rSha := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	t1Source(t, s, "t-parent", rYue, pSha)
	t1Source(t, s, "t-other", insertRecipe(t, s, "src2", 2), oSha)
	// a lossy reference Take and a reference Take as inputs
	refRecipe := insertStub(t, s, "ref1")
	mustExec(t, s, `INSERT INTO tracks(id,slug,title,recipe_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
		VALUES('t-ref','slug','R',?,'t-ref',0,'import','import',10.0,44100,16,2,441000,'masters/ref',?,1,'s','f-ref','m','reference',?,?)`, refRecipe, rSha, t1ts, t1ts)

	// ---- recipes_dsp_shape ----
	if err := t1Recipe(t, s, "dsp-glacier-a", nil); err != nil {
		t.Fatalf("a well-formed dsp recipe: %v", err)
	}
	for name, mutate := range map[string]func(*[]any){
		"import creator": func(a *[]any) { (*a)[11] = "import" },
		"style text":     func(a *[]any) { (*a)[4] = "reggae" },
		"lyrics":         func(a *[]any) { (*a)[5] = "la" },
		"cot":            func(a *[]any) { (*a)[7] = "full" },
		"sampling":       func(a *[]any) { (*a)[8] = "{}" },
		"no version":     func(a *[]any) { (*a)[3] = `{"id":"dsp-x","seed":0,"kind":"glacier","dsp_version":"mm-dsp/1"}` },
		"no kind":        func(a *[]any) { (*a)[3] = `{"id":"dsp-x","seed":0,"v":1,"dsp_version":"mm-dsp/1"}` },
		"foreign dsp":    func(a *[]any) { (*a)[3] = `{"id":"dsp-x","seed":0,"v":1,"kind":"glacier","dsp_version":"other/1"}` },
	} {
		err := t1Recipe(t, s, "dsp-x", func(a *[]any) {
			(*a)[3] = `{"id":"dsp-x","seed":0,"v":1,"kind":"glacier","dsp_version":"mm-dsp/1"}`
			mutate(a)
		})
		if err == nil || !strings.Contains(err.Error(), "recipes: dsp shape") {
			t.Errorf("recipes_dsp_shape accepted %q: %v", name, err)
		}
	}

	// ---- transforms: shape, CHECKs ----
	h1, h2, h3 := strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)
	if err := t1Transform(t, s, "x1", "r-dsp-glacier-a", h1, "queued"); err != nil {
		t.Fatal(err)
	}
	expectErr(t, t1Transform(t, s, "x2", "r-dsp-glacier-a", h1, "queued"), "UNIQUE") // live-spec uniqueness
	expectErr(t, t1Transform(t, s, "x3", "r-dsp-glacier-a", "short", "queued"), "CHECK")
	mustExec(t, s, `UPDATE transforms SET state='running', owner='r1', lease_expires_at=?, started_at=? WHERE id='x1'`, t1ts, t1ts)
	expectErr(t, exec(t, s, `UPDATE transforms SET owner=NULL WHERE id='x1'`), "CHECK") // running row needs owner and lease
	expectErr(t, exec(t, s, `UPDATE transforms SET kind='morph' WHERE id='x1'`), "frozen column")
	expectErr(t, exec(t, s, `UPDATE transforms SET spec_hash=? WHERE id='x1'`, h2), "frozen column")
	expectErr(t, exec(t, s, `DELETE FROM transforms WHERE id='x1'`), "no physical delete")
	mustExec(t, s, `UPDATE transforms SET state='queued', owner=NULL, lease_expires_at=NULL WHERE id='x1'`) // running -> queued (recover)
	mustExec(t, s, `UPDATE transforms SET state='running', owner='r1', lease_expires_at=?, started_at=? WHERE id='x1'`, t1ts, t1ts)
	mustExec(t, s, `UPDATE transforms SET state='done', pcm_sha256=?, cpu_ms=5, finished_at=?, owner=NULL, lease_expires_at=NULL WHERE id='x1'`, t1h, t1ts)
	expectErr(t, exec(t, s, `UPDATE transforms SET state='running', owner='r', lease_expires_at=? WHERE id='x1'`, t1ts), "transforms: ") // done -> running refused (state machine or finality)
	expectErr(t, exec(t, s, `UPDATE transforms SET progress_permille=5 WHERE id='x1'`), "terminal rows are immutable")
	// a failed row does not block a new transform of the same spec; a done one does
	if err := t1Transform(t, s, "x4", "r-dsp-glacier-a", h2, "failed"); err != nil {
		t.Fatal(err)
	}
	if err := t1Transform(t, s, "x5", "r-dsp-glacier-a", h2, "queued"); err != nil {
		t.Fatalf("a failed row must not block a new transform: %v", err)
	}
	expectErr(t, t1Transform(t, s, "x6", "r-dsp-glacier-a", h1, "queued"), "UNIQUE") // x1 is done
	// terminal rows need cpu_ms (0 if it never ran), error rows an error code
	expectErr(t, exec(t, s, `INSERT INTO transforms(id,kind,recipe_id,spec_hash,title,principal_id,principal_kind,principal_name,state,expected_cpu_ms,max_cpu_ms,out_seconds_est,mem_mb_est,created_at,error_code,finished_at)
		VALUES('x7','glacier','r-dsp-glacier-a',?,'T','a','agent','a','cancelled',1,1,1.0,1,?,'CANCELLED_BY_USER',?)`, h3, t1ts, t1ts), "CHECK")
	// state machine: queued -> done is illegal
	mustExec(t, s, `UPDATE transforms SET state='running', owner='r2', lease_expires_at=?, started_at=? WHERE id='x5'`, t1ts, t1ts)
	if err := t1Transform(t, s, "x8", "r-dsp-glacier-a", h3, "queued"); err != nil {
		t.Fatal(err)
	}
	expectErr(t, exec(t, s, `UPDATE transforms SET state='done', pcm_sha256=?, cpu_ms=1, finished_at=? WHERE id='x8'`, t1h, t1ts), "illegal state transition")

	// ---- transform_inputs ----
	mustExec(t, s, `INSERT INTO transform_inputs(transform_id,ord,role,track_id,master_sha256) VALUES('x5',0,'source','t-parent',?)`, pSha)
	expectErr(t, exec(t, s, `INSERT INTO transform_inputs(transform_id,ord,role,track_id,master_sha256) VALUES('x5',1,'b','t-other',?)`, h1), "master hash mismatch")
	expectErr(t, exec(t, s, `INSERT INTO transform_inputs(transform_id,ord,role,track_id,master_sha256) VALUES('x5',1,'b','t-ref',?)`, rSha), "lossy or reference input")
	expectErr(t, exec(t, s, `INSERT INTO transform_inputs(transform_id,ord,role,track_id,master_sha256) VALUES('x5',2,'b','t-other',?)`, oSha), "CHECK")
	expectErr(t, exec(t, s, `INSERT INTO transform_inputs(transform_id,ord,role,track_id,master_sha256) VALUES('x5',1,'zzz','t-other',?)`, oSha), "CHECK")
	mustExec(t, s, `INSERT INTO transform_inputs(transform_id,ord,role,track_id,master_sha256) VALUES('x5',1,'b','t-other',?)`, oSha)
	// a score_source annotation may name a reference Take (the source supplies text, not audio)
	mustExec(t, s, `INSERT INTO transform_inputs(transform_id,ord,role,track_id,master_sha256) VALUES('x8',0,'score_source','t-ref',?)`, rSha)
	expectErr(t, exec(t, s, `UPDATE transform_inputs SET role='a' WHERE transform_id='x5' AND ord=0`), "immutable")
	expectErr(t, exec(t, s, `DELETE FROM transform_inputs WHERE transform_id='x5'`), "no physical delete")

	// ---- tracks_transform_shape and the provenance columns ----
	if err := t1Take(t, s, "x5", "r-dsp-glacier-a", "t-parent", nil); err != nil {
		t.Fatalf("a well-formed transform take: %v", err)
	}
	for name, mutate := range map[string]func(*[]any){
		"id differs from transform_id": func(a *[]any) { (*a)[23] = "x1" },
		"import origin":                func(a *[]any) { (*a)[8] = "import" },
		"16 bits":                      func(a *[]any) { (*a)[11] = 16 },
		"mono":                         func(a *[]any) { (*a)[12] = 1 },
		"wrong rule":                   func(a *[]any) { (*a)[20] = "job" },
	} {
		err := t1Take(t, s, "x8", "r-dsp-glacier-a", "t-parent", mutate)
		if err == nil || !strings.Contains(err.Error(), "tracks: transform shape") {
			t.Errorf("tracks_transform_shape accepted %q: %v", name, err)
		}
	}
	// a dsp recipe needs a transform id, and a transform id needs a dsp recipe
	expectErr(t, t1Take(t, s, "x8", "r-dsp-glacier-a", "t-parent", func(a *[]any) { (*a)[23] = nil }), "tracks: transform shape")
	expectErr(t, t1Take(t, s, "x9", rYue, "t-parent", func(a *[]any) { (*a)[23] = "x5" }), "tracks: transform shape")
	expectErr(t, exec(t, s, `UPDATE tracks SET transform_id=NULL WHERE id='x5'`), "frozen column")
	expectErr(t, t1Take(t, s, "x9", "r-dsp-glacier-a", "t-parent", func(a *[]any) { (*a)[23] = "x8" }), "tracks: transform shape") // id <> transform_id
	// a second Take for the same transform is refused by the unique index (here via a fresh id sharing the transform id)
	mustExec(t, s, `INSERT INTO mutations(id,parent_track_id,child_track_id,operator,inferred,principal_id,created_at,transform_id) VALUES('m1','t-parent','x5','glacier',0,'agent:a',?,'x5')`, t1ts)
	expectErr(t, exec(t, s, `UPDATE mutations SET transform_id=NULL WHERE id='m1'`), "immutable")

	// ---- draft parents ----
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,status,request_id,request_json,parent_track_id,created_at,updated_at) VALUES('d-ok','editor','backlog','dr-ok','{}','t-parent',?,?)`, t1ts, t1ts)
	expectErr(t, exec(t, s, `INSERT INTO recipe_drafts(id,origin,status,request_id,request_json,parent_track_id,created_at,updated_at) VALUES('d-bad','editor','backlog','dr-bad','{}','x5',?,?)`, t1ts, t1ts), "not renderable")
	expectErr(t, exec(t, s, `UPDATE recipe_drafts SET parent_track_id='x5' WHERE id='d-ok'`), "not renderable")
}

// t1Source inserts a lossless 16-bit import Take with the given 64-hex master hash.
func t1Source(t *testing.T, s *Store, id, recipe, sha string) {
	t.Helper()
	mustExec(t, s, `INSERT INTO tracks(id,slug,title,recipe_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
		VALUES(?,'slug',?,?,?,0,'import','import',10.0,44100,16,2,?,?,?,1,'s',?,'m','r',?,?)`,
		id, "T "+id, recipe, id, 1000+len(id)+int(sha[0]), "masters/"+id, sha, "f-"+id, t1ts, t1ts)
}
