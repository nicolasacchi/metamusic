package library_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// dspSpec builds the canonical spec of a transform recipe for a test: {dsp_version, id, kind, seed, v}.
func dspSpec(kind string, n int) library.SpecRecipe {
	id := fmt.Sprintf("dsp-%s-%020d", kind, n)
	raw := `{"dsp_version":"mm-dsp/1","id":"` + id + `","kind":"` + kind + `","seed":0,"v":1}`
	h := sha256.Sum256([]byte(raw))
	return library.SpecRecipe{Kind: kind, Canonical: []byte(raw), Hash: hex.EncodeToString(h[:]), RequestID: id}
}

func beginTx(t *testing.T, e *env) *sql.Tx {
	t.Helper()
	tx, err := e.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// transformTake builds the NewTake of a transform of parent (rootless for score_render).
func transformTake(id, recipeID, kind, parent string, n int) library.NewTake {
	nt := newTake(id, recipeID, 900+n)
	nt.Origin, nt.ImportRule, nt.TransformID, nt.PrincipalID = "transform", "transform", id, "agent:tok1"
	nt.Generation = 0
	if parent != "" {
		nt.ParentTrackID, nt.RootTrackID, nt.Generation = parent, parent, 1
	}
	nt.Mutation = &library.NewMutation{Operator: kind, Rationale: "why", Params: map[string]any{"factor": 20}}
	return nt
}

// insertTransform runs the whole write path of one transform Take: the transform row, its recipe, the input row and the take.
func insertTransform(t *testing.T, e *env, tx *sql.Tx, kind, parent string, n int) (takeID string, nt library.NewTake) {
	t.Helper()
	ctx := context.Background()
	rid, _, err := e.svc.InsertTransformRecipe(ctx, tx, dspSpec(kind, n), "agent:tok1")
	if err != nil {
		t.Fatal(err)
	}
	takeID = fmt.Sprintf("01900000-0000-7000-8000-0000000b%04d", n)
	spec := strings.Repeat("a", 60) + fmt.Sprintf("%04d", n)
	if _, err := tx.ExecContext(ctx, `INSERT INTO transforms(id,kind,recipe_id,spec_hash,title,principal_id,principal_kind,principal_name,state,expected_cpu_ms,max_cpu_ms,out_seconds_est,mem_mb_est,created_at)
		VALUES(?,?,?,?,'T','agent:tok1','agent','tok1','queued',1,1,1.0,1,?)`, takeID, kind, rid, spec, "2026-11-03T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	return takeID, transformTake(takeID, rid, kind, parent, n)
}

func TestInsertTransformRecipe(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tx := beginTx(t, e)
	spec := dspSpec("glacier", 1)
	id, created, err := e.svc.InsertTransformRecipe(ctx, tx, spec, "agent:tok1")
	if err != nil || !created || id == "" {
		t.Fatalf("first insert: id=%q created=%v err=%v", id, created, err)
	}
	var engine, style, lyrics, cot, hash, core, by, reqID string
	var seed int
	if err := tx.QueryRow(`SELECT engine, style, lyrics, cot, hash, core_hash, created_by, request_id, seed FROM recipes WHERE id = ?`, id).
		Scan(&engine, &style, &lyrics, &cot, &hash, &core, &by, &reqID, &seed); err != nil {
		t.Fatal(err)
	}
	if engine != library.EngineDSP || style != "" || lyrics != "" || cot != "" || hash != spec.Hash || core != spec.Hash || by != "agent:tok1" || reqID != spec.RequestID || seed != 0 {
		t.Errorf("row = %q %q %q %q %q %q %q %q %d", engine, style, lyrics, cot, hash, core, by, reqID, seed)
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM recipe_keys WHERE recipe_id = ?`, id).Scan(&n); err != nil || n != 0 {
		t.Errorf("recipe_keys rows = %d (%v), want 0: a transform has no content/render split", n, err)
	}
	// idempotent by design: never ErrRecipeExists, the same id comes back
	id2, created2, err := e.svc.InsertTransformRecipe(ctx, tx, spec, "agent:other")
	if err != nil || created2 || id2 != id {
		t.Errorf("repeat: id=%q created=%v err=%v", id2, created2, err)
	}
	// refusals
	bad := spec
	bad.Hash = strings.Repeat("0", 64)
	for name, c := range map[string]struct {
		r  library.SpecRecipe
		by string
	}{
		"import creator": {spec, "import"}, "empty creator": {spec, ""}, "wrong hash": {bad, "agent:tok1"},
		"unknown kind": {library.SpecRecipe{Kind: "reverb", Canonical: spec.Canonical, Hash: spec.Hash, RequestID: "x"}, "agent:tok1"},
	} {
		if _, _, err := e.svc.InsertTransformRecipe(ctx, tx, c.r, c.by); err == nil || errors.Is(err, library.ErrRecipeExists) {
			t.Errorf("%s: err=%v, want a validation error", name, err)
		}
	}
}

func TestInsertTake_Transform(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tx := beginTx(t, e)
	// a human tag on the parent is inherited
	if _, err := tx.Exec(`INSERT INTO tags(id,name,created_at) VALUES('tg1','keeper',?)`, "2026-11-03T00:00:00.000Z"); err == nil {
		_, _ = tx.Exec(`INSERT INTO track_tags(track_id,tag_id,source,principal_id,created_at,updated_at) VALUES(?,'tg1','human','human:h',?,?)`, lt.TrackLuna, "2026-11-03T00:00:00.000Z", "2026-11-03T00:00:00.000Z")
	}
	id, nt := insertTransform(t, e, tx, "glacier", lt.TrackLuna, 1)
	nt.Mutation.Params = map[string]any{"factor": 20}
	if err := e.svc.InsertTake(ctx, tx, nt); err != nil {
		t.Fatal(err)
	}
	var origin, rule, by string
	var tid, job, env, bpm sql.NullString
	if err := tx.QueryRow(`SELECT origin, import_rule, status_set_by, transform_id, job_id, env_id, bpm_declared FROM tracks WHERE id = ?`, id).
		Scan(&origin, &rule, &by, &tid, &job, &env, &bpm); err != nil {
		t.Fatal(err)
	}
	if origin != "job" || rule != "transform" || by != "system:transform" || tid.String != id || job.Valid || env.Valid || bpm.Valid {
		t.Errorf("track = %q %q %q %+v %+v %+v %+v", origin, rule, by, tid, job, env, bpm)
	}
	var op, params, parent, mt sql.NullString
	var inferred int
	if err := tx.QueryRow(`SELECT operator, params_json, parent_track_id, transform_id, inferred FROM mutations WHERE child_track_id = ?`, id).
		Scan(&op, &params, &parent, &mt, &inferred); err != nil {
		t.Fatal(err)
	}
	if op.String != "glacier" || inferred != 0 || parent.String != lt.TrackLuna || mt.String != id || params.String != `{"factor":20,"transform_id":"`+id+`"}` {
		t.Errorf("mutation = %+v %d %+v %+v params=%s", op, inferred, parent, mt, params.String)
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM track_style_terms WHERE track_id = ?`, id).Scan(&n); err != nil || n != 0 {
		t.Errorf("style terms = %d (%v), want 0", n, err)
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM scores WHERE track_id = ?`, id).Scan(&n); err != nil || n != 0 {
		t.Errorf("scores rows = %d (%v), want 0", n, err)
	}
	var want, got int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM track_tags WHERE track_id = ? AND source IN ('human','inherited') AND removed_at IS NULL`, lt.TrackLuna).Scan(&want)
	_ = tx.QueryRow(`SELECT COUNT(*) FROM track_tags WHERE track_id = ? AND source = 'inherited' AND inherited_from = ?`, id, lt.TrackLuna).Scan(&got)
	if got != want {
		t.Errorf("inherited tags = %d, want %d", got, want)
	}
}

// TestInsertTake_TransformRefusals: the Go side of the shape refuses what the triggers would; score_render is rootless.
func TestInsertTake_TransformRefusals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tx := beginTx(t, e)
	_, base := insertTransform(t, e, tx, "morph", lt.TrackLuna, 2)
	for name, mutate := range map[string]func(*library.NewTake){
		"reference":       func(nt *library.NewTake) { nt.Reference = true },
		"lossy":           func(nt *library.NewTake) { nt.Lossy = true },
		"mismatched id":   func(nt *library.NewTake) { nt.TransformID = "other" },
		"no transform id": func(nt *library.NewTake) { nt.TransformID = "" },
		"16 bit":          func(nt *library.NewTake) { nt.BitsPerSample = 16 },
		"mono":            func(nt *library.NewTake) { nt.Channels = 1 },
		"wrong rule":      func(nt *library.NewTake) { nt.ImportRule = "job" },
		"missing parent":  func(nt *library.NewTake) { nt.ParentTrackID, nt.RootTrackID, nt.Generation = "", "", 0 },
		"job id":          func(nt *library.NewTake) { nt.JobID = "j1" },
		"no mutation":     func(nt *library.NewTake) { nt.Mutation = nil },
		"job operator":    func(nt *library.NewTake) { nt.Mutation.Operator = "reroll_seed" },
		"a score":         func(nt *library.NewTake) { nt.ScoreABC = []byte("X:1\nK:C\nCDEF|\n") },
	} {
		nt := base
		m := *base.Mutation
		nt.Mutation = &m
		mutate(&nt)
		err := e.svc.InsertTake(ctx, tx, nt)
		var ve *library.ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err=%v, want a validation error", name, err)
		}
	}
	// a non-transform take cannot carry a transform id
	imp := newTake("01900000-0000-7000-8000-00000000c001", base.RecipeID, 950)
	imp.TransformID = "x"
	if err := e.svc.InsertTake(ctx, tx, imp); err == nil {
		t.Error("an import take with a transform id was accepted")
	}
	// score_render is rootless and accepted
	id, root := insertTransform(t, e, tx, "score_render", "", 3)
	if err := e.svc.InsertTake(ctx, tx, root); err != nil {
		t.Fatalf("rootless score_render: %v", err)
	}
	var parent sql.NullString
	var gen int
	if err := tx.QueryRow(`SELECT m.parent_track_id, t.generation FROM mutations m JOIN tracks t ON t.id = m.child_track_id WHERE m.child_track_id = ?`, id).Scan(&parent, &gen); err != nil || parent.Valid || gen != 0 {
		t.Errorf("score render: parent=%+v gen=%d err=%v", parent, gen, err)
	}
	// a score render with a parent is refused
	_, withParent := insertTransform(t, e, tx, "score_render", lt.TrackLuna, 4)
	if err := e.svc.InsertTake(ctx, tx, withParent); err == nil {
		t.Error("a score render with a parent was accepted")
	}
}

