package library_test

import (
	"context"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func TestInsertTake_StatusSetAt(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	seed := lt.SeedJob(t, e.st, lt.FixedNow, lt.JobSeed{Name: "sa"})
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	rid, _, err := e.svc.InsertRecipe(ctx, tx, jobRecipe(t, "gen-sa-s9", 9), "agent:tok1")
	if err != nil {
		t.Fatal(err)
	}
	const id = "01900000-0000-7000-8000-00000000a0f1"
	if err := e.svc.InsertTake(ctx, tx, jobTake(id, rid, lt.TrackLuna, seed.JobID, seed.EnvID)); err != nil {
		t.Fatal(err)
	}
	var created, setAt string
	if err := tx.QueryRow(`SELECT created_at, status_set_at FROM tracks WHERE id = ?`, id).Scan(&created, &setAt); err != nil {
		t.Fatal(err)
	}
	if created == "" || setAt != created {
		t.Fatalf("created_at %q, status_set_at %q", created, setAt)
	}
}
