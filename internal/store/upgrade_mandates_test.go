package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// mandatesMigration is the position of 0024_mandates.sql (M6N-M1, C370); renumber here and rename the file if main moved.
const mandatesMigration = 24

// TestMigration_Mandates: a database one step before the mandates migration upgrades additively: the table is new and empty, every
// existing estimate and batch keeps a NULL mandate_id and every other table is untouched.
func TestMigration_Mandates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, _ := openAt(t, mandatesMigration-1)
	librarytest.FixtureM5(t, prev, librarytest.FixedNow)
	before := dbSnapshot(t, prev.R)
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'mandates'`); n != "0" {
		t.Fatal("the previous schema already has the table")
	}
	if err := store.MigrateTo(ctx, prev.W, mandatesMigration); err != nil {
		t.Fatal(err)
	}
	if v, _ := prev.UserVersion(ctx); v != mandatesMigration {
		t.Fatalf("user_version = %d", v)
	}
	for _, name := range []string{"mandates", "mandates_holder", "batches_mandate", "mandates_no_delete", "mandates_frozen", "mandates_transition",
		"mandates_final", "estimates_mandate_frozen", "batches_mandate_frozen"} {
		if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name); n != "1" {
			t.Errorf("%s missing", name)
		}
	}
	after := dbSnapshot(t, prev.R)
	if n := len(after["mandates"]); n != 0 {
		t.Fatalf("mandates has %d rows", n)
	}
	delete(after, "mandates")
	for _, tbl := range []string{"estimates", "batches"} {
		if n := scalar(t, prev.R, `SELECT COUNT(*) FROM `+tbl+` WHERE mandate_id IS NOT NULL`); n != "0" {
			t.Fatalf("%s existing %s have a mandate", n, tbl)
		}
		if len(before[tbl]) != len(after[tbl]) {
			t.Fatalf("%s changed in number", tbl)
		}
		delete(before, tbl)
		delete(after, tbl)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("an existing table changed")
	}
}