// TestEnumOperator_T1: nine transform operators and the seven originals are all written; an unknown one is refused.
func TestEnumOperator_T1(t *testing.T) {
	t.Parallel()
	if got := len(library.TransformKinds); got != 9 {
		t.Fatalf("%d transform kinds, want 9", got)
	}
	e := newEnv(t)
	ctx := context.Background()
	tx := beginTx(t, e)
	for i, kind := range library.TransformKinds {
		parent := lt.TrackLuna
		if kind == "score_render" {
			parent = ""
		}
		_, nt := insertTransform(t, e, tx, kind, parent, 10+i)
		if err := e.svc.InsertTake(ctx, tx, nt); err != nil {
			t.Errorf("operator %s: %v", kind, err)
		}
	}
	_, nt := insertTransform(t, e, tx, "glacier", lt.TrackLuna, 30)
	nt.Mutation.Operator = "teleport"
	if err := e.svc.InsertTake(ctx, tx, nt); err == nil {
		t.Error("unknown operator accepted")
	}
}

// TestInsertTake_TransformScoreSource: a transform never writes a scores row and never reaches scores.source; a job take still does.
func TestInsertTake_TransformScoreSource(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	seed := lt.SeedJob(t, e.st, lt.FixedNow, lt.JobSeed{Name: "ss"}) // before the transaction: one writer connection
	tx := beginTx(t, e)
	_, nt := insertTransform(t, e, tx, "widen", lt.TrackLuna, 40)
	if err := e.svc.InsertTake(ctx, tx, nt); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM scores WHERE source NOT IN ('import','job')`).Scan(&n); err != nil || n != 0 {
		t.Errorf("scores rows with a third source = %d (%v)", n, err)
	}
	rid, _, err := e.svc.InsertRecipe(ctx, tx, jobRecipe(t, "gen-ss-1", 5), "agent:tok1")
	if err != nil {
		t.Fatal(err)
	}
	jt := jobTake("01900000-0000-7000-8000-00000000d001", rid, lt.TrackLuna, seed.JobID, seed.EnvID)
	jt.ScoreABC = []byte("X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|CDEF|\n")
	jt.Mutation = &library.NewMutation{Operator: "reroll_seed", JobID: seed.JobID}
	if err := e.svc.InsertTake(ctx, tx, jt); err != nil {
		t.Fatal(err)
	}
	var src string
	if err := tx.QueryRow(`SELECT source FROM scores WHERE track_id = ?`, jt.ID).Scan(&src); err != nil || src != "job" {
		t.Errorf("job score source = %q (%v), want job", src, err)
	}
}

// TestInsertTake_JobUnchanged pins the rows of an M3 job take, which the transform arms must not change.
func TestInsertTake_JobUnchanged(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	seed := lt.SeedJob(t, e.st, lt.FixedNow, lt.JobSeed{Name: "ju"})
	tx := beginTx(t, e)
	rid, _, err := e.svc.InsertRecipe(ctx, tx, jobRecipe(t, "gen-ju-1", 6), "agent:tok1")
	if err != nil {
		t.Fatal(err)
	}
	jt := jobTake("01900000-0000-7000-8000-00000000d002", rid, lt.TrackLuna, seed.JobID, seed.EnvID)
	jt.Mutation = &library.NewMutation{Operator: "same_seed_edit", Params: map[string]any{"mode": "x"}, JobID: seed.JobID}
	if err := e.svc.InsertTake(ctx, tx, jt); err != nil {
		t.Fatal(err)
	}
	var origin, rule, by, op, params string
	var tid, mtid sql.NullString
	var jobID string
	if err := tx.QueryRow(`SELECT t.origin, t.import_rule, t.status_set_by, t.job_id, t.transform_id, m.operator, m.params_json, m.transform_id
		FROM tracks t JOIN mutations m ON m.child_track_id = t.id WHERE t.id = ?`, jt.ID).Scan(&origin, &rule, &by, &jobID, &tid, &op, &params, &mtid); err != nil {
		t.Fatal(err)
	}
	if origin != "job" || rule != "job" || by != "system:job" || jobID != seed.JobID || tid.Valid || mtid.Valid || op != "same_seed_edit" ||
		params != `{"job_id":"`+seed.JobID+`","mode":"x"}` {
		t.Errorf("job take rows changed: %q %q %q %q %+v %q %s %+v", origin, rule, by, jobID, tid, op, params, mtid)
	}
}

func TestRequireRenderableMother(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tx := beginTx(t, e)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// the reader pool sees committed rows only: build the chain luna -> glacier -> morph and a rootless score render
	build := func() (glacier, morph, score string) {
		tx := beginTx(t, e)
		g, gt := insertTransform(t, e, tx, "glacier", lt.TrackLuna, 50)
		if err := e.svc.InsertTake(ctx, tx, gt); err != nil {
			t.Fatal(err)
		}
		m, mt := insertTransform(t, e, tx, "morph", g, 51)
		if err := e.svc.InsertTake(ctx, tx, mt); err != nil {
			t.Fatal(err)
		}
		s, st := insertTransform(t, e, tx, "score_render", "", 52)
		if err := e.svc.InsertTake(ctx, tx, st); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return g, m, s
	}
	glacier, morph, score := build()
	if err := e.svc.RequireRenderableMother(ctx, lt.TrackLuna); err != nil {
		t.Errorf("an imported take is a mother: %v", err)
	}
	for _, c := range []struct {
		id   string
		want *string
	}{{glacier, ptr(lt.TrackLuna)}, {morph, ptr(lt.TrackLuna)}, {score, nil}} {
		err := e.svc.RequireRenderableMother(ctx, c.id)
		var me *library.TransformMotherError
		if !errors.Is(err, library.ErrTransformMother) || !errors.As(err, &me) || me.TrackID != c.id {
			t.Fatalf("%s: err=%v", c.id, err)
		}
		if (me.NearestAncestorID == nil) != (c.want == nil) || (c.want != nil && *me.NearestAncestorID != *c.want) {
			t.Errorf("%s: nearest = %v, want %v", c.id, me.NearestAncestorID, c.want)
		}
		got, err := e.svc.NearestRenderableAncestor(ctx, c.id)
		if err != nil || (got == nil) != (c.want == nil) {
			t.Errorf("NearestRenderableAncestor(%s) = %v, %v", c.id, got, err)
		}
	}
	if err := e.svc.RequireRenderableMother(ctx, "01900000-0000-7000-8000-ffffffffffff"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func ptr(s string) *string { return &s }

// commitTransform writes one transform Take (title given) and commits, so the reader pool sees it.
func commitTransform(t *testing.T, e *env, kind, title string, n int) string {
	t.Helper()
	tx := beginTx(t, e)
	id, nt := insertTransform(t, e, tx, kind, lt.TrackLuna, n)
	nt.Title = title
	if err := e.svc.InsertTake(context.Background(), tx, nt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestSummary_DspNoStyleTerms: the summary and the detail of a transform Take carry no style terms and no declared tempo,
// even when the title looks like a tempo (C211.1b).
func TestSummary_DspNoStyleTerms(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	id := commitTransform(t, e, "polytempo", "dsp polytempo 90/120, reggae", 60)
	page, err := e.svc.SearchTracks(ctx, library.SearchParams{Q: "polytempo"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != id {
		t.Fatalf("search: %+v %v", ids(page), err)
	}
	if got := page.Items[0]; len(got.StyleTerms) != 0 || got.StyleTerms == nil {
		t.Errorf("summary style terms = %#v, want an empty non-nil list", got.StyleTerms)
	}
	d, err := e.svc.Track(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.StyleTerms) != 0 || d.StyleTerms == nil || d.Recipe.Engine != library.EngineDSP || d.Recipe.Style != "" {
		t.Errorf("detail: terms=%#v engine=%q style=%q", d.StyleTerms, d.Recipe.Engine, d.Recipe.Style)
	}
	var bpm sql.NullInt64
	if err := e.st.R.QueryRow(`SELECT bpm_declared FROM tracks WHERE id = ?`, id).Scan(&bpm); err != nil || bpm.Valid {
		t.Errorf("bpm_declared = %+v (%v), want NULL", bpm, err)
	}
}

// TestSearch_StyleFilterSkipsDsp: a style filter never selects a transform Take, whatever its title says.
func TestSearch_StyleFilterSkipsDsp(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := commitTransform(t, e, "ensemble", "reggae 92 bpm ensemble cathedral", 61)
	for _, style := range []string{"reggae", "92 bpm", "cathedral"} {
		for _, got := range search(t, e, library.SearchParams{Style: []string{style}}) {
			if got == id {
				t.Errorf("style filter %q selected the transform Take", style)
			}
		}
	}
	var n int
	if err := e.st.R.QueryRow(`SELECT COUNT(*) FROM track_style_terms WHERE track_id = ?`, id).Scan(&n); err != nil || n != 0 {
		t.Errorf("style term rows = %d (%v)", n, err)
	}
}

// TestFTS_DspNotIndexed: the title of a transform is searchable as a title, but no style or lyrics text is indexed for it.
func TestFTS_DspNotIndexed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := commitTransform(t, e, "glacier", "Quietwords glacier x20", 62)
	if got := search(t, e, library.SearchParams{Q: "Quietwords"}); len(got) != 1 || got[0] != id {
		t.Errorf("title search = %v, want [%s]", got, id)
	}
	var style, lyrics string
	if err := e.st.R.QueryRow(`SELECT style, lyrics FROM tracks_fts WHERE track_id = ?`, id).Scan(&style, &lyrics); err != nil || style != "" || lyrics != "" {
		t.Errorf("fts style=%q lyrics=%q err=%v, want both empty", style, lyrics, err)
	}
}
