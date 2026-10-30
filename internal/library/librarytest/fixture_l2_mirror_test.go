package librarytest

import "testing"

// TestFixtureL2Mirror_Params: the mirror has the documented shape and holds no L2 row; every text is synthetic.
func TestFixtureL2Mirror_Params(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	info := FixtureL2Mirror(t, st, FixedNow)
	if len(info.Originals) != 4 || len(info.Groups) != 5 || len(info.IndexDrafts) != 360 || len(info.DemoDrafts) != 5 {
		t.Fatalf("shape: %d originals, %d groups, %d index drafts, %d demo drafts", len(info.Originals), len(info.Groups), len(info.IndexDrafts), len(info.DemoDrafts))
	}
	for _, g := range info.Groups {
		if len(g) != 3 {
			t.Errorf("group of %d takes, want 3", len(g))
		}
	}
	for _, tbl := range []string{"records", "record_slots", "record_changes", "crates", "crate_items", "tapes"} {
		if n := count(t, st, `SELECT COUNT(*) FROM `+tbl); n != 0 {
			t.Errorf("%s holds %d rows, want 0", tbl, n)
		}
	}
	if n := count(t, st, `SELECT COUNT(*) FROM songs`); n != 0 {
		t.Errorf("%d songs before a Settle", n)
	}
	for _, p := range []string{"Synthetic Original 004", "Synthetic Pilot 005 p3"} {
		if n := count(t, st, `SELECT COUNT(*) FROM tracks WHERE title = ?`, p); n != 1 {
			t.Errorf("%q: %d tracks", p, n)
		}
	}
}
