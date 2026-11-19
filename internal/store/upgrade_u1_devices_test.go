package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// deviceOwnerMigration is the position of 0019_device_owner.sql (U1-D1, C301.11).
const deviceOwnerMigration = 19

// TestMigration_U1Devices upgrades a schema-18 database holding a device, a Subsonic client and a pairing code through 0019
// and checks C301.11 and C314.3: every existing row becomes the Owner's (owner_key 'owner'), nothing else in the rows
// changes, the column is NOT NULL and non-empty, the owner key is frozen, and user_version advances.
func TestMigration_U1Devices(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	prev, p := openAt(t, deviceOwnerMigration-1)
	for _, q := range []string{
		`INSERT INTO devices(id,name,prefix,hmac_hash,scopes,user_agent,paired_by,created_at,expires_at)
		 VALUES('d1','Phone','abcd1234','h1','read,write','ua','human:alice','2026-10-01T10:00:00Z','2027-01-01T10:00:00Z')`,
		`INSERT INTO pairing_codes(id,hmac_hash,name,created_by,created_at,expires_at)
		 VALUES('pc1','ph1','Phone','human:alice','2026-10-01T10:00:00Z','2026-10-01T10:05:00Z')`,
		`INSERT INTO subsonic_clients(id,name,username,scopes,default_format,api_key_prefix,api_key_hmac,created_by,created_at)
		 VALUES('s1','Desk','desk.app','read,write','raw','abcd1234','k1','human:alice','2026-10-01T10:00:00Z')`,
	} {
		if _, err := prev.W.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if n := scalar(t, prev.R, `SELECT COUNT(*) FROM pragma_table_info('devices') WHERE name = 'owner_key'`); n != "0" {
		t.Fatalf("schema 18 already has devices.owner_key")
	}
	before := map[string]string{}
	for tb, q := range map[string]string{
		"devices":          `SELECT id||name||prefix||hmac_hash||scopes||paired_by||created_at FROM devices`,
		"pairing_codes":    `SELECT id||hmac_hash||name||created_by FROM pairing_codes`,
		"subsonic_clients": `SELECT id||name||username||scopes||created_by FROM subsonic_clients`,
	} {
		before[tb] = scalar(t, prev.R, q)
	}
	prev.W.Close()
	s, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.UserVersion(ctx); v != store.MigrationCount() || v < deviceOwnerMigration {
		t.Fatalf("version = %d, migrations = %d", v, store.MigrationCount())
	}
	for tb, q := range map[string]string{
		"devices":          `SELECT id||name||prefix||hmac_hash||scopes||paired_by||created_at FROM devices`,
		"pairing_codes":    `SELECT id||hmac_hash||name||created_by FROM pairing_codes`,
		"subsonic_clients": `SELECT id||name||username||scopes||created_by FROM subsonic_clients`,
	} {
		if got := scalar(t, s.R, q); got != before[tb] {
			t.Errorf("%s changed by the upgrade: %q -> %q", tb, before[tb], got)
		}
		if got := scalar(t, s.R, `SELECT group_concat(owner_key) FROM `+tb); got != "owner" {
			t.Errorf("%s owner_key = %q, want owner", tb, got)
		}
		if got := scalar(t, s.R, `SELECT "notnull" FROM pragma_table_info('`+tb+`') WHERE name = 'owner_key'`); got != "1" {
			t.Errorf("%s.owner_key is nullable", tb)
		}
		if _, err := s.W.ExecContext(ctx, `UPDATE `+tb+` SET owner_key = 'bob'`); err == nil || !strings.Contains(err.Error(), "frozen") {
			t.Errorf("%s.owner_key is not frozen: %v", tb, err)
		}
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO devices(id,name,prefix,hmac_hash,scopes,user_agent,paired_by,created_at,expires_at,owner_key)
		VALUES('d2','Phone','abcd1234','h2','read,write','ua','human:bob','2026-10-01T10:00:00Z','2027-01-01T10:00:00Z','')`); err == nil {
		t.Error("an empty owner_key was accepted")
	}
	if n := scalar(t, s.R, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != "0" {
		t.Errorf("foreign_key_check rows = %s", n)
	}
}
