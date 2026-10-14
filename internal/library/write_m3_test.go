package library_test

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

func jobRecipe(t *testing.T, id string, seed int64) yue2.Parsed {
	t.Helper()
	raw := `{"id":"` + id + `","style":"synth pop, 100 bpm","lyrics":"[Verse]\nline one","cot":"full","seed":` + itoa(seed) + `}`
	p, err := yue2.ParseRequest([]byte(raw), yue2.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestInsertRecipe_CreatedBy(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	p := jobRecipe(t, "gen-a-s1102", 1102)
	rid, created, err := e.svc.InsertRecipe(ctx, tx, p, "agent:tok1")
	if err != nil || !created {
		t.Fatalf("job recipe: id=%q created=%v err=%v", rid, created, err)
	}
	var by, content, render string
	if err := tx.QueryRow(`SELECT r.created_by, k.content_hash, k.render_hash FROM recipes r JOIN recipe_keys k ON k.recipe_id = r.id WHERE r.id = ?`, rid).
		Scan(&by, &content, &render); err != nil {
		t.Fatal(err)
	}
	wantC, wantR := lineage.Keys(p.Canonical)
	if by != "agent:tok1" || content != wantC || render != wantR {
		t.Errorf("row = %q %q %q, want agent:tok1 %q %q", by, content, render, wantC, wantR)
	}

	// An identical existing row is never reused by a job recipe (reuse goes through ReusableDraftRecipe).
	id2, created2, err := e.svc.InsertRecipe(ctx, tx, p, "agent:tok1")
	if !errors.Is(err, library.ErrRecipeExists) || created2 || id2 != rid {
		t.Errorf("second job insert: id=%q created=%v err=%v, want ErrRecipeExists with the existing id", id2, created2, err)
	}

	// import callers are unchanged: an existing row is returned with created=false and no error,
	// and a new import recipe also gets its recipe_keys row.
	imp := jobRecipe(t, "gen-import-1", 7)
	iid, c1, err := e.svc.InsertRecipe(ctx, tx, imp, "import")
	if err != nil || !c1 {
		t.Fatalf("import: %v %v", c1, err)
	}
	iid2, c2, err := e.svc.InsertRecipe(ctx, tx, imp, "import")
	if err != nil || c2 || iid2 != iid {
		t.Errorf("import repeat: id=%q created=%v err=%v", iid2, c2, err)
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM recipe_keys WHERE recipe_id = ?`, iid).Scan(&n); err != nil || n != 1 {
		t.Errorf("recipe_keys rows for the import recipe = %d (%v), want 1", n, err)
	}
}

func jobTake(id, rid, parent string, jobID, envID string) library.NewTake {
	nt := newTake(id, rid, 77)
	nt.ParentTrackID, nt.RootTrackID, nt.Generation = parent, parent, 1
	nt.ImportRule = "job"
	nt.Origin, nt.StatusSetBy, nt.JobID, nt.EnvID = "job", "system:job", jobID, envID
	nt.PrincipalID = "agent:tok1"
	return nt
}

func TestInsertTake_JobFields(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	seed := lt.SeedJob(t, e.st, lt.FixedNow, lt.JobSeed{Name: "a"})
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	p := jobRecipe(t, "gen-b-s2", 2)
	rid, _, err := e.svc.InsertRecipe(ctx, tx, p, "agent:tok1")
	if err != nil {
		t.Fatal(err)
	}
	const child = "01900000-0000-7000-8000-00000000a001"
	nt := jobTake(child, rid, lt.TrackLuna, seed.JobID, seed.EnvID)
	nt.ScoreABC = []byte("X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|CDEF|\n")
	nt.Mutation = &library.NewMutation{Operator: "reroll_seed", Params: map[string]any{"mode": "new_seed", "seed_policy": "auto"},
		JobID: seed.JobID, RecipePatchJSON: `{"style":"x"}`, Rationale: "try another seed"}
	if err := e.svc.InsertTake(ctx, tx, nt); err != nil {
		t.Fatal(err)
	}
	var origin, by, job, env string
	if err := tx.QueryRow(`SELECT origin, status_set_by, job_id, env_id FROM tracks WHERE id = ?`, child).Scan(&origin, &by, &job, &env); err != nil {
		t.Fatal(err)
	}
	if origin != "job" || by != "system:job" || job != seed.JobID || env != seed.EnvID {
		t.Errorf("track = %q %q %q %q", origin, by, job, env)
	}
	var op, params, patch, mjob sql.NullString
	var inferred int
	if err := tx.QueryRow(`SELECT operator, params_json, recipe_patch_json, job_id, inferred FROM mutations WHERE child_track_id = ?`, child).
		Scan(&op, &params, &patch, &mjob, &inferred); err != nil {
		t.Fatal(err)
	}
	if op.String != "reroll_seed" || inferred != 0 || patch.String != `{"style":"x"}` || mjob.String != seed.JobID {
		t.Errorf("mutation = %+v %+v %+v %d", op, patch, mjob, inferred)
	}
	if params.String != `{"job_id":"`+seed.JobID+`","mode":"new_seed","seed_policy":"auto"}` {
		t.Errorf("params_json = %s", params.String)
	}
	var src string
	if err := tx.QueryRow(`SELECT source FROM scores WHERE track_id = ?`, child).Scan(&src); err != nil || src != "job" {
		t.Errorf("score source = %q (%v), want job", src, err)
	}

	// an unknown operator is refused
	bad := jobTake("01900000-0000-7000-8000-00000000a002", rid, lt.TrackLuna, seed.JobID, seed.EnvID)
	bad.Mutation = &library.NewMutation{Operator: "teleport"}
	if err := e.svc.InsertTake(ctx, tx, bad); err == nil {
		t.Error("unknown operator accepted")
	}
}

func TestInsertTake_ImportTakesLeaveM3ColumnsAlone(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var origin, by string
	var job sql.NullString
	if err := e.st.R.QueryRow(`SELECT origin, status_set_by, job_id FROM tracks WHERE id = ?`, lt.TrackLuna).Scan(&origin, &by, &job); err != nil {
		t.Fatal(err)
	}
	if origin != "import" || by != "import" || job.Valid {
		t.Errorf("import track = %q %q %v", origin, by, job)
	}
}

func TestInsertTake_JobSkipsImporterFulfilment(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	seed := lt.SeedJob(t, e.st, lt.FixedNow, lt.JobSeed{Name: "f"})
	// A backlog draft whose source recipe is the very recipe a job take renders must stay in the backlog.
	var rid, draft string
	if err := e.st.R.QueryRow(`SELECT source_recipe_id, id FROM recipe_drafts WHERE status = 'backlog' AND source_recipe_id IS NOT NULL ORDER BY id LIMIT 1`).Scan(&rid, &draft); err != nil {
		t.Fatal(err)
	}
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var out library.Fulfilment
	nt := jobTake("01900000-0000-7000-8000-00000000a003", rid, lt.TrackLuna, seed.JobID, seed.EnvID)
	nt.Out = &out
	if err := e.svc.InsertTake(ctx, tx, nt); err != nil {
		t.Fatal(err)
	}
	var st string
	if err := tx.QueryRow(`SELECT status FROM recipe_drafts WHERE id = ?`, draft).Scan(&st); err != nil || st != "backlog" {
		t.Errorf("draft status = %q (%v), want backlog", st, err)
	}
	if out.OwnDraftID != "" || len(out.CoreDrafts) != 0 {
		t.Errorf("fulfilment reported %+v", out)
	}
}
