package main

import (
	"context"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestPruneIdempotency_RunsAtStartAndStops(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// A row that expired two days ago.
	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := st.W.Exec(`INSERT INTO idempotency(principal_key,key,op,request_sha256,derived,state,created_at,lease_until,expires_at)
VALUES('agent:a','k','op','s',0,'pending',?,?,?)`, old, old, old); err != nil {
		t.Fatal(err)
	}
	pctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { pruneIdempotency(pctx, idem.NewStore(st, time.Now), time.Hour); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		_ = st.R.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&n)
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired row was not pruned at start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("prune loop did not stop with its context")
	}
}
