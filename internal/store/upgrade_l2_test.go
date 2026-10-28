package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// curationMigration mirrors the constant of the internal schema_l2_test.go: the position of 0010_curation.sql.
const curationMigration = 10

var l2Tables = []string{"records", "record_slots", "record_changes", "crates", "crate_items", "tapes"}

func openAt(t *testing.T, version int) (*store.Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mm.db")
	raw, err := store.OpenRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateTo(context.Background(), raw, version); err != nil {
		t.Fatal(err)
	}
	return &store.Store{W: raw, R: raw}, p
}

func scalar(t *testing.T, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, q string, args ...any) string {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRowContext(context.Background(), q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v.String
}

// TestFixtureL2Upgrade_IsSchema9: the upgrade fixture predates 0010: it holds the Songs, Takes, Muses and Picks of
// FixtureL2 and none of the L2 tables or the ordinal column.
func TestFixtureL2Upgrade_IsSchema9(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v9, _ := openAt(t, curationMigration-1)
	defer v9.W.Close()
	info := librarytest.FixtureL2Upgrade(t, v9, librarytest.FixedNow)
	if v, _ := v9.UserVersion(ctx); v != curationMigration-1 {
		t.Fatalf("version %d", v)
	}
	for _, tb := range l2Tables {
		if n := scalar(t, v9.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, tb); n != "0" {
			t.Errorf("schema 9 already has %s", tb)
		}
	}
	if n := scalar(t, v9.R, `SELECT COUNT(*) FROM pragma_table_info('muses') WHERE name = 'ordinal'`); n != "0" {
		t.Error("schema 9 already has muses.ordinal")
	}
	if len(info.Songs) != 7 || len(info.Takes) != 8 || len(info.Records) != 0 {
		t.Errorf("songs %d takes %d records %d", len(info.Songs), len(info.Takes), len(info.Records))
	}
	for _, q := range []string{`SELECT COUNT(*) FROM songs WHERE title LIKE 'Synthetic Song S_'`, `SELECT COUNT(*) FROM song_picks`, `SELECT COUNT(*) FROM pick_events`,
		`SELECT COUNT(*) FROM muses`, `SELECT COUNT(*) FROM analyses WHERE analyzer = 'loudness'`} {
		if scalar(t, v9.R, q) == "0" {
			t.Errorf("%s = 0: the upgrade fixture is thin", q)
		}
	}
}

// TestMigration0010_UpgradeFromMain opens a schema-9 library holding the L1 fixture and the L2 Songs, lets store.Open apply
// 0010 and checks that every earlier row is unchanged, the L2 tables are empty and the Muses were ranked.
func TestMigration0010_UpgradeFromMain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, p := openAt(t, curationMigration-1)
	info := librarytest.FixtureL2Upgrade(t, prev, librarytest.FixedNow)
	if v, _ := prev.UserVersion(ctx); v != curationMigration-1 {
		t.Fatalf("setup version=%d", v)
	}
	snap := func(db interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}) []string {
		var out []string
		for _, q := range []string{
			`SELECT group_concat(id||recipe_id||title||status||master_sha256||created_at) FROM (SELECT * FROM tracks ORDER BY id)`,
			`SELECT group_concat(id||engine||request_id||request_json||created_by) FROM (SELECT * FROM recipes ORDER BY id)`,
			`SELECT group_concat(id||COALESCE(root_track_id,'')||COALESCE(muse_id,'')||title||created_at) FROM (SELECT * FROM songs ORDER BY id)`,
			`SELECT group_concat(root_track_id||song_id||via) FROM (SELECT * FROM song_roots ORDER BY root_track_id)`,
			`SELECT group_concat(song_id||take_id||source||last_event_id||evidence_json) FROM (SELECT * FROM song_picks ORDER BY song_id)`,
			`SELECT group_concat(id||song_id||seq||reason||evidence_json) FROM (SELECT * FROM pick_events ORDER BY id)`,
			`SELECT group_concat(id||kind||title||title_fold||COALESCE(artist,'')||key||created_at||revision) FROM (SELECT * FROM muses ORDER BY id)`,
			`SELECT group_concat(track_id||source||stars||principal_id) FROM (SELECT * FROM ratings ORDER BY track_id, source)`,
			`SELECT group_concat(id||track_id||kind) FROM (SELECT * FROM listen_events ORDER BY id)`,
			`SELECT group_concat(track_id||analyzer||version||metrics_json) FROM (SELECT * FROM analyses ORDER BY track_id, analyzer, version)`,
			`SELECT group_concat(id||origin||status||request_json) FROM (SELECT * FROM recipe_drafts ORDER BY id)`,
			`SELECT group_concat(kind||id) FROM (SELECT * FROM provenance_dirty ORDER BY kind, id)`,
			`SELECT group_concat(key||value) FROM (SELECT * FROM settings ORDER BY key)`,
		} {
			out = append(out, scalar(t, db, q))
		}
		return out
	}
	before := snap(prev.R)
	for i, b := range before {
		if b == "" && i != len(before)-1 { // settings may be empty
			t.Fatalf("snapshot %d empty: the fixture is too thin", i)
		}
	}
	prev.W.Close()

	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < curationMigration {
		t.Fatalf("version=%d, want %d", v, store.MigrationCount())
	}
	after := snap(s.R)
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("row set %d changed by the upgrade:\n%s\n%s", i, before[i], after[i])
		}
	}
	for _, tb := range l2Tables {
		if n := scalar(t, s.R, `SELECT COUNT(*) FROM `+tb); n != "0" {
			t.Errorf("%s has %s rows after the upgrade", tb, n)
		}
	}
	// the two Muses of the fixture were ranked; their ordinals are 1 and 2 in (created_at, id) order
	want := []string{info.Muses["M1"], info.Muses["M2"]}
	rows, err := s.R.QueryContext(ctx, `SELECT id FROM muses ORDER BY ordinal`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("muses by ordinal %v, want %v", got, want)
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
		t.Errorf("foreign_key_check rows = %s", n)
	}
	// the fixture's world now accepts Records over its Songs (the migration's triggers are live)
	if _, err := s.W.ExecContext(ctx, `INSERT INTO records(id,title,cover_seed,created_by,created_at,updated_by,updated_at) VALUES('r-up','Synthetic Upgrade Record',1,'human:h',?,'human:h',?)`,
		store.FormatTime(librarytest.FixedNow), store.FormatTime(librarytest.FixedNow)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO record_slots(id,record_id,song_id,added_by,added_at) VALUES('sl-up','r-up',?, 'human:h',?)`, info.Songs["S1"], store.FormatTime(librarytest.FixedNow)); err != nil {
		t.Fatal(err)
	}
}

// TestMigration0010_FromSchema5: 0006 to 0010 apply together in one Open on a schema-5 database.
func TestMigration0010_FromSchema5(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, p := openAt(t, 5)
	if v, _ := prev.UserVersion(ctx); v != 5 {
		t.Fatalf("setup version=%d", v)
	}
	prev.W.Close()
	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < curationMigration {
		t.Fatalf("version=%d", v)
	}
	for _, tb := range append([]string{"songs", "muses", "connector_grants"}, l2Tables...) {
		if n := scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, tb); n != "1" {
			t.Errorf("table %s missing after 5 -> head", tb)
		}
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
		t.Errorf("foreign_key_check rows = %s", n)
	}
}

// TestMigration0010_MuseOrdinalBackfill: 150 Muses written at schema 9 with equal created_at, inserted in scrambled id
// order, get the ordinals 1..150 in (created_at, id) order; an older Muse ranks before them whatever its id.
func TestMigration0010_MuseOrdinalBackfill(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, p := openAt(t, curationMigration-1)
	const n = 150
	same := store.FormatTime(librarytest.FixedNow)
	older := store.FormatTime(librarytest.FixedNow.Add(-48 * time.Hour))
	ins := func(id, at string) {
		t.Helper()
		if _, err := prev.W.ExecContext(ctx, `INSERT INTO muses(id,kind,title,title_fold,brief,key,source,created_by,created_at,updated_by,updated_at)
			VALUES(?,'brief','Synthetic Muse','synthetic muse','text',?,'human','human:h',?,'human:h',?)`, id, "brief:"+id, at, at); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		ins(fmt.Sprintf("m-%03d", (i*37)%n), same) // a permutation of 0..149 (37 is coprime to 150)
	}
	ins("m-zzz", older) // the largest id, but created first
	prev.W.Close()
	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.R.QueryContext(ctx, `SELECT id, ordinal FROM muses ORDER BY ordinal`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var id string
		var ord int
		if err := rows.Scan(&id, &ord); err != nil {
			t.Fatal(err)
		}
		i++
		want := fmt.Sprintf("m-%03d", i-2)
		if i == 1 {
			want = "m-zzz"
		}
		if ord != i || id != want {
			t.Fatalf("ordinal %d is %s, want ordinal %d = %s", ord, id, i, want)
		}
	}
	if i != n+1 {
		t.Fatalf("%d muses ranked, want %d", i, n+1)
	}
	if got := scalar(t, s.R, `SELECT COUNT(DISTINCT ordinal) FROM muses`); got != fmt.Sprint(n+1) {
		t.Errorf("distinct ordinals %s", got)
	}
}

// TestMuses_OrdinalAssigned: a Muse inserted by muses.Service.Create and by EnsureReferenceTx after 0010 gets MAX+1;
// hiding and editing never change it, and a hidden Muse keeps its number (a later Muse never reuses it).
func TestMuses_OrdinalAssigned(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "mm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := 0
	newID := func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd2e000+n) }
	svc := muses.New(s, func() time.Time { return librarytest.FixedNow }, newID)
	human := auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "proxy"}
	brief := "text"
	a, err := svc.Create(ctx, human, muses.NewMuse{Kind: muses.KindBrief, Title: "Synthetic Muse A", Brief: &brief})
	if err != nil {
		t.Fatal(err)
	}
	ord := func(id string) string { return scalar(t, s.R, `SELECT ordinal FROM muses WHERE id = ?`, id) }
	if ord(a.ID) != "1" {
		t.Fatalf("first ordinal %s", ord(a.ID))
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	artist := "Synthetic Artist B"
	bID, created, err := muses.EnsureReferenceTx(ctx, tx, librarytest.FixedNow, newID, "Synthetic Muse B", &artist, "system:provenance")
	if err != nil || !created {
		t.Fatalf("ensure: %v %v", created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if ord(bID) != "2" {
		t.Errorf("ordinal of the ensured Muse %s", ord(bID))
	}
	hide := true
	if _, err := svc.Update(ctx, human, a.ID, muses.Patch{Revision: a.Revision, Hidden: &hide}); err != nil {
		t.Fatal(err)
	}
	title := "Synthetic Muse A Renamed"
	cur, _ := svc.Get(ctx, a.ID)
	if _, err := svc.Update(ctx, human, a.ID, muses.Patch{Revision: cur.Revision, Title: &title}); err != nil {
		t.Fatal(err)
	}
	if ord(a.ID) != "1" {
		t.Errorf("ordinal after hide and rename %s", ord(a.ID))
	}
	c, err := svc.Create(ctx, human, muses.NewMuse{Kind: muses.KindBrief, Title: "Synthetic Muse C", Brief: &brief})
	if err != nil {
		t.Fatal(err)
	}
	if ord(c.ID) != "3" {
		t.Errorf("ordinal after a hidden Muse %s, want 3 (a number is never reused)", ord(c.ID))
	}
}

// recTB turns the Fatal of a fixture builder into a panic the test can catch.
type recTB struct{ testing.TB }

type fatalMsg string

func (recTB) Helper()                     {}
func (r recTB) Fatal(a ...any)            { panic(fatalMsg(fmt.Sprint(a...))) }
func (r recTB) Fatalf(f string, a ...any) { panic(fatalMsg(fmt.Sprintf(f, a...))) }
func (r recTB) Cleanup(func())            {}

// TestFixtureL2_NeedsSchema10: the Records half of the fixture refuses an older schema with a pointer to the migration.
func TestFixtureL2_NeedsSchema10(t *testing.T) {
	t.Parallel()
	old, _ := openAt(t, curationMigration-1)
	defer old.W.Close()
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg = fmt.Sprint(r)
			}
		}()
		librarytest.ApplyL2(recTB{t}, old, librarytest.FixedNow)
	}()
	if !strings.Contains(msg, "migration 0010") {
		t.Errorf("ApplyL2 on schema 9 stopped with %q", msg)
	}
}
