package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// enginesMigration mirrors the constant of the internal schema_e2_test.go: the position of 0015_engines.sql.
const enginesMigration = 15

// dbSnapshot is every row of every table (sqlite_* and the new E2 tables excluded), as sorted text per table.
func dbSnapshot(t *testing.T, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) map[string][]string {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '%\_fts%' ESCAPE '\'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	out := map[string][]string{}
	for _, tb := range tables {
		r, err := db.QueryContext(ctx, `SELECT * FROM "`+tb+`"`)
		if err != nil {
			continue // virtual tables of the search index
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var sb strings.Builder
			for i, v := range vals {
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				fmt.Fprintf(&sb, "%s=%v|", cols[i], v)
			}
			out[tb] = append(out[tb], sb.String())
		}
		r.Close()
		sort.Strings(out[tb])
	}
	return out
}

// TestUpgrade_E2_FromEveryPriorSchema populates a library at each schema since the generation tables exist (7 = M3 ...
// 13 = close-the-loop), migrates it to the schema before the engines migration, lets store.Open apply the engines
// migration, and checks that every row of every table is unchanged, the foreign keys are clean, the jobs table was rebuilt (no jobs_new left) and keeps its triggers.
// The fixtures hold draft-only Songs, reference and lossy imports, multi-part imports, Songs with and without Picks and
// jobs in several states.
func TestUpgrade_E2_FromEveryPriorSchema(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		version int
		fixture func(testing.TB, *store.Store) string
	}{
		{7, func(tb testing.TB, s *store.Store) string {
			librarytest.FixtureM3(tb, s, librarytest.FixedNow)
			return "m3"
		}},
		{9, func(tb testing.TB, s *store.Store) string {
			librarytest.FixtureL2Upgrade(tb, s, librarytest.FixedNow)
			return "l2"
		}},
		{10, func(tb testing.TB, s *store.Store) string {
			librarytest.FixtureL3Upgrade(tb, s, librarytest.FixedNow)
			return "l3"
		}},
		{11, func(tb testing.TB, s *store.Store) string {
			librarytest.FixtureL3Upgrade(tb, s, librarytest.FixedNow)
			return "l3"
		}},
		{12, func(tb testing.TB, s *store.Store) string {
			librarytest.FixtureL3Upgrade(tb, s, librarytest.FixedNow)
			return "l3"
		}},
		{13, func(tb testing.TB, s *store.Store) string {
			librarytest.FixtureM5(tb, s, librarytest.FixedNow)
			return "m5"
		}},
	} {
		t.Run(fmt.Sprintf("from_%d", c.version), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			prev, p := openAt(t, c.version)
			c.fixture(t, prev)
			// bring the populated library to the schema just before the engines migration with the earlier migrations, so
			// that the comparison isolates this migration (earlier ones backfill and add columns by design)
			if err := store.MigrateTo(ctx, prev.W, enginesMigration-1); err != nil {
				t.Fatal(err)
			}
			before := dbSnapshot(t, prev.R)
			if len(before["jobs"]) == 0 || len(before["tracks"]) == 0 {
				t.Fatalf("thin fixture: %d jobs, %d tracks", len(before["jobs"]), len(before["tracks"]))
			}
			prev.W.Close()
			s, err := store.Open(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < enginesMigration {
				t.Fatalf("version=%d, migrations=%d", v, store.MigrationCount())
			}
			after := dbSnapshot(t, s.R)
			for tb, rows := range before {
				if strings.Join(rows, "\n") != strings.Join(after[tb], "\n") {
					t.Errorf("table %s changed by the upgrade: %d rows before, %d after", tb, len(rows), len(after[tb]))
				}
			}
			if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
				t.Errorf("foreign_key_check rows = %s", n)
			}
			if n := scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE sql LIKE '%jobs_new%'`); n != "0" {
				t.Errorf("%s schema objects still mention jobs_new", n)
			}
			if n := scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE tbl_name='jobs' AND type='trigger'`); n < "6" {
				t.Errorf("jobs has %s triggers after the rebuild", n)
			}
			if scalar(t, s.R, `SELECT COUNT(*) FROM engine_adoption`) != "1" || scalar(t, s.R, `SELECT COUNT(*) FROM engine_rtf`) != "0" {
				t.Error("the new tables do not start as seeded")
			}
			// the rebuilt table is live: a take above the old 360 s ceiling is accepted
			var batch, recipe string
			if err := s.R.QueryRowContext(ctx, `SELECT batch_id, recipe_id FROM jobs ORDER BY id LIMIT 1`).Scan(&batch, &recipe); err != nil {
				t.Fatal(err)
			}
			if _, err := s.W.ExecContext(ctx, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
				VALUES('j-e2-up',?,999,'generate','acestep15','fake',?,'reroll_seed','{}','T',600,50,120,10,20,'queued','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`, batch, recipe); err != nil {
				t.Errorf("600 s job refused after the upgrade: %v", err)
			}
		})
	}
}

// TestUpgrade_E2_PatchLinkedJobSurvives: the jobs rebuild keeps the close-the-loop column (patch_id), its index, its
// frozen trigger and the foreign key to patches.
func TestUpgrade_E2_PatchLinkedJobSurvives(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, p := openAt(t, enginesMigration-1)
	librarytest.FixtureM5(t, prev, librarytest.FixedNow)
	var batch, recipe string
	if err := prev.R.QueryRowContext(ctx, `SELECT batch_id, recipe_id FROM jobs ORDER BY id LIMIT 1`).Scan(&batch, &recipe); err != nil {
		t.Fatal(err)
	}
	const at = "2026-01-01T00:00:00.000Z"
	if _, err := prev.W.ExecContext(ctx, `INSERT INTO patches(id,base_track_id,base_recipe_hash,operator,ops_json,compiled_json,scope_json,diff_json,rationale,principal_kind,principal_id,principal_name,created_at)
		VALUES('p-up',?,'h','lyric_edit','[]','{}','{}','{}','why','agent','a','a',?)`, librarytest.TrackLuna, at); err != nil {
		t.Fatal(err)
	}
	if _, err := prev.W.ExecContext(ctx, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at,patch_id)
		VALUES('j-patch',?,998,'generate','yue2','fake',?,'lyric_edit','{}','T',216,96,186,10,20,'queued',?,?,?,'p-up')`, batch, recipe, at, at, at); err != nil {
		t.Fatal(err)
	}
	prev.W.Close()
	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := scalar(t, s.R, `SELECT patch_id FROM jobs WHERE id='j-patch'`); got != "p-up" {
		t.Errorf("patch_id = %q", got)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE jobs SET patch_id = NULL WHERE id='j-patch'`); err == nil || !strings.Contains(err.Error(), "jobs: frozen column") {
		t.Errorf("patch_id is not frozen: %v", err)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_list('jobs') WHERE "table"='patches'`); n != "1" {
		t.Errorf("patches FK on jobs: %s", n)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
		t.Errorf("foreign_key_check rows = %s", n)
	}
}
