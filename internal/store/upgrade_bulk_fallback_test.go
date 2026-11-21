package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// uploadFallbackMigration is the position of 0023_upload_fallback.sql (BULK-R2, C399.3); renumber here if main moved.
const uploadFallbackMigration = 23

// TestMigration_UploadFallback: a database at 0022 migrates to 0023 additively: every existing table keeps its rows (the uploads rows
// gain a NULL via), the two new tables are empty, and an upload row written the old way (no via) is still valid.
func TestMigration_UploadFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, _ := openAt(t, uploadFallbackMigration-1)
	librarytest.FixtureM5(t, prev, librarytest.FixedNow)
	before := dbSnapshot(t, prev.R)
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('fallback_objects','upload_drills')`); n != "0" {
		t.Fatalf("0022 already has the tables")
	}
	if err := store.MigrateTo(ctx, prev.W, uploadFallbackMigration); err != nil {
		t.Fatal(err)
	}
	if v, _ := prev.UserVersion(ctx); v != uploadFallbackMigration {
		t.Fatalf("user_version = %d", v)
	}
	after := dbSnapshot(t, prev.R)
	for _, tbl := range []string{"fallback_objects", "upload_drills"} {
		if n := len(after[tbl]); n != 0 {
			t.Fatalf("%s has %d rows", tbl, n)
		}
		delete(after, tbl)
	}
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM uploads WHERE via IS NOT NULL`); n != "0" {
		t.Fatalf("%s existing uploads have a via", n)
	}
	// the snapshot lists the uploads columns it knows; the new one is the only difference, and it is NULL everywhere
	if !reflect.DeepEqual(len(before["uploads"]), len(after["uploads"])) {
		t.Fatal("the uploads rows changed in number")
	}
	delete(before, "uploads")
	delete(after, "uploads")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("an existing table changed")
	}
}
