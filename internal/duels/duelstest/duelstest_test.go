package duelstest_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

func count(t *testing.T, st *store.Store, q string, a ...any) int {
	t.Helper()
	var n int
	if err := st.R.QueryRowContext(context.Background(), q, a...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFixture_Rows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fx := lt.Fixture(t, st, lt.FixedNow)
	d := duelstest.Fixture(t, st, dir, fx, lt.FixedNow, nil)
	if d.Alpha != duelstest.TrackAlpha || d.Open != duelstest.DuelOpen {
		t.Fatalf("ids = %+v", d)
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM duels`:                                                                                          4,
		`SELECT count(*) FROM duels WHERE blind = 1`:                                                                          3,
		`SELECT count(*) FROM duel_verdicts`:                                                                                  3,
		`SELECT count(*) FROM duel_verdicts WHERE source = 'critic'`:                                                          1,
		`SELECT count(*) FROM duel_verdicts WHERE source = 'human'`:                                                           2,
		`SELECT count(*) FROM duels WHERE expires_at < '2026-10-06'`:                                                          1, // the expired one
		`SELECT count(*) FROM mutations WHERE operator='inferred_sibling' AND child_track_id = '` + duelstest.TrackBeta + `'`: 1,
	} {
		if got := count(t, st, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	refs, err := library.New(st, nil, nil).TrackRefs(context.Background(), []string{d.Alpha, d.Beta})
	if err != nil {
		t.Fatal(err)
	}
	a, b := refs[d.Alpha], refs[d.Beta]
	if a.Title != duelstest.TitleAlpha || a.Seed != duelstest.SeedAlpha || a.RequestID != duelstest.RequestAlpha || a.DurationS != duelstest.DurAlpha ||
		b.Title != duelstest.TitleBeta || b.ParentTrackID != d.Alpha || b.ParentOperator != "inferred_sibling" || b.RootTrackID != d.Alpha {
		t.Errorf("refs = %+v / %+v", a, b)
	}
	// the master files exist and match their recorded sha
	for _, r := range []library.TrackRef{a, b} {
		raw, err := os.ReadFile(filepath.Join(dir, r.MasterPath))
		if err != nil || library.SHA256Hex(raw) != r.MasterSHA256 {
			t.Errorf("master of %s: %v", r.Title, err)
		}
	}
}
