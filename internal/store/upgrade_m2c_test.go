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

func TestMigration0005_UpgradeFromM2b(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "mm.db")
	raw, err := store.OpenRaw(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateTo(ctx, raw, 4); err != nil {
		t.Fatal(err)
	}
	v4 := &store.Store{W: raw, R: raw}
	key := []byte("0123456789abcdef0123456789abcdef")
	plain, _, err := auth.NewTokenStore(v4, key, time.Now).Create(ctx, "t", []auth.Scope{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	librarytest.Fixture(t, v4, librarytest.FixedNow)
	// an M2b row: one blind duel between two fixture tracks
	if _, err := raw.ExecContext(ctx, `INSERT INTO duels(id,slot_a_track_id,slot_b_track_id,swapped,blind,from_ms,to_ms,clip_def,tool_version,match,gain_a_cdb,gain_b_cdb,created_by,created_by_kind,created_at,expires_at)
		VALUES('duel-1',?,?,0,1,0,1000,'mm-clip/1','ffmpeg x','off',0,0,'human:h','human','2026-01-01T00:00:00Z','2026-01-02T00:00:00Z')`,
		librarytest.TrackLuna, librarytest.TrackCitta); err != nil {
		t.Fatal(err)
	}
	if v, _ := v4.UserVersion(ctx); v != 4 {
		t.Fatalf("setup version=%d", v)
	}
	snap := func(s *store.Store) []string {
		var out []string
		for _, q := range []string{
			`SELECT group_concat(id||recipe_id||title||status||master_sha256) FROM (SELECT * FROM tracks ORDER BY id)`,
			`SELECT group_concat(id||request_id||request_json||status||created_at||updated_at) FROM (SELECT * FROM recipe_drafts ORDER BY id)`,
			`SELECT group_concat(id||hmac_hash||scopes) FROM api_tokens`,
			`SELECT group_concat(id||hmac_hash||scopes||name) FROM devices`,
			`SELECT group_concat(track_id||source||stars||principal_id) FROM ratings`,
			`SELECT group_concat(id||track_id||body) FROM comments`,
			`SELECT group_concat(id||slot_a_track_id||slot_b_track_id||created_at) FROM duels`,
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
	before := snap(v4)
	var devices int
	if err := raw.QueryRowContext(ctx, `SELECT count(*) FROM devices`).Scan(&devices); err != nil || devices == 0 {
		t.Fatalf("fixture device missing: %d %v", devices, err)
	}
	raw.Close()

	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != 5 {
		t.Fatalf("version=%d", v)
	}
	after := snap(s)
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("row set %d changed by upgrade: %q -> %q", i, before[i], after[i])
		}
	}
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM duels`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duels=%d err=%v", n, err)
	}
	var tracks, lossy int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(lossy),0) FROM tracks`).Scan(&tracks, &lossy); err != nil || tracks == 0 || lossy != 0 {
		t.Fatalf("tracks=%d lossy=%d err=%v", tracks, lossy, err)
	}
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM tracks WHERE lossy <> 0 OR import_rule = 'reference'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("reference rows after upgrade: %d %v", n, err)
	}
	if _, err := auth.NewTokenStore(s, key, time.Now).Verify(ctx, plain); err != nil {
		t.Fatalf("token no longer verifies: %v", err)
	}
}
