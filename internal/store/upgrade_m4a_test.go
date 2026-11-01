package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// remoteMigration mirrors the constant of the internal schema_m4a_test.go: the position of the M4a
// migration (9: 0009_remote.sql, after L1's 0008_provenance.sql).
const remoteMigration = 9

// TestMigration0009_UpgradeFromM3 opens a library one migration below the M4a one with the M3 fixture rows,
// lets store.Open apply the migration and checks that every earlier row is unchanged, every track's
// status_set_at equals its created_at, a PAT still verifies and the offline set exists.
func TestMigration0009_UpgradeFromM3(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "mm.db")
	raw, err := store.OpenRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateTo(ctx, raw, remoteMigration-1); err != nil {
		t.Fatal(err)
	}
	prev := &store.Store{W: raw, R: raw}
	key := []byte("0123456789abcdef0123456789abcdef")
	plain, err := legacyToken(ctx, prev, key)
	if err != nil {
		t.Fatal(err)
	}
	librarytest.FixtureM3(t, prev, librarytest.FixedNow)
	if v, _ := prev.UserVersion(ctx); v != remoteMigration-1 {
		t.Fatalf("setup version=%d", v)
	}
	var has int
	if err := prev.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('tracks') WHERE name='status_set_at'`).Scan(&has); err != nil || has != 0 {
		t.Fatalf("previous schema already has status_set_at: %d %v", has, err)
	}
	snap := func(s *store.Store) []string {
		var out []string
		for _, q := range []string{
			`SELECT group_concat(id||recipe_id||title||status||master_sha256||created_at) FROM (SELECT * FROM tracks ORDER BY id)`,
			`SELECT group_concat(id||engine||request_id||request_json||created_by) FROM (SELECT * FROM recipes ORDER BY id)`,
			`SELECT group_concat(id||hmac_hash||scopes) FROM api_tokens`,
			`SELECT group_concat(id||hmac_hash||scopes||name) FROM devices`,
			`SELECT group_concat(track_id||source||stars||principal_id) FROM ratings`,
			`SELECT group_concat(id||track_id||body) FROM comments`,
			`SELECT group_concat(id||state||ceiling_micro_usd) FROM (SELECT * FROM batches ORDER BY id)`,
			`SELECT group_concat(id||state) FROM (SELECT * FROM jobs ORDER BY id)`,
			`SELECT group_concat(id||monthly_micro_usd) FROM spend_caps`,
		} {
			var v *string
			if err := s.R.QueryRowContext(ctx, q).Scan(&v); err != nil {
				t.Fatal(err)
			}
			if v == nil {
				out = append(out, "")
			} else {
				out = append(out, *v)
			}
		}
		return out
	}
	// playlists are compared separately: the migration adds one row
	var oldPlaylists string
	if err := prev.R.QueryRowContext(ctx, `SELECT group_concat(id||name||items_json||revision) FROM (SELECT * FROM playlists ORDER BY id)`).Scan(&oldPlaylists); err != nil {
		t.Fatal(err)
	}
	before := snap(prev)
	for i, b := range before {
		if b == "" {
			t.Fatalf("snapshot %d empty: fixture too thin (%q)", i, b)
		}
	}
	var tracksBefore int
	if err := prev.R.QueryRowContext(ctx, `SELECT count(*) FROM tracks`).Scan(&tracksBefore); err != nil || tracksBefore == 0 {
		t.Fatalf("tracks before: %d %v", tracksBefore, err)
	}
	raw.Close()

	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < remoteMigration {
		t.Fatalf("version=%d, want %d", v, store.MigrationCount())
	}
	after := snap(s)
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("row set %d changed by upgrade: %q -> %q", i, before[i], after[i])
		}
	}
	var newPlaylists string
	if err := s.R.QueryRowContext(ctx, `SELECT group_concat(id||name||items_json||revision) FROM (SELECT * FROM playlists WHERE role IS NULL ORDER BY id)`).Scan(&newPlaylists); err != nil || newPlaylists != oldPlaylists {
		t.Fatalf("existing playlists changed: %q -> %q (%v)", oldPlaylists, newPlaylists, err)
	}
	var bad, total int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(status_set_at IS NOT created_at),0) FROM tracks`).Scan(&total, &bad); err != nil || total != tracksBefore || bad != 0 {
		t.Fatalf("status_set_at: %d tracks (was %d), %d differ from created_at, err %v", total, tracksBefore, bad, err)
	}
	if _, err := auth.NewTokenStore(s, key, time.Now).Verify(ctx, plain); err != nil {
		t.Fatalf("token no longer verifies: %v", err)
	}
	var off int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM playlists WHERE role='offline' AND id='00000000-0000-7000-8000-0000000000f1'`).Scan(&off); err != nil || off != 1 {
		t.Fatalf("offline set rows = %d %v", off, err)
	}
	for _, tbl := range []string{"connector_grants", "subsonic_clients", "subsonic_salts", "subsonic_bookmarks", "subsonic_play_queue"} {
		var n int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM `+tbl).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s rows = %d %v", tbl, n, err)
		}
	}
}
