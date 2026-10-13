package store

import (
	"strings"
	"testing"
)

const stubJSON = `{"id":"ref_a_00000000","seed":0,"title":"Syn ref a"}`

// insertStub inserts a stub recipe with the C58 shape; rid is the id suffix.
func insertStub(t *testing.T, s *Store, rid string) string {
	t.Helper()
	id := "r-" + rid
	js := `{"id":"` + rid + `","seed":0,"title":"Syn ` + rid + `"}`
	mustExec(t, s, stubInsert, id, rid, js, "h-"+rid, "h-"+rid)
	return id
}

const stubInsert = `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at)
	VALUES(?,'unknown',?,?,'','',0,'',NULL,?,?,'import','` + ts + `')`

const lossyTrackQ = `INSERT INTO tracks(id,slug,title,recipe_id,parent_track_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
	master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at,lossy)
	VALUES(?,'slug','T',?,?,?,?,'import',?,2.0,44100,?,?,88200,?,?,'1000',?,?,?,?,'` + ts + `','` + ts + `',?)`

type lossyRow struct {
	id, recipe          string
	parent              any
	root                string
	gen                 int
	origin              string
	bits, channels      int
	path, sha           string
	source, frames, md5 string
	rule                string
	lossy               int
}

func baseLossy() lossyRow {
	return lossyRow{id: "l1", recipe: "r-st", root: "l1", origin: "import", bits: 16, channels: 2,
		path: "audio/masters/l1.mp3", sha: "sha-l1", source: "sha-l1", frames: "sha-l1", md5: "00000000000000000000000000000000", rule: "reference", lossy: 1}
}

func (r lossyRow) insert(s *Store, t *testing.T) error {
	return exec(t, s, lossyTrackQ, r.id, r.recipe, r.parent, r.root, r.gen, r.origin, r.bits, r.channels, r.path, r.sha,
		r.source, r.frames, r.md5, r.rule, r.lossy)
}

func TestMigration0005_StubShape(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	ok := func(id, js string) error {
		return exec(t, s, stubInsert, "r-"+id, id, js, "h-"+id, "h-"+id)
	}
	if err := ok("ref_a_00000000", stubJSON); err != nil {
		t.Fatal(err)
	}
	const bad = "recipes: unknown-engine shape"
	for name, q := range map[string]string{
		"style":    `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at) VALUES('x1','unknown','b1','{"id":"b1","seed":0,"title":"t"}','x','','0','',NULL,'h1','h1','import','` + ts + `')`,
		"lyrics":   `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at) VALUES('x2','unknown','b2','{"id":"b2","seed":0,"title":"t"}','','x',0,'',NULL,'h2','h2','import','` + ts + `')`,
		"cot":      `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at) VALUES('x3','unknown','b3','{"id":"b3","seed":0,"title":"t"}','','',0,'full',NULL,'h3','h3','import','` + ts + `')`,
		"seed":     `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at) VALUES('x4','unknown','b4','{"id":"b4","seed":5,"title":"t"}','','',5,'',NULL,'h4','h4','import','` + ts + `')`,
		"sampling": `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at) VALUES('x5','unknown','b5','{"id":"b5","seed":0,"title":"t"}','','',0,'','{}','h5','h5','import','` + ts + `')`,
		"creator":  `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at) VALUES('x6','unknown','b6','{"id":"b6","seed":0,"title":"t"}','','',0,'',NULL,'h6','h6','cli','` + ts + `')`,
		"corehash": `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at) VALUES('x7','unknown','b7','{"id":"b7","seed":0,"title":"t"}','','',0,'',NULL,'h7','other','import','` + ts + `')`,
		"notitle":  `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at) VALUES('x8','unknown','b8','{"id":"b8","seed":0,"name":"t"}','','',0,'',NULL,'h8','h8','import','` + ts + `')`,
		"extra":    `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at) VALUES('x9','unknown','b9','{"extra":1,"id":"b9","seed":0,"title":"t"}','','',0,'',NULL,'h9','h9','import','` + ts + `')`,
	} {
		if err := exec(t, s, q); err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("%s: want %q, got %v", name, bad, err)
		}
	}
	expectErr(t, exec(t, s, `UPDATE recipes SET engine='yue2'`), "recipes: immutable")
}

func TestMigration0005_LossyShape(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	insertStub(t, s, "st")
	base := baseLossy()
	const bad = "tracks: lossy shape"
	cases := map[string]func(r *lossyRow){
		"flac path with lossy=1": func(r *lossyRow) { r.path = "audio/masters/l1.flac" },
		"mp3 path with lossy=0":  func(r *lossyRow) { r.lossy = 0 },
		"origin job":             func(r *lossyRow) { r.origin = "job" },
		"24 bits":                func(r *lossyRow) { r.bits = 24 },
		"3 channels":             func(r *lossyRow) { r.channels = 3 },
		"source differs":         func(r *lossyRow) { r.source = "other" },
		"frames differ":          func(r *lossyRow) { r.frames = "other" },
		"audio_md5 non-zero":     func(r *lossyRow) { r.md5 = "00000000000000000000000000000001" },
	}
	for name, mut := range cases {
		r := base
		r.id, r.root = "bad-"+name, "bad-"+name
		r.path, r.sha, r.source, r.frames = "audio/masters/"+name+".mp3", "sha-"+name, "sha-"+name, "sha-"+name
		mut(&r)
		if err := r.insert(s, t); err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("%s: want %q, got %v", name, bad, err)
		}
	}
	if err := base.insert(s, t); err != nil {
		t.Fatal(err)
	}
	expectErr(t, exec(t, s, `UPDATE tracks SET lossy=0 WHERE id='l1'`), "tracks: frozen column")
}

