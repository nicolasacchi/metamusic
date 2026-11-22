package jobs

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// podsMigrationFile is the pod tables' migration (BULK-P3); renumber with the file if main moved.
const podsMigrationFile = "0026_pods.sql"

var podTables = []string{"pods", "pod_assignments", "pod_events", "pod_safety_log"}

// TestMigration_Pods_ServerlessPathUnchanged: a database migrated from the schema before the pod tables claims, submits, polls and
// fails a serverless-style job exactly as before: the pod tables stay empty, no trigger outside them mentions a pod, and the backend
// of the job and of its attempt is the one the set routed (nothing routes to a pod with the backend off).
func TestMigration_Pods_ServerlessPathUnchanged(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "s.db")
	// a database exactly as the previous migration left it (never latest-then-DROP, which breaks on every later migration)
	if err := store.CreateBefore(t.Context(), p, podsMigrationFile); err != nil {
		t.Fatal(err)
	}
	r := newRigAt(t, p, t.TempDir(), true) // store.Open migrates it to the latest
	for _, tbl := range podTables {
		if n := r.count(`SELECT COUNT(*) FROM ` + tbl); n != 0 {
			t.Fatalf("%s has %d rows after the migration", tbl, n)
		}
	}
	if n := r.count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name NOT LIKE 'pod%' AND lower(sql) LIKE '%pod%'`); n != 0 {
		t.Fatalf("%d triggers of other tables reach the pod tables", n)
	}
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	if spec.Wire == provider.ContractV2 {
		t.Fatal("a plain job was sent as /2")
	}
	if b := r.str(`SELECT backend FROM job_attempts WHERE job_id = ?`, job); b == "runpod-pod" {
		t.Fatalf("the attempt went to %s", b)
	}
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1_000), ErrorText: "SOMETHING: no"})
	r.advance(31 * time.Second)
	r.step()
	r.wantState(job, StateFailed)
	for _, tbl := range podTables {
		if n := r.count(`SELECT COUNT(*) FROM ` + tbl); n != 0 {
			t.Fatalf("the serverless path wrote %d rows to %s", n, tbl)
		}
	}
}
