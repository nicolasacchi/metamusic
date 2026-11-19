package store_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// ratingsPerUserMigration is the position of 0017_ratings_per_user.sql (U1-R1, C306).
const ratingsPerUserMigration = 17

// TestMigration_U1 upgrades a populated schema-16 database (human, device-origin and agent ratings) through 0017 and checks
// C306.1 to C306.3: every human row becomes rater 'owner' (device-origin ones included), the agent row 'critic', nothing else
// changes, the triggers and indexes come back (the provenance ones only for the Owner's rows), the pairing CHECKs hold, the
// views read the right rows and the foreign keys are clean.
func TestMigration_U1(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, p := openAt(t, ratingsPerUserMigration-1)
	librarytest.FixtureM5(t, prev, librarytest.FixedNow)
	tracks := scalar(t, prev.R, `SELECT group_concat(id, ',') FROM (SELECT id FROM tracks ORDER BY id)`)
	ids := strings.Split(tracks, ",")
	if len(ids) < 6 {
		t.Fatalf("thin fixture: %d tracks", len(ids))
	}
	// a device-origin human row and a human row with a cleared rating, on tracks the fixture left unrated
	var spare []string
	for _, id := range ids {
		if scalar(t, prev.R, `SELECT COUNT(*) FROM ratings WHERE track_id = ?`, id) == "0" {
			spare = append(spare, id)
		}
	}
	if len(spare) < 2 {
		t.Fatalf("no unrated tracks in the fixture")
	}
	for _, q := range []struct {
		id, stars, kind, pid string
	}{{spare[0], "4", "device", "device:d1"}, {spare[1], "NULL", "human", "human:alice"}} {
		if _, err := prev.W.ExecContext(ctx, `INSERT INTO ratings(track_id,source,stars,verdict,principal_kind,principal_id,principal_name,created_at,updated_at)
			VALUES(?,'human',`+q.stars+`,'synthetic',?,?,'n','2026-10-14T10:00:00Z','2026-10-14T10:00:00Z')`, q.id, q.kind, q.pid); err != nil {
			t.Fatal(err)
		}
	}
	type row struct{ track, source, stars, verdict, kind, pid, name, created, updated string }
	read := func(s *store.Store) map[string]row {
		out := map[string]row{}
		rows, err := s.R.QueryContext(ctx, `SELECT track_id, source, COALESCE(stars,'-'), COALESCE(verdict,'-'), principal_kind, principal_id, principal_name, created_at, updated_at FROM ratings`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.track, &r.source, &r.stars, &r.verdict, &r.kind, &r.pid, &r.name, &r.created, &r.updated); err != nil {
				t.Fatal(err)
			}
			out[r.track+"/"+r.source] = r
		}
		return out
	}
	before := read(prev)
	var humans, agents int
	for _, r := range before {
		if r.source == "human" {
			humans++
		} else {
			agents++
		}
	}
	if humans < 3 || agents < 1 {
		t.Fatalf("thin fixture: %d human, %d agent ratings", humans, agents)
	}
	dirtyBefore := scalar(t, prev.R, `SELECT COUNT(*) FROM provenance_dirty`)
	prev.W.Close()

	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < ratingsPerUserMigration {
		t.Fatalf("version=%d, migrations=%d", v, store.MigrationCount())
	}

	// rows: same content, rater by source
	after := read(s)
	if len(after) != len(before) {
		t.Fatalf("%d rows before, %d after", len(before), len(after))
	}
	for k, b := range before {
		if after[k] != b {
			t.Errorf("row %s changed: %+v -> %+v", k, b, after[k])
		}
		want := "owner"
		if b.source == "agent" {
			want = "critic"
		}
		if got := scalar(t, s.R, `SELECT rater FROM ratings WHERE track_id = ? AND source = ?`, b.track, b.source); got != want {
			t.Errorf("row %s: rater %q, want %q", k, got, want)
		}
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM ratings WHERE source = 'human' AND rater <> 'owner'`); n != "0" {
		t.Errorf("%s human rows are not the Owner's", n)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM ratings WHERE principal_kind = 'device'`); n == "0" {
		t.Error("device-origin rating lost")
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM provenance_dirty`); n != dirtyBefore {
		t.Errorf("the copy touched the provenance queue: %s -> %s", dirtyBefore, n)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
		t.Errorf("foreign_key_check rows = %s", n)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE sql LIKE '%ratings\_new%' ESCAPE '\'`); n != "0" {
		t.Errorf("%s objects still mention ratings_new", n)
	}

	// views
	if got, want := scalar(t, s.R, `SELECT COUNT(*) FROM owner_ratings`), scalar(t, s.R, `SELECT COUNT(*) FROM ratings WHERE source='human' AND rater='owner'`); got != want || got != strconv.Itoa(humans) {
		t.Errorf("owner_ratings has %s rows, want %d (%s)", got, humans, want)
	}
	if got := scalar(t, s.R, `SELECT COUNT(*) FROM critic_ratings`); got != strconv.Itoa(agents) {
		t.Errorf("critic_ratings has %s rows, want %d", got, agents)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_table_info('owner_ratings') WHERE name = 'rater'`); n != "1" {
		t.Error("owner_ratings does not expose rater")
	}

	// triggers and indexes
	for _, name := range []string{"ratings_no_delete", "ratings_frozen", "pd_ratings_ai", "pd_ratings_au"} {
		if n := scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='ratings' AND name=?`, name); n != "1" {
			t.Errorf("trigger %s missing", name)
		}
	}
	for _, name := range []string{"ratings_source_stars", "ratings_track_rater"} {
		if n := scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name='ratings' AND name=?`, name); n != "1" {
			t.Errorf("index %s missing", name)
		}
	}
	if sqlText := scalar(t, s.R, `SELECT sql FROM sqlite_master WHERE name='pd_ratings_ai'`); !strings.Contains(sqlText, "rater = 'owner'") {
		t.Errorf("pd_ratings_ai lacks the owner-only WHEN: %s", sqlText)
	}
	if _, err := s.W.ExecContext(ctx, `DELETE FROM ratings`); err == nil || !strings.Contains(err.Error(), "no physical delete") {
		t.Errorf("ratings delete: %v", err)
	}
	for _, col := range []string{"track_id", "source", "rater", "created_at"} {
		if _, err := s.W.ExecContext(ctx, `UPDATE ratings SET `+col+` = `+col+` || 'x' WHERE source='agent'`); err == nil || !strings.Contains(err.Error(), "frozen") {
			t.Errorf("ratings.%s is not frozen: %v", col, err)
		}
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE ratings SET stars = 6 WHERE source='agent'`); err == nil {
		t.Error("stars CHECK lost")
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE ratings SET verdict = '' WHERE source='agent'`); err == nil {
		t.Error("verdict CHECK lost")
	}

	// pairing CHECKs and source/principal_kind CHECK
	ins := `INSERT INTO ratings(track_id,rater,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES(?,?,?,3,?,'p','n','2026-10-14T10:00:00Z','2026-10-14T10:00:00Z')`
	tr := ids[0]
	for _, c := range []struct{ rater, source, kind string }{
		{"critic", "human", "human"}, {"owner", "agent", "agent"}, {"bob", "agent", "agent"}, {"", "human", "human"},
		{"bob", "human", "agent"}, {"critic", "agent", "human"},
	} {
		if _, err := s.W.ExecContext(ctx, ins, tr, c.rater, c.source, c.kind); err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("insert %+v: %v", c, err)
		}
	}

	// a second rater does not enqueue provenance; the Owner's rows still do
	if _, err := s.W.ExecContext(ctx, `DELETE FROM provenance_dirty`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.ExecContext(ctx, ins, tr, "bob", "human", "human"); err != nil {
		t.Fatalf("second rater: %v", err)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE ratings SET stars = 5 WHERE track_id = ? AND rater = 'bob'`, tr); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM provenance_dirty WHERE id = ?`, tr); n != "0" {
		t.Errorf("a non-owner rating enqueued provenance (%s rows)", n)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM owner_ratings WHERE track_id = ? AND rater = 'bob'`, tr); n != "0" {
		t.Error("owner_ratings shows another rater")
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE ratings SET stars = 1 WHERE track_id = ? AND rater = 'owner' AND source = 'human'`, tr); err != nil {
		t.Fatalf("owner row: %v", err)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM provenance_dirty WHERE id = ?`, tr); n != "1" {
		t.Errorf("the Owner's rating did not enqueue provenance (%s rows)", n)
	}
	if _, err := s.W.ExecContext(ctx, ins, tr, "owner", "human", "human"); err == nil {
		t.Error("the Owner can rate a track twice")
	}
	// the upsert key of C306.5
	if _, err := s.W.ExecContext(ctx, `INSERT INTO ratings(track_id,rater,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at)
		VALUES(?,'bob','human',2,'human','p','n','2026-10-14T11:00:00Z','2026-10-14T11:00:00Z') ON CONFLICT(track_id, source, rater) DO UPDATE SET stars = excluded.stars`, tr); err != nil {
		t.Errorf("upsert on (track_id, source, rater): %v", err)
	}
}
