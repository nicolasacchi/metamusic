package store

import "testing"

const podIns = `INSERT INTO pods(id,name,kind,batch_id,n,gpu_try,gpu_type,state,cloud,image_digest,price_pph_micro_usd,overhead_reserved_micro_usd,deadline_at,poll_exp,created_at,updated_at)
	VALUES(?,?,?,?,?,?,'NVIDIA GeForce RTX 4090',?,?,'ghcr.io/example/w@sha256:aa',300000,1000,'2026-10-14T12:00:00.000Z',1,'2026-10-14T12:00:00.000Z','2026-10-14T12:00:00.000Z')`

func newPod(t *testing.T, s *Store, id, name string, n, try int) {
	t.Helper()
	mustExec(t, s, podIns, id, name, "batch", "b1", n, try, "intent", "SECURE")
}

// TestMigration_Pods_Constraints pins the constraints and triggers of the pods migration (C394): the transition table is fixed, a pod
// starts as intent and moves only along its rows, frozen and set-once columns, settled, the assignments and the append-only logs.
func TestMigration_Pods_Constraints(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m3World(t, s) // b1, j1, a1
	if err := m3Attempt(t, s, "a2", "j1", 2, "pending", nil); err != nil {
		t.Fatal(err)
	}

	t.Run("pod_transitions is fixed", func(t *testing.T) {
		if n := scalar0(t, s.R, `SELECT COUNT(*) FROM pod_transitions`); n != "19" {
			t.Fatalf("%s rows", n)
		}
		expectErr(t, exec(t, s, `INSERT INTO pod_transitions VALUES('terminated','ready')`), "fixed by the migration")
		expectErr(t, exec(t, s, `UPDATE pod_transitions SET to_state = 'ready'`), "fixed by the migration")
		expectErr(t, exec(t, s, `DELETE FROM pod_transitions`), "fixed by the migration")
	})

	t.Run("pods", func(t *testing.T) {
		expectErr(t, exec(t, s, podIns, "p0", "mm-x", "batch", "b1", 1, 1, "ready", "SECURE"), "a pod starts as intent")
		expectErr(t, exec(t, s, podIns, "p0", "x-bad", "batch", "b1", 1, 1, "intent", "SECURE"), "CHECK")
		expectErr(t, exec(t, s, podIns, "p0", "mm-x", "other", "b1", 1, 1, "intent", "SECURE"), "CHECK")
		expectErr(t, exec(t, s, podIns, "p0", "mm-x", "batch", "nope", 1, 1, "intent", "SECURE"), "FOREIGN KEY")
		expectErr(t, exec(t, s, podIns, "p0", "mm-x", "batch", "b1", 3, 1, "intent", "SECURE"), "CHECK")
		expectErr(t, exec(t, s, podIns, "p0", "mm-x", "batch", "b1", 1, 5, "intent", "SECURE"), "CHECK")
		expectErr(t, exec(t, s, podIns, "p0", "mm-x", "batch", "b1", 1, 1, "intent", "COMMUNITY"), "CHECK")
		expectErr(t, exec(t, s, podIns, "p0", "mm-x", "canary", "b1", 2, 1, "intent", "SECURE"), "CHECK") // a canary is n = 1
		newPod(t, s, "p1", "mm-0190aaaaaaaa", 1, 1)
		expectErr(t, exec(t, s, podIns, "p1", "mm-0190bbbbbbbb", "batch", "b1", 2, 1, "intent", "SECURE"), "UNIQUE")
		expectErr(t, exec(t, s, podIns, "p9", "mm-0190aaaaaaaa", "batch", "b1", 2, 1, "intent", "SECURE"), "UNIQUE")
		expectErr(t, exec(t, s, podIns, "p9", "mm-0190bbbbbbbb", "batch", "b1", 1, 1, "intent", "SECURE"), "UNIQUE")

		for _, col := range []string{"name = 'mm-zzz'", "kind = 'canary'", "batch_id = 'b2'", "n = 2", "gpu_try = 2", "gpu_type = 'x'", "image_digest = 'x'",
			"price_pph_micro_usd = 5", "overhead_reserved_micro_usd = 5", "deadline_at = 'x'", "poll_exp = 5", "created_at = 'x'", "id = 'p2'"} {
			expectErr(t, exec(t, s, `UPDATE pods SET `+col+` WHERE id = 'p1'`), "frozen column")
		}
		mustExec(t, s, `UPDATE pods SET price_pph_micro_usd = 300000 WHERE id = 'p1'`) // the same value is not a change

		expectErr(t, exec(t, s, `UPDATE pods SET state = 'ready' WHERE id = 'p1'`), "illegal state transition")
		expectErr(t, exec(t, s, `UPDATE pods SET state = 'terminated' WHERE id = 'p1'`), "CHECK") // legal, but terminated needs terminated_at
		mustExec(t, s, `UPDATE pods SET state = 'creating' WHERE id = 'p1'`)
		mustExec(t, s, `UPDATE pods SET state = 'booting', provider_ref = 'ref1', running_seen_at = 't1', first_poll_at = 't1', last_poll_at = 't1', gone_seen_at = 't1', overhead_reserved_at = 't1' WHERE id = 'p1'`)
		for _, col := range []string{"provider_ref = 'ref2'", "running_seen_at = 't2'", "first_poll_at = 't2'", "gone_seen_at = 't2'", "overhead_reserved_at = 't2'"} {
			expectErr(t, exec(t, s, `UPDATE pods SET `+col+` WHERE id = 'p1'`), "is set once")
		}
		mustExec(t, s, `UPDATE pods SET provider_ref = 'ref1', last_poll_at = 't3' WHERE id = 'p1'`) // same value, and last_poll_at moves
		newPod(t, s, "p2", "mm-0190bbbbbbbb", 2, 1)
		expectErr(t, exec(t, s, `UPDATE pods SET provider_ref = 'ref1' WHERE id = 'p2'`), "UNIQUE")
		expectErr(t, exec(t, s, `UPDATE pods SET first_poll_at = 't' WHERE id = 'p2'`), "CHECK") // first and last poll come together
		expectErr(t, exec(t, s, `UPDATE pods SET restarts = 3 WHERE id = 'p2'`), "CHECK")
		expectErr(t, exec(t, s, `UPDATE pods SET boot_json = 'not json' WHERE id = 'p2'`), "CHECK")
		expectErr(t, exec(t, s, `UPDATE pods SET settled = 1 WHERE id = 'p2'`), "CHECK") // settled needs terminated

		mustExec(t, s, `UPDATE pods SET state = 'terminated', terminated_at = 't', lifetime_s = 60 WHERE id = 'p2'`)
		expectErr(t, exec(t, s, `UPDATE pods SET state = 'ready' WHERE id = 'p2'`), "illegal state transition")
		expectErr(t, exec(t, s, `UPDATE pods SET lifetime_s = -1 WHERE id = 'p2'`), "CHECK")
		mustExec(t, s, `UPDATE pods SET settled = 1 WHERE id = 'p2'`)
		expectErr(t, exec(t, s, `UPDATE pods SET settled = 0 WHERE id = 'p2'`), "settled is set once")
		mustExec(t, s, `UPDATE pods SET settled = 1 WHERE id = 'p2'`)
		expectErr(t, exec(t, s, `DELETE FROM pods`), "no physical delete")
	})

	t.Run("pod_assignments", func(t *testing.T) {
		ins := `INSERT INTO pod_assignments(attempt_id,batch_id,pod_id,queued_at,handed_at) VALUES(?,?,?,'` + m3ts + `',?)`
		expectErr(t, exec(t, s, ins, "a1", "b2", nil, nil), "batch_id must be")
		expectErr(t, exec(t, s, ins, "nope", "b1", nil, nil), "batch_id must be")
		expectErr(t, exec(t, s, ins, "a1", "b1", "p1", m3ts), "queued, not handed")
		expectErr(t, exec(t, s, `INSERT INTO pod_assignments(attempt_id,batch_id,queued_at,done_at,status) VALUES('a1','b1','`+m3ts+`','`+m3ts+`','ok')`), "queued, not handed")
		mustExec(t, s, ins, "a1", "b1", nil, nil)
		expectErr(t, exec(t, s, ins, "a1", "b1", nil, nil), "UNIQUE")
		for _, col := range []string{"attempt_id = 'a2'", "batch_id = 'b2'", "queued_at = 'x'"} {
			expectErr(t, exec(t, s, `UPDATE pod_assignments SET `+col), "frozen column")
		}
		expectErr(t, exec(t, s, `UPDATE pod_assignments SET pod_id = 'p1'`), "CHECK")                         // pod_id and handed_at come together
		expectErr(t, exec(t, s, `UPDATE pod_assignments SET status = 'ok', done_at = '`+m3ts+`'`), "CHECK")   // ok needs a pod
		expectErr(t, exec(t, s, `UPDATE pod_assignments SET status = 'lost', done_at = '`+m3ts+`'`), "CHECK") // so does lost
		expectErr(t, exec(t, s, `UPDATE pod_assignments SET status = 'bogus', done_at = '`+m3ts+`'`), "CHECK")
		expectErr(t, exec(t, s, `UPDATE pod_assignments SET done_at = '`+m3ts+`'`), "CHECK") // done and status come together
		expectErr(t, exec(t, s, `UPDATE pod_assignments SET execution_ms = -1`), "CHECK")
		// a pod of another batch cannot take the assignment
		if err := m3Estimate(t, s, "e2", 25); err != nil {
			t.Fatal(err)
		}
		if err := m3Batch(t, s, "b2", "e2", "active", 25); err != nil {
			t.Fatal(err)
		}
		mustExec(t, s, podIns, "pb2", "mm-0190cccccccc", "batch", "b2", 1, 1, "intent", "SECURE")
		expectErr(t, exec(t, s, `UPDATE pod_assignments SET pod_id = 'pb2', handed_at = '`+m3ts+`'`), "another batch")
		expectErr(t, exec(t, s, `UPDATE pod_assignments SET pod_id = 'nope', handed_at = '`+m3ts+`'`), "another batch")
		mustExec(t, s, `UPDATE pod_assignments SET pod_id = 'p1', handed_at = '`+m3ts+`'`)
		expectErr(t, exec(t, s, `UPDATE pod_assignments SET pod_id = 'p2'`), "pod_id is set once")
		expectErr(t, exec(t, s, `UPDATE pod_assignments SET handed_at = 'later'`), "handed_at is set once")
		mustExec(t, s, `UPDATE pod_assignments SET pod_id = 'p1', handed_at = '`+m3ts+`'`) // the same values are not a change
		mustExec(t, s, `UPDATE pod_assignments SET status = 'ok', done_at = '`+m3ts+`', execution_ms = 5, agent_execution_ms = 4`)
		for _, col := range []string{"done_at = 'later'", "status = 'error'", "execution_ms = 6", "agent_execution_ms = 5", "error_code = 'X'"} {
			expectErr(t, exec(t, s, `UPDATE pod_assignments SET `+col), "done is set once")
		}
		expectErr(t, exec(t, s, `DELETE FROM pod_assignments`), "no physical delete")
		// a take nobody fetched is closed not_handed, which needs no pod and forbids one
		mustExec(t, s, ins, "a2", "b1", nil, nil)
		expectErr(t, exec(t, s, `UPDATE pod_assignments SET pod_id = 'p1', handed_at = '`+m3ts+`', status = 'not_handed', done_at = '`+m3ts+`' WHERE attempt_id = 'a2'`), "CHECK")
		mustExec(t, s, `UPDATE pod_assignments SET status = 'not_handed', done_at = '`+m3ts+`', error_code = 'SUBMIT_REJECTED' WHERE attempt_id = 'a2'`)
	})

	t.Run("pod_events", func(t *testing.T) {
		mustExec(t, s, `INSERT INTO pod_events(pod_id,at,event,detail_json) VALUES('p1','`+m3ts+`','note','{"a":1}')`)
		expectErr(t, exec(t, s, `INSERT INTO pod_events(pod_id,at,event,detail_json) VALUES('p1','`+m3ts+`','note','nope')`), "CHECK")
		expectErr(t, exec(t, s, `INSERT INTO pod_events(pod_id,at,event) VALUES('nope','`+m3ts+`','note')`), "FOREIGN KEY")
		expectErr(t, exec(t, s, `UPDATE pod_events SET event = 'x'`), "append-only")
		expectErr(t, exec(t, s, `DELETE FROM pod_events`), "no physical delete")
	})

	t.Run("pod_safety_log", func(t *testing.T) {
		ins := `INSERT INTO pod_safety_log(kind,image_digest,verdict,detail_json,created_by,created_at) VALUES(?,?,?,?,'cli:x','` + m3ts + `')`
		mustExec(t, s, ins, "probe", nil, "too_broad", "{}")
		mustExec(t, s, ins, "canary", "ghcr.io/e/w@sha256:aa", "inconclusive", "{}")
		expectErr(t, exec(t, s, ins, "probe", nil, "verified", "{}"), "CHECK")
		expectErr(t, exec(t, s, ins, "canary", "d", "ok", "{}"), "CHECK")
		expectErr(t, exec(t, s, ins, "canary", nil, "verified", "{}"), "CHECK")
		expectErr(t, exec(t, s, ins, "probe", "d", "ok", "{}"), "CHECK")
		expectErr(t, exec(t, s, ins, "other", nil, "ok", "{}"), "CHECK")
		expectErr(t, exec(t, s, ins, "probe", nil, "ok", "nope"), "CHECK")
		expectErr(t, exec(t, s, `UPDATE pod_safety_log SET verdict = 'failed'`), "append-only")
		expectErr(t, exec(t, s, `DELETE FROM pod_safety_log`), "no physical delete")
	})
}
