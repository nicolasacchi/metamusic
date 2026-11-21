package library_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

const recoveredChild = "01900000-0000-7000-8000-00000000b001"

// recoverTake inserts a job take with operator recover that names ref as the reference it was recovered from.
func recoverTake(t *testing.T, e *env, tx *sql.Tx, seed lt.SeededJob, id, ref string, params map[string]any, mod func(*library.NewTake)) error {
	t.Helper()
	ctx := context.Background()
	rid, _, err := e.svc.InsertRecipe(ctx, tx, jobRecipe(t, "rec-song-"+id[len(id)-3:], 5), "agent:tok1")
	if err != nil {
		t.Fatal(err)
	}
	nt := jobTake(id, rid, "", seed.JobID, seed.EnvID)
	nt.RootTrackID, nt.Generation = "", 0
	if params == nil {
		params = map[string]any{"mode": "from_draft", "recovered_from": ref}
	}
	nt.Mutation = &library.NewMutation{Operator: "recover", Params: params, JobID: seed.JobID}
	if mod != nil {
		mod(&nt)
	}
	return e.svc.InsertTake(ctx, tx, nt)
}

func TestOperatorRecover_NamesAgree(t *testing.T) {
	t.Parallel()
	if library.OperatorRecover != lineage.OpRecover || !lineage.ValidOperator(lineage.OpRecover) {
		t.Errorf("library %q, lineage %q", library.OperatorRecover, lineage.OpRecover)
	}
}

func TestInsertMutation_RecoverParentIsReference(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, lt.FixtureM2c)
	ctx := context.Background()
	seed := lt.SeedJob(t, e.st, lt.FixedNow, lt.JobSeed{Name: "r"})
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := recoverTake(t, e, tx, seed, recoveredChild, lt.TrackRefFlac, nil, nil); err != nil {
		t.Fatal(err)
	}
	var op string
	var parent, params sql.NullString
	if err := tx.QueryRow(`SELECT operator, parent_track_id, params_json FROM mutations WHERE child_track_id = ?`, recoveredChild).Scan(&op, &parent, &params); err != nil {
		t.Fatal(err)
	}
	if op != "recover" || parent.String != lt.TrackRefFlac || !strings.Contains(params.String, `"recovered_from":"`+lt.TrackRefFlac+`"`) {
		t.Errorf("mutation = %q parent %q params %s", op, parent.String, params.String)
	}
}

func TestRecover_ChildIsNewRoot(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, lt.FixtureM2c)
	ctx := context.Background()
	seed := lt.SeedJob(t, e.st, lt.FixedNow, lt.JobSeed{Name: "r"})
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := recoverTake(t, e, tx, seed, recoveredChild, lt.TrackRefFlac, nil, nil); err != nil {
		t.Fatal(err)
	}
	var parent sql.NullString
	var root string
	var gen int
	if err := tx.QueryRow(`SELECT parent_track_id, root_track_id, generation FROM tracks WHERE id = ?`, recoveredChild).Scan(&parent, &root, &gen); err != nil {
		t.Fatal(err)
	}
	if parent.Valid || root != recoveredChild || gen != 0 {
		t.Errorf("track parent=%v root=%s generation=%d, want a new root", parent, root, gen)
	}
	// the reference is untouched: same row, no child
	if n := 0; tx.QueryRow(`SELECT COUNT(*) FROM tracks WHERE parent_track_id = ?`, lt.TrackRefFlac).Scan(&n) != nil || n != 0 {
		t.Errorf("the reference has %d children", n)
	}
}

func TestInsertMutation_RecoverRefusals(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, lt.FixtureM2c)
	ctx := context.Background()
	n := 0
	for name, c := range map[string]struct {
		params map[string]any
		mod    func(*library.NewTake)
	}{
		"no recovered_from":    {params: map[string]any{"mode": "from_draft"}},
		"empty recovered_from": {params: map[string]any{"recovered_from": ""}},
		"not a string":         {params: map[string]any{"recovered_from": 7}},
		"unknown track":        {params: map[string]any{"recovered_from": "no-such-track"}},
		"a known take":         {params: map[string]any{"recovered_from": lt.TrackLuna}},
		"with a parent":        {mod: func(nt *library.NewTake) { nt.ParentTrackID, nt.RootTrackID = lt.TrackLuna, lt.TrackLuna }},
	} {
		n++
		seed := lt.SeedJob(t, e.st, lt.FixedNow, lt.JobSeed{Name: "x" + string(rune('a'+n))})
		tx, err := e.st.W.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		id := "01900000-0000-7000-8000-00000000b1" + string(rune('0'+n/10)) + string(rune('0'+n%10))
		if err := recoverTake(t, e, tx, seed, id, lt.TrackRefFlac, c.params, c.mod); err == nil {
			t.Errorf("%s was accepted", name)
		}
		_ = tx.Rollback()
	}
}