func TestMigration0005_ReferenceRule(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	insertStub(t, s, "st")
	known := insertRecipe(t, s, "k1", 1)
	const bad = "tracks: reference rule"
	r := baseLossy()
	r.id, r.root, r.recipe, r.path = "x1", "x1", known, "audio/masters/x1.mp3"
	r.sha, r.source, r.frames = "sha-x1", "sha-x1", "sha-x1"
	expectErr(t, r.insert(s, t), bad) // reference rule with a yue2 recipe
	r = baseLossy()
	r.id, r.root, r.rule, r.path = "x2", "x2", "slug", "audio/masters/x2.mp3"
	r.sha, r.source, r.frames = "sha-x2", "sha-x2", "sha-x2"
	expectErr(t, r.insert(s, t), bad) // lossy with a non-reference rule
	// a non-reference rule with a stub recipe (FLAC path)
	expectErr(t, exec(t, s, `INSERT INTO tracks(id,slug,title,recipe_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
		VALUES('x3','s','T','r-st','x3',0,'import','import',1,44100,16,2,5,'m/x3.flac','sh3',1,'s','f3','m','mapping','`+ts+`','`+ts+`')`), bad)
}

func TestMigration0005_ReferenceLineage(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	insertStub(t, s, "st")
	insertStub(t, s, "st2")
	known := insertRecipe(t, s, "k1", 1)
	insertTrack(t, s, "kp", known, "Known", "fk", 100)
	const bad = "tracks: reference lineage"
	root := baseLossy()
	if err := root.insert(s, t); err != nil {
		t.Fatal(err)
	}
	// reference child of a known parent
	c := baseLossy()
	c.id, c.recipe, c.parent, c.root, c.gen, c.path = "c1", "r-st2", "kp", "kp", 1, "audio/masters/c1.mp3"
	c.sha, c.source, c.frames = "sha-c1", "sha-c1", "sha-c1"
	expectErr(t, c.insert(s, t), bad)
	// known child of a reference parent
	expectErr(t, exec(t, s, `INSERT INTO tracks(id,slug,title,recipe_id,parent_track_id,root_track_id,generation,status_set_by,origin,duration_s,sample_rate,bits_per_sample,channels,total_samples,
		master_path,master_sha256,master_bytes,source_sha256,frames_sha256,audio_md5,import_rule,recorded_at,created_at)
		VALUES('c2','s','T',?,'l1','l1',1,'import','import',1,44100,16,2,5,'m/c2.flac','sh-c2',1,'s','f-c2','m','slug','`+ts+`','`+ts+`')`, known), bad)
	// reference child of a reference parent succeeds
	c = baseLossy()
	c.id, c.recipe, c.parent, c.root, c.gen, c.path = "c3", "r-st2", "l1", "l1", 1, "audio/masters/c3.mp3"
	c.sha, c.source, c.frames = "sha-c3", "sha-c3", "sha-c3"
	if err := c.insert(s, t); err != nil {
		t.Fatal(err)
	}
}

func TestMigration0005_RenditionAndDraftTriggers(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	insertStub(t, s, "st")
	known := insertRecipe(t, s, "k1", 1)
	insertTrack(t, s, "kp", known, "Known", "fk", 100)
	if err := baseLossy().insert(s, t); err != nil {
		t.Fatal(err)
	}
	const rq = `INSERT INTO renditions(track_id,kind,sha256,bytes,path,params_json,created_at) VALUES(?,?,?,1,?,'{}','` + ts + `')`
	const rbad = "renditions: a lossy track has no mp3 rendition"
	expectErr(t, exec(t, s, rq, "l1", "mp3_320", "rs1", "p/1.mp3"), rbad)
	mustExec(t, s, rq, "l1", "peaks", "rs2", "p/2.json")
	mustExec(t, s, rq, "kp", "mp3_320", "rs3", "p/3.mp3")
	mustExec(t, s, rq, "kp", "peaks", "rs4", "p/4.json")
	// update: move a rendition's kind/track onto a lossy track
	expectErr(t, exec(t, s, `UPDATE renditions SET track_id='l1' WHERE sha256='rs3'`), rbad)
	expectErr(t, exec(t, s, `UPDATE renditions SET kind='mp3_320' WHERE sha256='rs2'`), rbad)

	const dq = `INSERT INTO recipe_drafts(id,request_id,request_json,status,origin,parent_track_id,created_at,updated_at) VALUES(?,?,?,'backlog','api',?,'` + ts + `','` + ts + `')`
	const dbad = "recipe_drafts: recipe unknown"
	expectErr(t, exec(t, s, dq, "d1", "d1", `{"id":"d1"}`, "l1"), dbad)
	mustExec(t, s, dq, "d2", "d2", `{"id":"d2"}`, "kp")
	mustExec(t, s, dq, "d3", "d3", `{"id":"d3"}`, nil)
	expectErr(t, exec(t, s, `UPDATE recipe_drafts SET parent_track_id='l1' WHERE id='d3'`), dbad)
}
