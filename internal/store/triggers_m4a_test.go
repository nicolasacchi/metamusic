package store

import "testing"

const (
	m4aTS      = "2026-10-15T10:00:00.000Z"
	m4aGrantQ  = `INSERT INTO connector_grants(id,client_id,subject,username,label,scopes,created_at,updated_at,updated_by) VALUES(?,?,?,?,?,?,?,?,'system:oauth')`
	m4aClientQ = `INSERT INTO subsonic_clients(id,name,username,scopes,default_format,api_key_prefix,api_key_hmac,created_by,created_at) VALUES(?,?,?,?,'raw','abcdefgh',?,'human:alice',?)`
	m4aPlayQ   = `INSERT INTO playlists(id,name,description,items_json,items_count,revision,created_by,updated_by,created_at,updated_at,hidden_at,role) VALUES(?,?,'','[]',0,1,'a','a',?,?,NULL,?)`
)

func m4aGrant(t *testing.T, s *Store, id, scopes string) error {
	t.Helper()
	return exec(t, s, m4aGrantQ, id, "client-a", "sub-"+id, "alice", "Claude", scopes, m4aTS, m4aTS)
}

func TestTriggers_M4a(t *testing.T) {
	t.Parallel()
	s, _ := open(t)

	// connector_grants
	mustExec(t, s, m4aGrantQ, "g1", "client-a", "sub-1", "alice", "Claude", "read,write", m4aTS, m4aTS)
	expectErr(t, exec(t, s, `DELETE FROM connector_grants`), "connector_grants: no physical delete")
	expectErr(t, exec(t, s, `UPDATE connector_grants SET subject='other'`), "connector_grants: frozen column")
	expectErr(t, exec(t, s, `UPDATE connector_grants SET client_id='other'`), "connector_grants: frozen column")
	expectErr(t, m4aGrant(t, s, "g2", "read,admin"), "CHECK")
	expectErr(t, m4aGrant(t, s, "g3", "write"), "CHECK")
	expectErr(t, exec(t, s, m4aGrantQ, "g4", "client-a", "sub-1", "alice", "Claude", "read", m4aTS, m4aTS), "UNIQUE")
	mustExec(t, s, `UPDATE connector_grants SET scopes='read' WHERE id='g1'`)
	mustExec(t, s, `UPDATE connector_grants SET scopes='read,write,generate', daily_budget_micro_usd=5, revoked_at=?, revoked_by='human:alice' WHERE id='g1'`, m4aTS)
	expectErr(t, exec(t, s, `UPDATE connector_grants SET revoked_by=NULL WHERE id='g1'`), "CHECK")
	expectErr(t, exec(t, s, `UPDATE connector_grants SET auto_approve_takes=101 WHERE id='g1'`), "CHECK")

	// subsonic_clients
	mustExec(t, s, m4aClientQ, "c1", "Amperfy", "amperfy", "read", "h1", m4aTS)
	expectErr(t, exec(t, s, `DELETE FROM subsonic_clients`), "subsonic_clients: no physical delete")
	expectErr(t, exec(t, s, `UPDATE subsonic_clients SET api_key_hmac='h2'`), "subsonic_clients: frozen column")
	expectErr(t, exec(t, s, `UPDATE subsonic_clients SET username='other'`), "subsonic_clients: frozen column")
	expectErr(t, exec(t, s, m4aClientQ, "c2", "Other", "amperfy", "read", "h3", m4aTS), "UNIQUE")
	expectErr(t, exec(t, s, m4aClientQ, "c3", "Gen", "genny", "read,generate", "h4", m4aTS), "CHECK")
	expectErr(t, exec(t, s, m4aClientQ, "c4", "Short", "ab", "read", "h5", m4aTS), "CHECK")
	expectErr(t, exec(t, s, `INSERT INTO subsonic_clients(id,name,username,scopes,default_format,created_by,created_at) VALUES('c5','n','nokey','read','raw','x',?)`, m4aTS), "CHECK")
	mustExec(t, s, `UPDATE subsonic_clients SET revoked_at=?, revoked_by='human:alice' WHERE id='c1'`, m4aTS)
	mustExec(t, s, m4aClientQ, "c6", "Reuse", "amperfy", "read", "h6", m4aTS) // the name is free after revoke
	mustExec(t, s, `UPDATE subsonic_clients SET last_seen_at=?, last_client='x' WHERE id='c6'`, m4aTS)

	// subsonic_salts is a cache: physical DELETE allowed
	mustExec(t, s, `INSERT INTO subsonic_salts(client_id,salt_sha256,first_seen_at) VALUES('c6','aa',?)`, m4aTS)
	mustExec(t, s, `DELETE FROM subsonic_salts`)

	// bookmarks and play queue
	insertStub(t, s, "bk")
	mustExec(t, s, `INSERT INTO subsonic_play_queue(id,track_ids_json,position_ms,changed_at,changed_by) VALUES(1,'[]',0,?,'amperfy')`, m4aTS)
	expectErr(t, exec(t, s, `DELETE FROM subsonic_play_queue`), "subsonic_play_queue: no physical delete")
	expectErr(t, exec(t, s, `INSERT INTO subsonic_play_queue(id,track_ids_json,position_ms,changed_at,changed_by) VALUES(2,'[]',0,?,'x')`, m4aTS), "CHECK")
	expectErr(t, exec(t, s, `UPDATE subsonic_play_queue SET track_ids_json='{}'`), "CHECK")
	mustExec(t, s, `UPDATE subsonic_play_queue SET position_ms=5000`)
	// a bookmark needs a track: borrow the stub recipe via a lossy track
	lr := baseLossy()
	lr.id, lr.root, lr.recipe = "bt1", "bt1", "r-bk"
	if err := lr.insert(s, t); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO subsonic_bookmarks(track_id,position_ms,created_at,updated_at,updated_by) VALUES('bt1',10,?,?,'amperfy')`, m4aTS, m4aTS)
	expectErr(t, exec(t, s, `DELETE FROM subsonic_bookmarks`), "subsonic_bookmarks: no physical delete")
	expectErr(t, exec(t, s, `UPDATE subsonic_bookmarks SET track_id='x'`), "subsonic_bookmarks: frozen column")
	expectErr(t, exec(t, s, `UPDATE subsonic_bookmarks SET position_ms=-1`), "CHECK")
	mustExec(t, s, `UPDATE subsonic_bookmarks SET position_ms=20, deleted_at=?`, m4aTS)

	// playlists.role
	expectErr(t, exec(t, s, `UPDATE playlists SET role=NULL`), "playlists: frozen column")
	expectErr(t, exec(t, s, m4aPlayQ, "p-second", "Second offline", m4aTS, m4aTS, "offline"), "UNIQUE")
	mustExec(t, s, m4aPlayQ, "p-plain", "Plain", m4aTS, m4aTS, nil)
	mustExec(t, s, m4aPlayQ, "p-plain2", "Plain 2", m4aTS, m4aTS, nil) // NULL roles never collide
	expectErr(t, exec(t, s, `DELETE FROM playlists WHERE id='00000000-0000-7000-8000-0000000000f1'`), "playlists: no physical delete")
}

// TestWriteSQL_M4aRunTwice runs every C103 write form twice: the second run must succeed and change nothing
// it should not (idempotent upserts, guarded updates).
func TestWriteSQL_M4aRunTwice(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	count := func(q string, args ...any) (n int) {
		t.Helper()
		if err := s.R.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	twice := func(q string, args ...any) (first, second int64) {
		t.Helper()
		for i, dst := range []*int64{&first, &second} {
			res, err := s.W.Exec(q, args...)
			if err != nil {
				t.Fatalf("run %d of %q: %v", i+1, q, err)
			}
			*dst, _ = res.RowsAffected()
		}
		return
	}

	// grant upsert
	const ensure = `INSERT INTO connector_grants(id, client_id, subject, username, label, scopes, created_at, updated_at, updated_by)
		VALUES(?,?,?,?,?,'read,write',?,?,'system:oauth') ON CONFLICT(client_id, subject) DO NOTHING RETURNING id`
	for i := 0; i < 2; i++ {
		rows, err := s.W.Query(ensure, "g-"+itoa(i), "client-a", "sub-1", "alice", "Claude", m4aTS, m4aTS)
		if err != nil {
			t.Fatal(err)
		}
		got := rows.Next()
		_ = rows.Close()
		if got != (i == 0) {
			t.Fatalf("ensure run %d returned a row = %v", i+1, got)
		}
	}
	if n := count(`SELECT count(*) FROM connector_grants`); n != 1 {
		t.Fatalf("grants = %d", n)
	}
	// grant touch (at most once a minute) and set
	if a, b := twice(`UPDATE connector_grants SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR last_used_at < ?)`, m4aTS, "g-0", "2026-10-15T09:59:00.000Z"); a != 1 || b != 0 {
		t.Fatalf("touch rows %d %d", a, b)
	}
	if a, b := twice(`UPDATE connector_grants SET scopes=?, daily_budget_micro_usd=?, auto_approve_takes=?, auto_approve_micro_usd=?, revoked_at=?, revoked_by=?, updated_at=?, updated_by=? WHERE id=?`,
		"read,write,generate", 1000, 2, 500, nil, nil, m4aTS, "human:alice", "g-0"); a != 1 || b != 1 {
		t.Fatalf("set rows %d %d", a, b)
	}

	// subsonic client revoke and touch
	mustExec(t, s, m4aClientQ, "c1", "Amperfy", "amperfy", "read", "h1", m4aTS)
	if a, b := twice(`UPDATE subsonic_clients SET revoked_at=?, revoked_by=? WHERE id=? AND revoked_at IS NULL`, m4aTS, "human:alice", "c1"); a != 1 || b != 0 {
		t.Fatalf("revoke rows %d %d", a, b)
	}
	mustExec(t, s, m4aClientQ, "c2", "Tempus", "tempus", "read", "h2", m4aTS)
	if a, b := twice(`UPDATE subsonic_clients SET last_seen_at=?, last_client=? WHERE id=? AND (last_seen_at IS NULL OR last_seen_at < ? OR last_client IS NOT ?)`,
		m4aTS, "Tempus", "c2", "2026-10-15T09:59:00.000Z", "Tempus"); a != 1 || b != 0 {
		t.Fatalf("client touch rows %d %d", a, b)
	}

	// salt sighting and prune
	if a, b := twice(`INSERT INTO subsonic_salts(client_id, salt_sha256, first_seen_at) VALUES(?,?,?) ON CONFLICT DO NOTHING`, "c2", "aa", m4aTS); a != 1 || b != 0 {
		t.Fatalf("salt rows %d %d", a, b)
	}
	if a, b := twice(`DELETE FROM subsonic_salts WHERE first_seen_at < ?`, "2026-10-15T11:00:00.000Z"); a != 1 || b != 0 {
		t.Fatalf("prune rows %d %d", a, b)
	}

	// bookmark upsert and delete (soft)
	insertStub(t, s, "w")
	lr := baseLossy()
	lr.id, lr.root, lr.recipe = "wt1", "wt1", "r-w"
	if err := lr.insert(s, t); err != nil {
		t.Fatal(err)
	}
	const bm = `INSERT INTO subsonic_bookmarks(track_id, position_ms, comment, comment_id, created_at, updated_at, updated_by, deleted_at)
		VALUES(?,?,?,?,?,?,?,NULL) ON CONFLICT(track_id) DO UPDATE SET position_ms=excluded.position_ms, comment=excluded.comment,
		comment_id=COALESCE(excluded.comment_id, subsonic_bookmarks.comment_id), updated_at=excluded.updated_at, updated_by=excluded.updated_by, deleted_at=NULL`
	twice(bm, "wt1", 1000, "note", nil, m4aTS, m4aTS, "amperfy")
	if n := count(`SELECT count(*) FROM subsonic_bookmarks`); n != 1 {
		t.Fatalf("bookmarks = %d", n)
	}
	if a, b := twice(`UPDATE subsonic_bookmarks SET deleted_at=?, updated_at=?, updated_by=? WHERE track_id=? AND deleted_at IS NULL`, m4aTS, m4aTS, "amperfy", "wt1"); a != 1 || b != 0 {
		t.Fatalf("bookmark delete rows %d %d", a, b)
	}
	twice(bm, "wt1", 2000, "note", nil, m4aTS, m4aTS, "amperfy") // resurrects the soft-deleted bookmark
	if n := count(`SELECT count(*) FROM subsonic_bookmarks WHERE deleted_at IS NULL AND position_ms = 2000`); n != 1 {
		t.Fatalf("bookmark resurrect: %d", n)
	}

	// play queue
	const pq = `INSERT INTO subsonic_play_queue(id, track_ids_json, current_track_id, position_ms, changed_at, changed_by) VALUES(1,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET track_ids_json=excluded.track_ids_json, current_track_id=excluded.current_track_id,
		position_ms=excluded.position_ms, changed_at=excluded.changed_at, changed_by=excluded.changed_by`
	twice(pq, `["wt1"]`, "wt1", 5, m4aTS, "amperfy")
	twice(pq, `[]`, nil, 0, m4aTS, "tempus")
	if n := count(`SELECT count(*) FROM subsonic_play_queue WHERE changed_by='tempus'`); n != 1 {
		t.Fatalf("play queue rows: %d", n)
	}
}
