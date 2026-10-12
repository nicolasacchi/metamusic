package store

import (
	"context"
	"strings"
	"testing"
)

// m2aTrack inserts one recipe and track "t1" for the M2a table tests.
func m2aTrack(t *testing.T, s *Store) {
	t.Helper()
	r := insertRecipe(t, s, "m2a", 1)
	insertTrack(t, s, "t1", r, "T", "f", 100)
}

func TestMigration0003_Applies(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	if v, err := s.UserVersion(ctx); err != nil || v < 3 {
		t.Fatalf("version=%d err=%v", v, err)
	}
	want := map[string][]string{
		"table": {"ratings", "tags", "track_tags", "comments", "playlists", "listen_events", "exports", "audit_log",
			"idempotency", "devices", "pairing_codes", "backup_runs"},
		"index": {"ratings_source_stars", "track_tags_active", "comments_track", "comments_open", "listen_events_track",
			"exports_sha", "audit_log_at", "audit_log_principal", "audit_log_op", "idempotency_expires",
			"backup_runs_started", "backup_runs_one_running"},
		"trigger": {"recipe_drafts_created_by_frozen", "recipe_drafts_fulfilled_frozen",
			"ratings_no_delete", "ratings_frozen", "tags_immutable", "tags_no_delete", "track_tags_no_delete", "track_tags_frozen",
			"comments_no_delete", "comments_frozen", "playlists_no_delete", "playlists_frozen", "listen_events_immutable",
			"listen_events_no_delete", "exports_immutable", "exports_no_delete", "audit_log_immutable", "audit_log_no_delete",
			"devices_no_delete", "devices_frozen", "pairing_codes_no_delete", "pairing_codes_frozen", "pairing_codes_single_use",
			"backup_runs_no_delete", "backup_runs_final", "backup_runs_frozen"},
	}
	for typ, names := range want {
		for _, n := range names {
			var c int
			if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type=? AND name=?`, typ, n).Scan(&c); err != nil || c != 1 {
				t.Errorf("%s %s missing", typ, n)
			}
		}
	}
	for _, col := range []string{"revision", "created_by", "updated_by"} {
		var c int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('recipe_drafts') WHERE name=?`, col).Scan(&c); err != nil || c != 1 {
			t.Errorf("recipe_drafts.%s missing", col)
		}
	}
}

func TestMigration0003_RatingSourceCheck(t *testing.T) {
	s, _ := open(t)
	m2aTrack(t, s)
	const q = `INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES('t1',?,3,?,'p','n',?,?)`
	expectErr(t, exec(t, s, q, "human", "agent", ts, ts), "CHECK")
	expectErr(t, exec(t, s, q, "agent", "device", ts, ts), "CHECK")
	expectErr(t, exec(t, s, q, "agent", "human", ts, ts), "CHECK")
	mustExec(t, s, q, "human", "device", ts, ts)
	mustExec(t, s, q, "agent", "agent", ts, ts)
	expectErr(t, exec(t, s, `UPDATE ratings SET stars=6 WHERE source='agent'`), "CHECK")
	mustExec(t, s, `UPDATE ratings SET stars=NULL WHERE source='agent'`)
}

func TestMigration0003_CommentSectionSourceNeverLyrics(t *testing.T) {
	s, _ := open(t)
	m2aTrack(t, s)
	const q = `INSERT INTO comments(id,track_id,body,at_second,section_label,section_source,kind,state,source,principal_kind,principal_id,principal_name,created_at,updated_at)
		VALUES(?,'t1','b',?,?,?,'note','open','web','human','h','h',?,?)`
	expectErr(t, exec(t, s, q, "c1", 5.0, "chorus 1", "lyrics", ts, ts), "CHECK")
	expectErr(t, exec(t, s, q, "c2", nil, "chorus 1", "plan", ts, ts), "CHECK") // label without at_second
	expectErr(t, exec(t, s, q, "c3", 5.0, "chorus 1", nil, ts, ts), "CHECK")    // label without source
	expectErr(t, exec(t, s, q, "c4", 5.0, nil, "plan", ts, ts), "CHECK")        // source without label
	mustExec(t, s, q, "c5", 5.0, "chorus 1", "manual", ts, ts)
	mustExec(t, s, q, "c6", nil, nil, nil, ts, ts)
}

