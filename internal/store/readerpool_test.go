package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// The reader pool must refuse writes at the database level (CLAUDE.md rule 5:
// GET never mutates), on every pooled connection.
func TestReaderPoolIsQueryOnly(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/ro.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.W.ExecContext(ctx, `CREATE TABLE rp_probe (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("writer DDL: %v", err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO rp_probe (v) VALUES ('a')`); err != nil {
		t.Fatalf("writer insert: %v", err)
	}
	// Hold several reader connections open so the check covers more than one.
	st.R.SetMaxOpenConns(3)
	var conns []interface{ Close() error }
	for range 3 {
		c, err := st.R.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		for _, q := range []string{
			`INSERT INTO rp_probe (v) VALUES ('b')`,
			`UPDATE rp_probe SET v='z'`,
			`DELETE FROM rp_probe WHERE 0`,
			`CREATE TABLE rp_other (x)`,
			`DROP TABLE rp_probe`,
		} {
			_, err := c.ExecContext(ctx, q)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
				t.Errorf("%s via reader: want readonly error, got %v", q, err)
			}
		}
		tx, err := c.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM rp_probe WHERE 0`); err == nil {
			t.Error("DELETE in reader tx succeeded")
		}
		tx.Rollback()
	}
	for _, c := range conns {
		c.Close()
	}
	var n int
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM rp_probe`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("reads: n=%d err=%v", n, err)
	}
}
