package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestMigration0008_SeedsQueue upgrades a schema-7 library (the M3 fixture) and checks that the migration seeded one
// queue row per source row: runs, distinct collection names, batches, drafts, tracks (D-L1-4).
func TestMigration0008_SeedsQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "mm.db")
	raw, err := store.OpenRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateTo(ctx, raw, 7); err != nil {
		t.Fatal(err)
	}
	v7 := &store.Store{W: raw, R: raw}
	librarytest.FixtureM3(t, v7, librarytest.FixedNow)
	src := map[string]string{
		"run": `SELECT COUNT(*) FROM import_runs`, "collection": `SELECT COUNT(DISTINCT name) FROM draft_collections`,
		"batch": `SELECT COUNT(*) FROM batches`, "draft": `SELECT COUNT(*) FROM recipe_drafts`, "track": `SELECT COUNT(*) FROM tracks`,
	}
	want := map[string]int{}
	for k, q := range src {
		if err := raw.QueryRowContext(ctx, q).Scan(new(int)); err != nil {
			t.Fatal(err)
		}
		var n int
		_ = raw.QueryRowContext(ctx, q).Scan(&n)
		want[k] = n
		if n == 0 {
			t.Fatalf("fixture has no %s rows; the test would be vacuous", k)
		}
	}
	raw.Close()
	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v < 8 {
		t.Fatalf("version %d", v)
	}
	for kind, n := range want {
		var got int
		if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM provenance_dirty WHERE kind = ?`, kind).Scan(&got); err != nil || got != n {
			t.Errorf("queue rows of kind %s = %d (%v), want %d", kind, got, err, n)
		}
	}
	var songs int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM songs`).Scan(&songs); err != nil || songs != 0 {
		t.Errorf("the migration must create no Song: %d", songs)
	}
}

// TestFixtureL1Upgrade_IsSchema7 writes the L1 fixture into a schema-7 store: it predates 0008 and carries the L1 rows.
func TestFixtureL1Upgrade_IsSchema7(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	raw, err := store.OpenRaw(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := store.MigrateTo(ctx, raw, 7); err != nil {
		t.Fatal(err)
	}
	v7 := &store.Store{W: raw, R: raw}
	info := librarytest.FixtureL1Upgrade(t, v7, librarytest.FixedNow)
	var v, n int
	if err := raw.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil || v != 7 {
		t.Fatalf("user_version = %d (%v), want 7", v, err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('songs','muses','provenance_dirty')`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the upgrade fixture must predate 0008: %d L1 tables (%v)", n, err)
	}
	if len(info.IndexDrafts) != 18 {
		t.Errorf("index drafts = %d, want 18", len(info.IndexDrafts))
	}
}

// TestMigration0008_UpgradeFromM3 upgrades a schema-7 library holding the L1 fixture: every earlier row is unchanged,
// the queue holds one row per source row and no derived row exists yet. From a schema-6 FixtureM2c both 0007 and 0008 apply.
func TestMigration0008_UpgradeFromM3(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, from := range []int{7, 6} {
		p := filepath.Join(t.TempDir(), "up.db")
		raw, err := store.OpenRaw(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MigrateTo(ctx, raw, from); err != nil {
			t.Fatal(err)
		}
		old := &store.Store{W: raw, R: raw}
		if from == 7 {
			librarytest.FixtureL1Upgrade(t, old, librarytest.FixedNow)
		} else {
			librarytest.FixtureM2c(t, old, librarytest.FixedNow)
		}
		snap := func(db interface {
			QueryRowContext(context.Context, string, ...any) *sql.Row
		}) []string {
			var out []string
			for _, q := range []string{
				`SELECT group_concat(id||recipe_id||title||status||master_sha256||lossy) FROM (SELECT * FROM tracks ORDER BY id)`,
				`SELECT group_concat(id||request_id||status||created_at) FROM (SELECT * FROM recipe_drafts ORDER BY id)`,
				`SELECT group_concat(recipe_id||source||ref_json) FROM (SELECT * FROM recipe_refs ORDER BY recipe_id, source)`,
				`SELECT group_concat(id||state) FROM (SELECT * FROM import_runs ORDER BY id)`,
				`SELECT group_concat(draft_id||name) FROM (SELECT * FROM draft_collections ORDER BY draft_id, name)`,
			} {
				var v sql.NullString
				if err := db.QueryRowContext(ctx, q).Scan(&v); err != nil {
					t.Fatal(err)
				}
				out = append(out, v.String)
			}
			return out
		}
		before := snap(raw)
		var tracks, drafts, runs, batches, colls int
		for q, dst := range map[string]*int{
			`SELECT COUNT(*) FROM tracks`: &tracks, `SELECT COUNT(*) FROM recipe_drafts`: &drafts, `SELECT COUNT(*) FROM import_runs`: &runs,
			`SELECT COUNT(DISTINCT name) FROM draft_collections`: &colls,
		} {
			if err := raw.QueryRowContext(ctx, q).Scan(dst); err != nil {
				t.Fatal(err)
			}
		}
		if from == 7 {
			if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches`).Scan(&batches); err != nil {
				t.Fatal(err)
			}
		}
		raw.Close()
		s, err := store.Open(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := s.UserVersion(ctx); v < 8 {
			t.Fatalf("from %d: version %d", from, v)
		}
		after := snap(s.R)
		for i := range before {
			if before[i] != after[i] {
				t.Errorf("from %d: snapshot %d changed by the migration", from, i)
			}
		}
		for kind, want := range map[string]int{"track": tracks, "draft": drafts, "run": runs, "collection": colls, "batch": batches} {
			var got int
			if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM provenance_dirty WHERE kind = ?`, kind).Scan(&got); err != nil || got != want {
				t.Errorf("from %d: queue rows of kind %s = %d (%v), want %d", from, kind, got, err, want)
			}
		}
		rows, err := s.R.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			t.Fatal(err)
		}
		if rows.Next() {
			t.Errorf("from %d: foreign_key_check is not empty", from)
		}
		rows.Close()
		s.Close()
	}
}

// TestBackfill_UpgradePath: a schema-7 library holding the L1 fixture is upgraded by store.Open (0008 seeds the queue)
// and one Settle gives the same Songs, Muses and sessions as building the fixture on a migrated database.
func TestBackfill_UpgradePath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "up.db")
	raw, err := store.OpenRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateTo(ctx, raw, 7); err != nil {
		t.Fatal(err)
	}
	librarytest.FixtureL1Upgrade(t, &store.Store{W: raw, R: raw}, librarytest.FixedNow)
	raw.Close()
	up, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	fresh, err := store.Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	librarytest.FixtureL1(t, fresh, librarytest.FixedNow)
	for _, st := range []*store.Store{up, fresh} {
		prov := provenance.New(provenance.Options{Store: st, Now: func() time.Time { return librarytest.FixedNow }})
		if _, err := prov.Settle(ctx); err != nil {
			t.Fatal(err)
		}
		if an, err := prov.Verify(ctx); err != nil || len(an) != 0 {
			t.Errorf("Verify: %v %v", an, err)
		}
	}
	count := func(st *store.Store, q string) int {
		var n int
		if err := st.R.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, q := range []string{`SELECT COUNT(*) FROM songs`, `SELECT COUNT(*) FROM muses`, `SELECT COUNT(*) FROM sessions`, `SELECT COUNT(*) FROM song_roots`,
		`SELECT COUNT(*) FROM songs WHERE muse_id IS NOT NULL`} {
		if a, b := count(up, q), count(fresh, q); a != b || a == 0 {
			t.Errorf("%s: upgraded %d, fresh %d", q, a, b)
		}
	}
}