func TestMigration0003_DeviceScopes(t *testing.T) {
	s, _ := open(t)
	const q = `INSERT INTO devices(id,name,prefix,hmac_hash,scopes,user_agent,paired_by,created_at,expires_at) VALUES(?,'phone','abcd1234',?,?,'ua','human:h',?,?)`
	expectErr(t, exec(t, s, q, "d1", "h1", "read,write,generate", ts, ts), "CHECK")
	expectErr(t, exec(t, s, q, "d2", "h2", "read", ts, ts), "CHECK")
	mustExec(t, s, q, "d3", "h3", "read,write", ts, ts)
	expectErr(t, exec(t, s, `INSERT INTO devices(id,name,prefix,hmac_hash,scopes,user_agent,paired_by,created_at,expires_at) VALUES('d4','p','x','h4','read,write',?,'h',?,?)`,
		strings.Repeat("u", 513), ts, ts), "CHECK")
}

func TestMigration0003_PlaylistCount(t *testing.T) {
	s, _ := open(t)
	const q = `INSERT INTO playlists(id,name,items_json,items_count,created_by,updated_by,created_at,updated_at) VALUES(?,'pl',?,?,'h','h',?,?)`
	expectErr(t, exec(t, s, q, "p1", `["a","b"]`, 1, ts, ts), "CHECK")
	expectErr(t, exec(t, s, q, "p2", `{"a":1}`, 1, ts, ts), "CHECK")
	expectErr(t, exec(t, s, q, "p3", `not json`, 0, ts, ts), "CHECK")
	mustExec(t, s, q, "p4", `["a","b"]`, 2, ts, ts)
	expectErr(t, exec(t, s, `UPDATE playlists SET items_json='[]' WHERE id='p4'`), "CHECK")
	mustExec(t, s, `UPDATE playlists SET items_json='[]', items_count=0, revision=revision+1 WHERE id='p4'`)
}

func TestMigration0003_AuditAppendOnly(t *testing.T) {
	s, _ := open(t)
	mustExec(t, s, `INSERT INTO audit_log(id,at,principal_kind,principal_id,principal_name,transport,op,method,args_sha256,status,idempotency,duration_ms)
		VALUES('a1',?,'agent','agent:x','x','rest','list_tracks','GET','h',200,'none',3)`, ts)
	expectErr(t, exec(t, s, `UPDATE audit_log SET status=500`), "append-only")
	expectErr(t, exec(t, s, `DELETE FROM audit_log`), "append-only")
	expectErr(t, exec(t, s, `INSERT INTO audit_log(id,at,principal_kind,principal_id,principal_name,transport,op,method,args_sha256,status,idempotency,duration_ms)
		VALUES('a2',?,'agent','agent:x','x','rest','op','GET','h',200,'none',-1)`, ts), "CHECK")
}

func TestMigration0003_DraftColumnDefaults(t *testing.T) {
	s, _ := open(t)
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,request_id,request_json,created_at,updated_at) VALUES('d1','import_backlog','dr','{}',?,?)`, ts, ts)
	var rev int
	var cb, ub string
	if err := s.R.QueryRowContext(context.Background(), `SELECT revision,created_by,updated_by FROM recipe_drafts WHERE id='d1'`).Scan(&rev, &cb, &ub); err != nil {
		t.Fatal(err)
	}
	if rev != 1 || cb != "import" || ub != "import" {
		t.Fatalf("defaults = %d %q %q", rev, cb, ub)
	}
	expectErr(t, exec(t, s, `UPDATE recipe_drafts SET revision=0`), "CHECK")
}
