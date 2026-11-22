package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// nightShiftMigration is the position of 0029_night_shift.sql (M6N-N2, C377); renumber here and rename the file if main moved.
const nightShiftMigration = 29

// TestMigration_NightShift: a database one step before the night-shift migration upgrades additively. The night shift arrives OFF (the
// one settings row says enabled = 0, nothing is halted) and no night exists, so deploying it changes nothing until the Owner enables it.
func TestMigration_NightShift(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, _ := openAt(t, nightShiftMigration-1)
	librarytest.FixtureM5(t, prev, librarytest.FixedNow)
	before := dbSnapshot(t, prev.R)
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'night_shift%'`); n != "0" {
		t.Fatal("the previous schema already has the tables")
	}
	if err := store.MigrateTo(ctx, prev.W, nightShiftMigration); err != nil {
		t.Fatal(err)
	}
	if v, _ := prev.UserVersion(ctx); v != nightShiftMigration {
		t.Fatalf("user_version = %d", v)
	}
	for _, name := range []string{"night_shift_settings", "night_shift_runs", "night_shift_runs_batch", "night_shift_settings_no_delete", "night_shift_runs_no_delete",
		"night_shift_runs_frozen", "night_shift_runs_batch_once", "night_shift_runs_transition"} {
		if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name); n != "1" {
			t.Errorf("%s missing", name)
		}
	}
	if got := scalar(t, prev.R, `SELECT enabled || '|' || COALESCE(halted_at, '-') FROM night_shift_settings`); got != "0|-" {
		t.Fatalf("the shipped settings are %s, want off and not halted", got)
	}
	after := dbSnapshot(t, prev.R)
	if n := len(after["night_shift_runs"]); n != 0 {
		t.Fatalf("night_shift_runs has %d rows", n)
	}
	delete(after, "night_shift_runs")
	delete(after, "night_shift_settings")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("an existing table changed")
	}
}
