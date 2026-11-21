package store

import "testing"

// TestMigration_UploadFallback_Constraints pins the constraints and triggers of 0023_upload_fallback.sql (C399.3): the via column,
// the fallback_objects set-once and no-delete rules, and the append-only upload_drills.
func TestMigration_UploadFallback_Constraints(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m3World(t, s) // u1 (open) belongs to attempt a1 of job j1
	if err := m3Attempt(t, s, "a2", "j1", 2, "pending", nil); err != nil {
		t.Fatal(err)
	}

	t.Run("via", func(t *testing.T) {
		expectErr(t, exec(t, s, `UPDATE uploads SET state='received', sha256='aa', bytes=3, received_at='`+m3ts+`', via='elsewhere' WHERE id = 'u1'`), "CHECK")
		expectErr(t, exec(t, s, `UPDATE uploads SET via = 'primary' WHERE id = 'u1'`), "via needs a received upload")
		expectErr(t, exec(t, s, `INSERT INTO uploads(id,job_id,attempt_id,artifact,max_bytes,exp,state,via,created_at) VALUES('ux','j1','a1','x.flac',1,1,'open','primary','`+m3ts+`')`), "via is set at receipt")
		mustExec(t, s, `UPDATE uploads SET state='received', sha256='aa', bytes=3, received_at='`+m3ts+`', via='fallback' WHERE id = 'u1'`)
		expectErr(t, exec(t, s, `UPDATE uploads SET via = 'primary' WHERE id = 'u1'`), "via is set once")
		mustExec(t, s, `UPDATE uploads SET via = 'fallback' WHERE id = 'u1'`) // the same value is not a change
		mustExec(t, s, `UPDATE uploads SET state = 'consumed' WHERE id = 'u1'`)
	})
	t.Run("fallback_objects", func(t *testing.T) {
		expectErr(t, exec(t, s, `INSERT INTO fallback_objects(upload_id,attempt_id,minted_at) VALUES('u1','a2','`+m3ts+`')`), "attempt_id must be")
		expectErr(t, exec(t, s, `INSERT INTO fallback_objects(upload_id,attempt_id,minted_at) VALUES('nope','a1','`+m3ts+`')`), "attempt_id must be")
		expectErr(t, exec(t, s, `INSERT INTO fallback_objects(upload_id,attempt_id,minted_at,deleted_at) VALUES('u1','a1','`+m3ts+`','`+m3ts+`')`), "neither fetched nor deleted")
		mustExec(t, s, `INSERT INTO fallback_objects(upload_id,attempt_id,minted_at) VALUES('u1','a1','`+m3ts+`')`)
		expectErr(t, exec(t, s, `INSERT INTO fallback_objects(upload_id,attempt_id,minted_at) VALUES('u1','a1','`+m3ts+`')`), "UNIQUE")
		for _, col := range []string{"upload_id = 'u2'", "attempt_id = 'a2'", "minted_at = 'x'"} {
			expectErr(t, exec(t, s, `UPDATE fallback_objects SET `+col), "frozen column")
		}
		mustExec(t, s, `UPDATE fallback_objects SET fetched_at = '`+m3ts+`'`)
		expectErr(t, exec(t, s, `UPDATE fallback_objects SET fetched_at = '2027'`), "fetched_at is set once")
		mustExec(t, s, `UPDATE fallback_objects SET deleted_at = '`+m3ts+`'`)
		expectErr(t, exec(t, s, `UPDATE fallback_objects SET deleted_at = '2027'`), "deleted_at is set once")
		expectErr(t, exec(t, s, `DELETE FROM fallback_objects`), "no physical delete")
	})
	t.Run("upload_drills", func(t *testing.T) {
		ins := `INSERT INTO upload_drills(attempt_id,until,reason,created_by,created_at) VALUES(?,?,?,?,'` + m3ts + `')`
		expectErr(t, exec(t, s, ins, "a1", m3ts, "", "human:h"), "CHECK")
		expectErr(t, exec(t, s, ins, "a1", m3ts, string(make([]byte, 501)), "human:h"), "CHECK")
		expectErr(t, exec(t, s, ins, "a1", m3ts, "drill", ""), "CHECK")
		expectErr(t, exec(t, s, ins, "nope", m3ts, "drill", "cli:x"), "FOREIGN KEY")
		mustExec(t, s, ins, "a1", m3ts, "drill", "cli:x")
		expectErr(t, exec(t, s, ins, "a1", m3ts, "again", "cli:x"), "UNIQUE")
		expectErr(t, exec(t, s, `UPDATE upload_drills SET reason = 'x'`), "append-only")
		expectErr(t, exec(t, s, `DELETE FROM upload_drills`), "no physical delete")
	})
}
