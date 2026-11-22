package store_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// podsMigration is the position of 0028_pods.sql (BULK-P3, C394); renumber here and rename the file if main moved.
const (
	podsMigration     = 28
	podsMigrationFile = "0028_pods.sql"
)

var podTableNames = []string{"pod_transitions", "pods", "pod_assignments", "pod_events", "pod_safety_log"}

// TestMigration_Pods: a database one step before the pods migration (built with CreateBefore) upgrades additively: the four tables are
// new (pod_transitions holds its 19 rows), every pod table that can be empty is, and every existing table is untouched.
func TestMigration_Pods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "mm.db")
	if err := store.CreateBefore(ctx, p, podsMigrationFile); err != nil {
		t.Fatal(err)
	}
	raw, err := store.OpenRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	prev := &store.Store{W: raw, R: raw}
	if v, _ := prev.UserVersion(ctx); v != podsMigration-1 {
		t.Fatalf("CreateBefore left user_version %d", v)
	}
	librarytest.FixtureM5(t, prev, librarytest.FixedNow)
	before := dbSnapshot(t, prev.R)
	for _, tbl := range podTableNames {
		if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, tbl); n != "0" {
			t.Fatalf("the previous schema already has %s", tbl)
		}
	}
	if err := store.MigrateTo(ctx, prev.W, podsMigration); err != nil {
		t.Fatal(err)
	}
	if v, _ := prev.UserVersion(ctx); v != podsMigration {
		t.Fatalf("user_version = %d", v)
	}
	for _, name := range []string{"pods_initial_state", "pods_frozen", "pods_transition_exists", "pods_settled_once", "pods_no_delete", "pods_overhead_reserved_once",
		"pod_transitions_no_insert", "pod_transitions_no_update", "pod_transitions_no_delete", "pod_assignments_no_delete", "pod_assignments_done_once",
		"pod_events_no_update", "pod_safety_log_no_update", "pods_live"} {
		if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name); n != "1" {
			t.Errorf("%s missing", name)
		}
	}
	after := dbSnapshot(t, prev.R)
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM pod_transitions`); n != "19" {
		t.Fatalf("pod_transitions has %s rows", n)
	}
	for _, tbl := range podTableNames {
		if tbl != "pod_transitions" && len(after[tbl]) != 0 {
			t.Fatalf("%s has %d rows", tbl, len(after[tbl]))
		}
		delete(after, tbl)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("an existing table changed")
	}
}
