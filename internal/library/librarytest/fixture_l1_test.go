package librarytest

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func openL1(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func count(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.R.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestFixtureL1_Shape(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	FixtureM3(t, st, FixedNow)
	refs0 := count(t, st, `SELECT COUNT(*) FROM recipe_refs`)
	drafts0 := count(t, st, `SELECT COUNT(*) FROM recipe_drafts`)
	ref0 := count(t, st, `SELECT COUNT(*) FROM tracks WHERE import_rule = 'reference'`)
	coll0 := count(t, st, `SELECT COUNT(DISTINCT name) FROM draft_collections`)
	runs0 := count(t, st, `SELECT COUNT(*) FROM import_runs`)
	mothered0 := count(t, st, `SELECT COUNT(*) FROM recipe_drafts WHERE parent_track_id IS NOT NULL`)
	st.Close()
	st = openL1(t)
	info := FixtureL1(t, st, FixedNow)
	if d := count(t, st, `SELECT COUNT(*) FROM recipe_refs`) - refs0; d != 18 {
		t.Errorf("refs rows +%d, want 18 (6 Muses x 3 angles)", d)
	}
	if d := count(t, st, `SELECT COUNT(*) FROM recipe_drafts`) - drafts0; d != 18+4+1 {
		t.Errorf("drafts +%d, want 18 index + 4 demo + 1 mothered", d)
	}
	if d := count(t, st, `SELECT COUNT(*) FROM tracks WHERE import_rule = 'reference'`) - ref0; d != 3+6 {
		t.Errorf("reference tracks +%d, want 3 originals + 2 groups of 3", d)
	}
	if d := count(t, st, `SELECT COUNT(DISTINCT name) FROM draft_collections`) - coll0; d != 2 {
		t.Errorf("collections +%d, want 2", d)
	}
	if d := count(t, st, `SELECT COUNT(*) FROM import_runs`) - runs0; d != 2 {
		t.Errorf("import runs +%d, want 2", d)
	}
	if d := count(t, st, `SELECT COUNT(*) FROM recipe_drafts WHERE parent_track_id IS NOT NULL`) - mothered0; d != 1 {
		t.Errorf("mothered drafts +%d, want 1", d)
	}
	for _, g := range info.Groups {
		if len(g) != 3 {
			t.Fatalf("group %v", g)
		}
		for _, child := range g[1:] {
			if n := count(t, st, `SELECT COUNT(*) FROM tracks WHERE id = ? AND parent_track_id = ? AND root_track_id = ?`, child, g[0], g[0]); n != 1 {
				t.Errorf("%s is not parented to %s", child, g[0])
			}
		}
	}
	if n := count(t, st, `SELECT COUNT(*) FROM songs`); n != 0 {
		t.Errorf("Settle must not run in the fixture: %d songs", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM provenance_dirty`); n == 0 {
		t.Errorf("the queue must hold the fixture's keys")
	}
	for _, a := range []string{"variant-a", "variant-b", "variant-c"} {
		if n := count(t, st, `SELECT COUNT(*) FROM recipe_refs WHERE json_extract(ref_json, '$.variant_type') = ?`, a); n != 6 {
			t.Errorf("angle %s: %d refs, want 6", a, n)
		}
	}
}

func TestFixtureL1Mirror_Params(t *testing.T) {
	t.Parallel()
	for _, p := range []L1Params{
		{Muses: 2, Angles: 3, DemoDrafts: 1, Originals: 1, Groups: 1, GroupSize: 2},
		{Muses: 4, Angles: 2, DemoDrafts: 0, Originals: 0, Groups: 2, GroupSize: 3},
		{Muses: 1, Angles: 1, DemoDrafts: 5, Originals: 2, Groups: 0, GroupSize: 3},
	} {
		st := openL1(t)
		FixtureM3(t, st, FixedNow)
		idx0 := count(t, st, `SELECT COUNT(*) FROM recipe_refs`)
		ref0 := count(t, st, `SELECT COUNT(*) FROM tracks WHERE import_rule = 'reference'`)
		st.Close()
		st = openL1(t)
		FixtureL1Mirror(t, st, FixedNow, p)
		if d := count(t, st, `SELECT COUNT(*) FROM recipe_refs`) - idx0; d != p.Muses*p.Angles {
			t.Errorf("%+v: refs +%d", p, d)
		}
		if d := count(t, st, `SELECT COUNT(*) FROM tracks WHERE import_rule = 'reference'`) - ref0; d != p.Originals+p.Groups*max(p.GroupSize, 1) {
			t.Errorf("%+v: reference tracks +%d", p, d)
		}
	}
	st := openL1(t)
	FixtureM3(t, st, FixedNow)
	FixtureL1Scale(t, st, FixedNow, 5, 3)
	if n := count(t, st, `SELECT COUNT(*) FROM recipe_refs WHERE json_extract(ref_json, '$.source_title') LIKE 'Synthetic Muse%'`); n != 15 {
		t.Errorf("scale fixture refs = %d, want 15", n)
	}
}

var syntheticRe = regexp.MustCompile(`^Synthetic [A-Za-z]+( [A-Za-z]+)*( \d{3}| \d+)?$`)

func TestFixtures_L1_SyntheticOnly(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	FixtureL1Mirror(t, st, FixedNow, L1Params{Muses: 5, Angles: 2, DemoDrafts: 2, Originals: 1, Groups: 1, GroupSize: 2})
	rows, err := st.R.Query(`SELECT json_extract(ref_json, '$.source_title'), json_extract(ref_json, '$.source_artist') FROM recipe_refs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var title, artist *string
		if err := rows.Scan(&title, &artist); err != nil {
			t.Fatal(err)
		}
		n++
		if title != nil && !syntheticRe.MatchString(*title) {
			t.Errorf("non-synthetic Muse title %q", *title)
		}
		if artist != nil && !syntheticRe.MatchString(*artist) {
			t.Errorf("non-synthetic artist %q", *artist)
		}
		if title != nil && strings.Contains(strings.ToLower(*title), "http") {
			t.Errorf("title looks like a URL: %q", *title)
		}
	}
	if n == 0 {
		t.Fatal("no refs rows")
	}
	for i := 1; i <= 3; i++ {
		if !syntheticRe.MatchString(L1MuseTitle(i)) || !syntheticRe.MatchString(L1MuseArtist(i)) {
			t.Errorf("helper names are not synthetic")
		}
	}
}
