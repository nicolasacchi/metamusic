package faultdb_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/store/faultdb"
)

func template(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.W.Exec(`CREATE TABLE fault_probe(n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`INSERT INTO fault_probe VALUES (1),(2),(3)`); err != nil {
		t.Fatal(err)
	}
	return faultdb.Snapshot(t, st, path)
}

// TestSweep_EveryCallKindFailsAndStateResets: an operation that writes in a transaction, reads rows and writes outside
// one sees the template at the start of every run, and every injected failure surfaces as ErrInjected.
func TestSweep_EveryCallKindFailsAndStateResets(t *testing.T) {
	t.Parallel()
	tpl := template(t)
	var runs int
	errored := faultdb.Sweep(t, tpl, func(ctx context.Context, st *store.Store) error {
		runs++
		var before int
		if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM fault_probe`).Scan(&before); err != nil {
			return err
		}
		if before != 3 {
			t.Errorf("run %d started with %d rows: the previous run was not rolled back", runs, before)
		}
		tx, err := st.W.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `INSERT INTO fault_probe VALUES (4)`); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT n FROM fault_probe ORDER BY n`)
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		_, err = st.W.ExecContext(ctx, `UPDATE fault_probe SET n = n + 1`)
		return err
	})
	if errored == 0 || runs < 6 {
		t.Fatalf("errored %d of %d runs", errored, runs)
	}
}

func TestSweep_SplitReaderSeesTheTemplate(t *testing.T) {
	t.Parallel()
	tpl := template(t)
	var sawStale bool
	faultdb.SweepSplit(t, tpl, func(ctx context.Context, st *store.Store) error {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO fault_probe VALUES (9)`); err != nil {
			return err
		}
		var n int
		tx, err := st.R.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM fault_probe`).Scan(&n); err != nil {
			return err
		}
		if n == 3 {
			sawStale = true // the reader does not see the writer's uncommitted row
		}
		return tx.Commit()
	})
	if !sawStale {
		t.Error("the split reader saw the writer's uncommitted row")
	}
}

func TestErrInjectedIsDistinct(t *testing.T) {
	t.Parallel()
	if !errors.Is(faultdb.ErrInjected, faultdb.ErrInjected) || errors.Is(faultdb.ErrInjected, context.Canceled) {
		t.Error("ErrInjected identity")
	}
}
