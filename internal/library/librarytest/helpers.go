package librarytest

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func begin(tb testing.TB, st *store.Store) *sql.Tx {
	tb.Helper()
	tx, err := st.W.BeginTx(context.Background(), nil)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func check(tb testing.TB, err error) {
	tb.Helper()
	if err != nil {
		tb.Fatal(err)
	}
}

func mustExec(tb testing.TB, st *store.Store, q string, a ...any) {
	tb.Helper()
	if _, err := st.W.ExecContext(context.Background(), q, a...); err != nil {
		tb.Fatal(err)
	}
}

func principalCLI() auth.Principal {
	return auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture"}
}
