package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// agentsAtlasMigration mirrors the constant of the internal schema_l3_test.go: the position of 0011_agents_atlas.sql.
const agentsAtlasMigration = 11

var l3Tables = []string{"take_features", "song_features", "record_sequences", "taxonomy_proposals"}

// TestFixtureL3Upgrade_IsSchema10: the upgrade fixture predates 0011: the L2 and L3 worlds and none of the L3 objects.
func TestFixtureL3Upgrade_IsSchema10(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, _ := openAt(t, agentsAtlasMigration-1)
	defer prev.W.Close()
	info := librarytest.FixtureL3Upgrade(t, prev, librarytest.FixedNow)
	if v, _ := prev.UserVersion(ctx); v != agentsAtlasMigration-1 {
		t.Fatalf("version %d", v)
	}
	for _, tb := range l3Tables {
		if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, tb); n != "0" {
			t.Errorf("schema 10 already has %s", tb)
		}
	}
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM pragma_table_info('song_sounds') WHERE name = 'ord'`); n != "0" {
		t.Error("schema 10 already has song_sounds.ord")
	}
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'pd_analyses_feat'`); n != "0" {
		t.Error("schema 10 already has pd_analyses_feat")
	}
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM records`); n != "2" {
		t.Errorf("records = %s: the L2 world is part of the upgrade fixture", n)
	}
	if len(info.Songs) != 7+14 || len(info.Takes) != 8+9 || len(info.Drafts3) != 5 {
		t.Errorf("songs %d takes %d drafts %d", len(info.Songs), len(info.Takes), len(info.Drafts3))
	}
}

// TestMigration0011_UpgradeFromL2 opens a schema-10 library holding the L2 and L3 worlds, lets store.Open apply 0011 and
// checks that every earlier row is unchanged, the L3 tables are empty and the queue holds one track row per take and one
// draft row per draft.
func TestMigration0011_UpgradeFromL2(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, p := openAt(t, agentsAtlasMigration-1)
	librarytest.FixtureL3Upgrade(t, prev, librarytest.FixedNow)
	snap := func(db interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}) []string {
		var out []string
		for _, q := range []string{
			`SELECT group_concat(id||recipe_id||title||status||master_sha256||created_at) FROM (SELECT * FROM tracks ORDER BY id)`,
			`SELECT group_concat(id||engine||request_id||request_json||created_by) FROM (SELECT * FROM recipes ORDER BY id)`,
			`SELECT group_concat(id||COALESCE(root_track_id,'')||COALESCE(muse_id,'')||COALESCE(angle,'')||title||created_at) FROM (SELECT * FROM songs ORDER BY id)`,
			`SELECT group_concat(song_id||take_id||source||last_event_id||evidence_json) FROM (SELECT * FROM song_picks ORDER BY song_id)`,
			`SELECT group_concat(id||song_id||seq||reason||evidence_json) FROM (SELECT * FROM pick_events ORDER BY id)`,
			`SELECT group_concat(id||title||ordinal) FROM (SELECT * FROM muses ORDER BY id)`,
			`SELECT group_concat(track_id||source||stars) FROM (SELECT * FROM ratings ORDER BY track_id, source)`,
			`SELECT group_concat(track_id||analyzer||version||metrics_json) FROM (SELECT * FROM analyses ORDER BY track_id, analyzer, version)`,
			`SELECT group_concat(id||origin||status||request_json) FROM (SELECT * FROM recipe_drafts ORDER BY id)`,
			`SELECT group_concat(id||status||order_json||revision) FROM (SELECT * FROM records ORDER BY id)`,
			`SELECT group_concat(id||seq||kind) FROM (SELECT * FROM record_changes ORDER BY id)`,
			`SELECT group_concat(id||name||kind) FROM (SELECT * FROM crates ORDER BY id)`,
			`SELECT group_concat(key||value) FROM (SELECT * FROM settings ORDER BY key)`,
		} {
			out = append(out, scalar(t, db, q))
		}
		return out
	}
	before := snap(prev.R)
	for i, b := range before {
		if b == "" && i != len(before)-1 {
			t.Fatalf("snapshot %d empty: the fixture is too thin", i)
		}
	}
	tracks, drafts := scalar(t, prev.R, `SELECT COUNT(*) FROM tracks`), scalar(t, prev.R, `SELECT COUNT(*) FROM recipe_drafts`)
	// the triggers of schema 10 already marked the new rows; clear the queue so the migration's re-seed is the only writer
	if _, err := prev.W.ExecContext(ctx, `DELETE FROM provenance_dirty`); err != nil {
		t.Fatal(err)
	}
	prev.W.Close()

	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < agentsAtlasMigration {
		t.Fatalf("version=%d, want %d", v, store.MigrationCount())
	}
	after := snap(s.R)
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("row set %d changed by the upgrade:\n%s\n%s", i, before[i], after[i])
		}
	}
	for _, tb := range l3Tables {
		if n := scalar(t, s.R, `SELECT COUNT(*) FROM `+tb); n != "0" {
			t.Errorf("%s has %s rows after the upgrade", tb, n)
		}
	}
	if got := scalar(t, s.R, `SELECT COUNT(*) FROM provenance_dirty WHERE kind = 'track'`); got != tracks {
		t.Errorf("queue holds %s track rows, want %s (one per take)", got, tracks)
	}
	if got := scalar(t, s.R, `SELECT COUNT(*) FROM provenance_dirty WHERE kind = 'draft'`); got != drafts {
		t.Errorf("queue holds %s draft rows, want %s (one per draft)", got, drafts)
	}
	if got := scalar(t, s.R, `SELECT COUNT(*) FROM provenance_dirty WHERE kind NOT IN ('track','draft')`); got != "0" {
		t.Errorf("%s other queue rows", got)
	}
	if got := scalar(t, s.R, `SELECT COUNT(*) FROM provenance_dirty WHERE tries <> 0`); got != "0" {
		t.Errorf("%s queue rows with tries", got)
	}
	// song_sounds gained ord; nothing was derived yet
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_table_info('song_sounds') WHERE name = 'ord'`); n != "1" {
		t.Error("song_sounds.ord missing after the upgrade")
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
		t.Errorf("foreign_key_check rows = %s", n)
	}
	// the new tables are live on the upgraded database
	if _, err := s.W.ExecContext(ctx, `INSERT INTO taxonomy_proposals(id,state,base_version,rationale,mappings_json,mappings_digest,impact_json,principal_id,principal_kind,principal_name,created_at)
		VALUES('p-up','open',1,'why','[{"term":"x","facet":"mood","value":"chill","op":"add"}]',?,'{}','agent:a','agent','a',?)`, librarytest.Sha("m"), store.FormatTime(librarytest.FixedNow)); err != nil {
		t.Fatal(err)
	}
}

// TestMigration0011_FromSchema5: 0006 to 0011 apply together in one Open on a schema-5 database.
func TestMigration0011_FromSchema5(t *testing.T) {
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
	if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < agentsAtlasMigration {
		t.Fatalf("version=%d", v)
	}
	for _, tb := range append([]string{"songs", "muses", "records", "song_sounds"}, l3Tables...) {
		if n := scalar(t, s.R, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, tb); n != "1" {
			t.Errorf("table %s missing after 5 -> head", tb)
		}
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
		t.Errorf("foreign_key_check rows = %s", n)
	}
}
