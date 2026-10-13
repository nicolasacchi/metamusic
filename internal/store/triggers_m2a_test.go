package store

import "testing"

func TestTriggers_M2a(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m2aTrack(t, s)
	mustExec(t, s, `INSERT INTO ratings(track_id,source,stars,principal_kind,principal_id,principal_name,created_at,updated_at) VALUES('t1','human',4,'human','human:h','h',?,?)`, ts, ts)
	mustExec(t, s, `INSERT INTO tags(name,created_at) VALUES('warm',?)`, ts)
	mustExec(t, s, `INSERT INTO track_tags(track_id,tag_id,source,principal_id,created_at,updated_at) VALUES('t1',1,'human','human:h',?,?)`, ts, ts)
	mustExec(t, s, `INSERT INTO comments(id,track_id,body,kind,state,source,principal_kind,principal_id,principal_name,created_at,updated_at)
		VALUES('c1','t1','b','note','open','web','human','human:h','h',?,?)`, ts, ts)
	mustExec(t, s, `INSERT INTO playlists(id,name,created_by,updated_by,created_at,updated_at) VALUES('p1','pl','h','h',?,?)`, ts, ts)
	mustExec(t, s, `INSERT INTO listen_events(id,track_id,session_id,kind,source,principal_kind,principal_id,client_at,received_at)
		VALUES('e1','t1','session-01','start','web','human','human:h',?,?)`, ts, ts)
	mustExec(t, s, `INSERT INTO exports(id,kind,params_json,sha256,bytes,path,filename,summary_json,principal_id,created_at)
		VALUES('x1','songs_jsonl','{}','s',1,'p','f.jsonl','{}','human:h',?)`, ts)
	mustExec(t, s, `INSERT INTO audit_log(id,at,principal_kind,principal_id,principal_name,transport,op,method,args_sha256,status,idempotency,duration_ms)
		VALUES('a1',?,'agent','agent:x','x','rest','op','GET','h',200,'none',1)`, ts)
	mustExec(t, s, `INSERT INTO idempotency(principal_key,key,op,request_sha256,derived,state,created_at,lease_until,expires_at) VALUES('agent:x','k','op','h',0,'pending',?,?,?)`, ts, ts, ts)
	mustExec(t, s, `INSERT INTO devices(id,name,prefix,hmac_hash,scopes,user_agent,paired_by,created_at,expires_at) VALUES('d1','phone','abcd1234','dh','read,write','ua','human:h',?,?)`, ts, ts)
	mustExec(t, s, `INSERT INTO pairing_codes(id,hmac_hash,name,created_by,created_at,expires_at) VALUES('pc1','ph','phone','human:h',?,?)`, ts, ts)
	mustExec(t, s, `INSERT INTO backup_runs(id,cause,principal_id,state,started_at,heartbeat_at) VALUES('b1','cli','system:cli','running',?,?)`, ts, ts)
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,request_id,request_json,created_at,updated_at) VALUES('dbk','import_backlog','dr','{}',?,?)`, ts, ts)
	mustExec(t, s, `INSERT INTO recipe_drafts(id,origin,request_id,request_json,status,fulfilled_by_track_id,created_at,updated_at) VALUES('dfu','import_backlog','dr2','{}','fulfilled','t1',?,?)`, ts, ts)

	// physical deletes: refused everywhere except the idempotency cache table
	for _, tbl := range []string{"ratings", "tags", "track_tags", "comments", "playlists", "listen_events", "exports", "audit_log", "devices", "pairing_codes", "backup_runs"} {
		err := exec(t, s, `DELETE FROM `+tbl)
		if err == nil {
			t.Errorf("DELETE on %s succeeded", tbl)
			continue
		}
		if msg := err.Error(); !contains(msg, "no physical delete") && !contains(msg, "append-only") {
			t.Errorf("DELETE on %s: unexpected error %v", tbl, err)
		}
	}
	mustExec(t, s, `DELETE FROM idempotency`)

	updates := []struct{ name, q, want string }{
		{"audit", `UPDATE audit_log SET status=500`, "append-only"},
		{"listen", `UPDATE listen_events SET to_ms=5`, "immutable"},
		{"tags", `UPDATE tags SET name='x'`, "tags: immutable"},
		{"exports", `UPDATE exports SET bytes=2`, "exports: immutable"},
		{"fulfilled draft request_json", `UPDATE recipe_drafts SET request_json='{"a":1}' WHERE id='dfu'`, "fulfilled drafts are immutable"},
		{"fulfilled draft request_id", `UPDATE recipe_drafts SET request_id='z' WHERE id='dfu'`, "fulfilled drafts are immutable"},
		{"draft created_by", `UPDATE recipe_drafts SET created_by='x' WHERE id='dbk'`, "frozen column"},
		{"rating source", `UPDATE ratings SET source='agent'`, "frozen column"},
		{"rating created_at", `UPDATE ratings SET created_at='2027-01-01T00:00:00Z'`, "frozen column"},
		{"track_tags source", `UPDATE track_tags SET source='agent'`, "frozen column"},
		{"track_tags created_at", `UPDATE track_tags SET created_at='x'`, "frozen column"},
		{"comment body ok but author frozen", `UPDATE comments SET principal_id='agent:x'`, "frozen column"},
		{"comment created_at", `UPDATE comments SET created_at='x'`, "frozen column"},
		{"comment at_second", `UPDATE comments SET at_second=3`, "frozen column"},
		{"playlist created_by", `UPDATE playlists SET created_by='x'`, "frozen column"},
		{"device hash", `UPDATE devices SET hmac_hash='x'`, "frozen column"},
		{"device scopes", `UPDATE devices SET scopes='read,write'`, "frozen column"},
		{"pairing name", `UPDATE pairing_codes SET name='x'`, "frozen column"},
		{"pairing expiry", `UPDATE pairing_codes SET expires_at='x'`, "frozen column"},
		{"backup cause", `UPDATE backup_runs SET cause='api'`, "frozen column"},
		{"second running backup", `INSERT INTO backup_runs(id,cause,principal_id,state,started_at,heartbeat_at) VALUES('b2','api','system:cli','running','` + ts + `','` + ts + `')`, "UNIQUE"},
	}
	for _, u := range updates {
		expectErr(t, exec(t, s, u.q), u.want)
	}

	// allowed updates, each run twice
	for i := 0; i < 2; i++ {
		mustExec(t, s, `UPDATE recipe_drafts SET request_json='{"a":1}', revision=revision+1, updated_by='human:h' WHERE id='dbk'`)
		mustExec(t, s, `UPDATE ratings SET stars=2, updated_at=?`, ts)
		mustExec(t, s, `UPDATE track_tags SET removed_at=?, removed_by='human:h'`, ts)
		mustExec(t, s, `UPDATE comments SET body='edited', state='resolved', edited_at=?, state_changed_at=?, state_changed_by='human:h'`, ts, ts)
		mustExec(t, s, `UPDATE playlists SET name='renamed', revision=revision+1`)
		mustExec(t, s, `UPDATE devices SET last_seen_at=?, expires_at=?, revoked_at=?`, ts, ts, ts)
		mustExec(t, s, `UPDATE backup_runs SET heartbeat_at=? WHERE id='b1'`, ts)
	}

	// pairing code redeems exactly once
	mustExec(t, s, `UPDATE pairing_codes SET redeemed_at=?, device_id='d1' WHERE id='pc1'`, ts)
	expectErr(t, exec(t, s, `UPDATE pairing_codes SET redeemed_at=?, device_id='d1' WHERE id='pc1'`, ts), "already redeemed")
	mustExec(t, s, `UPDATE pairing_codes SET revoked_at=? WHERE id='pc1'`, ts) // revocation stays possible

	// a finished backup run is immutable and frees the "one running" slot
	mustExec(t, s, `UPDATE backup_runs SET state='completed', finished_at=?, snapshot='20261001T000000Z' WHERE id='b1' AND state='running'`, ts)
	expectErr(t, exec(t, s, `UPDATE backup_runs SET error='x' WHERE id='b1'`), "finished runs are immutable")
	mustExec(t, s, `INSERT INTO backup_runs(id,cause,principal_id,state,started_at,heartbeat_at) VALUES('b2','api','system:cli','running',?,?)`, ts, ts)
}
