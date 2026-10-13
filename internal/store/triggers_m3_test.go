package store

import (
	"strings"
	"testing"
)

// ---- seeding helpers (synthetic rows only) -------------------------------------------------------

const m3ts = "2026-10-14T12:00:00.000Z"

func m3Estimate(t *testing.T, s *Store, id string, ceiling int) error {
	t.Helper()
	return exec(t, s, `INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at)
		VALUES(?,'agent:t1','agent','yue2','fake','[]','[]',1,'{}',10,20,5,?,690000,2,20,0,'[]',?,?,?)`,
		id, ceiling, "tok-"+id, m3ts, m3ts)
}

func m3Batch(t *testing.T, s *Store, id, estimate, state string, ceiling int) error {
	t.Helper()
	fin := any(nil)
	if state == "finished" || state == "rejected" || state == "expired" {
		fin = m3ts
	}
	return exec(t, s, `INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at,finished_at)
		VALUES(?,?,'agent:t1','agent','t1','api','yue2','fake',1,NULL,10,20,5,?,690000,200,15,2,20,?,?,?)`,
		id, estimate, ceiling, state, m3ts, fin)
}

const m3JobQ = `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,
	timeout_s,expected_micro_usd,reserved_micro_usd,state,lease_owner,lease_expires_at,next_attempt_at,finished_at,created_at,updated_at)
	VALUES(?,?,?, 'generate','yue2','fake',?, 'reroll_seed','{}','T',100,50,120,10,20,?,?,?,?,?,?,?)`

func m3Job(t *testing.T, s *Store, id, batch string, idx int, recipe, state string, lease any) error {
	t.Helper()
	exp := any(nil)
	if lease != nil {
		exp = m3ts
	}
	fin := any(nil)
	if state == "completed" || state == "failed" || state == "cancelled" || state == "expired" {
		fin = m3ts
	}
	return exec(t, s, m3JobQ, id, batch, idx, recipe, state, lease, exp, m3ts, fin, m3ts, m3ts)
}

func m3Attempt(t *testing.T, s *Store, id, job string, n int, state string, ref any) error {
	t.Helper()
	basis, at := any(nil), any(nil)
	if state == "done" || state == "rejected" {
		basis, at = "provider", m3ts
	}
	return exec(t, s, `INSERT INTO job_attempts(id,job_id,n,backend,state,ref,submitted_at,window_ends_at,price_pph_micro_usd,terminal_basis,terminal_at,created_at)
		VALUES(?,?,?,'fake',?,?,?,?,690000,?,?,?)`, id, job, n, state, ref, m3ts, m3ts, basis, at, m3ts)
}

// m3World seeds one row in every new table plus the parents they need; the DELETE test needs a row each.
func m3World(t *testing.T, s *Store) {
	t.Helper()
	r := insertRecipe(t, s, "m3a", 1)
	insertTrack(t, s, "t1", r, "T1", "f1", 100)
	if err := m3Estimate(t, s, "e1", 25); err != nil {
		t.Fatal(err)
	}
	if err := m3Batch(t, s, "b1", "e1", "active", 25); err != nil {
		t.Fatal(err)
	}
	if err := m3Job(t, s, "j1", "b1", 0, r, "queued", nil); err != nil {
		t.Fatal(err)
	}
	if err := m3Attempt(t, s, "a1", "j1", 1, "pending", nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO recipe_keys(recipe_id,content_hash,render_hash) VALUES('` + r + `','c','r')`,
		`INSERT INTO spend_cap_changes(at,principal_id,target,before_json,after_json) VALUES('` + m3ts + `','human:h','global','{}','{}')`,
		`INSERT INTO approvals(batch_id,state,reasons,created_at,expires_at) VALUES('b1','pending','[]','` + m3ts + `','` + m3ts + `')`,
		`INSERT INTO job_events(job_id,at,event) VALUES('j1','` + m3ts + `','claim')`,
		`INSERT INTO uploads(id,job_id,attempt_id,artifact,max_bytes,exp,state,created_at) VALUES('u1','j1','a1','audio.flac',100,1,'open','` + m3ts + `')`,
		`INSERT INTO ledger(at,kind,micro_usd,batch_id,job_id,principal_id,reason) VALUES('` + m3ts + `','reserve',20,'b1','j1','agent:t1','take')`,
		`INSERT INTO billing_windows VALUES('` + m3ts + `','2026-10-14T13:00:00.000Z','hour',1,5,5,5,5,1,'ok','` + m3ts + `')`,
		`INSERT INTO billing_buckets VALUES('` + m3ts + `','` + m3ts + `',5,5,0)`,
		`INSERT INTO worker_meter VALUES('wk','` + m3ts + `','` + m3ts + `','` + m3ts + `',10,'RUNNING',NULL)`,
		`INSERT INTO env_fingerprints VALUES('env1','yue2','0.1','c','{}','d','fake','A5000','img','{}','` + m3ts + `')`,
		`INSERT INTO runner_lease VALUES(1,'o1','` + m3ts + `','` + m3ts + `','` + m3ts + `')`,
		`INSERT INTO provider_state(backend,armed,changed_by,changed_at) VALUES('fake',1,'system:default','` + m3ts + `')`,
		`INSERT INTO provider_trips(id,backend,tripped_at,reason,details_json,actions_json) VALUES('trip1','fake','` + m3ts + `','x','{}','[]')`,
	} {
		mustExec(t, s, q)
	}
}

func TestTriggers_M3(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m3World(t, s)

	t.Run("no physical delete", func(t *testing.T) {
		for _, tbl := range []string{"recipe_keys", "spend_caps", "spend_cap_changes", "estimates", "batches", "approvals",
			"job_transitions", "jobs", "job_attempts", "job_events", "uploads", "ledger", "billing_windows", "billing_buckets",
			"worker_meter", "env_fingerprints", "rtf_stats", "runner_lease", "provider_state", "provider_trips"} {
			err := exec(t, s, `DELETE FROM `+tbl)
			if err == nil {
				t.Errorf("DELETE FROM %s succeeded", tbl)
			}
		}
	})

	t.Run("append-only", func(t *testing.T) {
		expectErr(t, exec(t, s, `UPDATE ledger SET micro_usd=1`), "ledger: append-only")
		expectErr(t, exec(t, s, `UPDATE job_events SET code='x'`), "job_events: append-only")
		expectErr(t, exec(t, s, `UPDATE spend_cap_changes SET target='x'`), "spend_cap_changes: append-only")
		expectErr(t, exec(t, s, `UPDATE billing_buckets SET total_micro_usd=9`), "billing_buckets: append-only")
		expectErr(t, exec(t, s, `UPDATE billing_windows SET verdict='ok'`), "billing_windows: immutable")
		expectErr(t, exec(t, s, `UPDATE recipe_keys SET render_hash='z'`), "recipe_keys: immutable")
		expectErr(t, exec(t, s, `UPDATE env_fingerprints SET decoder='z'`), "env_fingerprints: immutable")
	})

	t.Run("job_transitions immutable", func(t *testing.T) {
		expectErr(t, exec(t, s, `INSERT INTO job_transitions VALUES('queued','completed')`), "job_transitions: immutable")
		expectErr(t, exec(t, s, `UPDATE job_transitions SET to_state='x'`), "job_transitions: immutable")
		expectErr(t, exec(t, s, `DELETE FROM job_transitions`), "job_transitions: immutable")
	})

	t.Run("token consumed once", func(t *testing.T) {
		mustExec(t, s, `UPDATE estimates SET consumed_at=?, consumed_batch_id='b1' WHERE id='e1'`, m3ts)
		expectErr(t, exec(t, s, `UPDATE estimates SET consumed_at=?, consumed_batch_id='b2' WHERE id='e1'`, m3ts), "already consumed")
		expectErr(t, exec(t, s, `UPDATE estimates SET consumed_batch_id=NULL WHERE id='e1'`), "already consumed")
	})

	t.Run("frozen columns", func(t *testing.T) {
		for q, want := range map[string]string{
			`UPDATE estimates SET max_micro_usd=max_micro_usd WHERE id='e1'`:       "estimates: frozen column",
			`UPDATE estimates SET token_hash='x' WHERE id='e1'`:                    "estimates: frozen column",
			`UPDATE batches SET n_takes=2 WHERE id='b1'`:                           "batches: frozen column",
			`UPDATE batches SET ceiling_micro_usd=ceiling_micro_usd WHERE id='b1'`: "batches: frozen column",
			`UPDATE approvals SET reasons='[]' WHERE batch_id='b1'`:                "approvals: frozen column",
			`UPDATE approvals SET expires_at='x' WHERE batch_id='b1'`:              "approvals: frozen column",
			`UPDATE jobs SET title='x' WHERE id='j1'`:                              "jobs: frozen column",
			`UPDATE jobs SET timeout_s=1 WHERE id='j1'`:                            "jobs: frozen column",
			`UPDATE job_attempts SET window_ends_at='x' WHERE id='a1'`:             "job_attempts: frozen column",
			`UPDATE job_attempts SET price_pph_micro_usd=1 WHERE id='a1'`:          "job_attempts: frozen column",
			`UPDATE uploads SET max_bytes=1 WHERE id='u1'`:                         "uploads: frozen column",
			`UPDATE uploads SET artifact='x' WHERE id='u1'`:                        "uploads: frozen column",
			`UPDATE provider_trips SET reason='y' WHERE id='trip1'`:                "provider_trips: frozen column",
			`UPDATE tracks SET job_id='j1' WHERE id='t1'`:                          "tracks: frozen column",
			`UPDATE tracks SET env_id='env1' WHERE id='t1'`:                        "tracks: frozen column",
			`UPDATE tracks SET job_id=job_id WHERE id='t1'`:                        "tracks: frozen column",
			`UPDATE batches SET id='zz' WHERE id='b1'`:                             "batches: frozen column",
		} {
			expectErr(t, exec(t, s, q), want)
		}
	})

	t.Run("batch lifecycle", func(t *testing.T) {
		mustExec(t, s, `UPDATE batches SET state='finished', finished_at=? WHERE id='b1'`, m3ts)
		expectErr(t, exec(t, s, `UPDATE batches SET state='active' WHERE id='b1'`), "finished batches are immutable")
		expectErr(t, exec(t, s, `UPDATE batches SET finished_at=NULL WHERE id='b1'`), "finished batches are immutable")
		mustExec(t, s, `UPDATE batches SET reconciled_at=? WHERE id='b1'`, m3ts)
		expectErr(t, exec(t, s, `UPDATE batches SET reconciled_at=? WHERE id='b1'`, m3ts), "already reconciled")
		// reconciled before finished is a CHECK
		if err := m3Estimate(t, s, "e2", 25); err != nil {
			t.Fatal(err)
		}
		if err := m3Batch(t, s, "b2", "e2", "active", 25); err != nil {
			t.Fatal(err)
		}
		expectErr(t, exec(t, s, `UPDATE batches SET reconciled_at=? WHERE id='b2'`, m3ts), "CHECK")
	})

	t.Run("approvals decided are immutable", func(t *testing.T) {
		mustExec(t, s, `UPDATE approvals SET state='approved', via='web', decided_by='human:h', decided_at=? WHERE batch_id='b1'`, m3ts)
		expectErr(t, exec(t, s, `UPDATE approvals SET state='rejected' WHERE batch_id='b1'`), "decided approvals are immutable")
		expectErr(t, exec(t, s, `UPDATE approvals SET reason='x' WHERE batch_id='b1'`), "decided approvals are immutable")
	})

	t.Run("ceiling checks", func(t *testing.T) {
		expectErr(t, m3Estimate(t, s, "e3", 24), "CHECK")
		expectErr(t, m3Estimate(t, s, "e3", 26), "CHECK")
		if err := m3Estimate(t, s, "e3", 25); err != nil {
			t.Fatal(err)
		}
		expectErr(t, m3Batch(t, s, "b3", "e3", "active", 24), "CHECK")
	})

	t.Run("worker meter and uploads", func(t *testing.T) {
		expectErr(t, exec(t, s, `UPDATE worker_meter SET uptime_s=5`), "uptime never decreases")
		mustExec(t, s, `UPDATE worker_meter SET uptime_s=11`)
		mustExec(t, s, `UPDATE uploads SET state='received', sha256='x', bytes=1, received_at=? WHERE id='u1'`, m3ts)
		expectErr(t, exec(t, s, `UPDATE uploads SET sha256='y' WHERE id='u1'`), "received uploads are immutable")
		expectErr(t, exec(t, s, `UPDATE uploads SET bytes=2 WHERE id='u1'`), "received uploads are immutable")
		mustExec(t, s, `UPDATE uploads SET state='consumed' WHERE id='u1'`)
		expectErr(t, exec(t, s, `UPDATE uploads SET state='open' WHERE id='u1'`), "CHECK")
	})

	t.Run("provider trips", func(t *testing.T) {
		expectErr(t, exec(t, s, `INSERT INTO provider_trips(id,backend,tripped_at,reason,details_json,actions_json) VALUES('trip2','fake',?,'x','{}','[]')`, m3ts), "UNIQUE")
		mustExec(t, s, `INSERT INTO provider_trips(id,backend,tripped_at,reason,details_json,actions_json) VALUES('trip3','other',?,'x','{}','[]')`, m3ts)
		mustExec(t, s, `UPDATE provider_trips SET reset_at=?, reset_by='human:h', reset_reason='checked' WHERE id='trip1'`, m3ts)
		expectErr(t, exec(t, s, `UPDATE provider_trips SET reset_reason='again' WHERE id='trip1'`), "already reset")
		// once reset, a new trip for the backend is allowed again
		mustExec(t, s, `INSERT INTO provider_trips(id,backend,tripped_at,reason,details_json,actions_json) VALUES('trip4','fake',?,'x','{}','[]')`, m3ts)
		expectErr(t, exec(t, s, `UPDATE provider_trips SET reset_at=? WHERE id='trip4'`, m3ts), "CHECK")
	})
}

func TestTriggers_M3Jobs(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m3World(t, s)
	r := "r-m3a"

	// initial states
	expectErr(t, m3Job(t, s, "jx", "b1", 5, r, "generating", "o"), "illegal initial state")
	expectErr(t, m3Job(t, s, "jx", "b1", 5, r, "completed", nil), "illegal initial state")
	if err := m3Job(t, s, "jc", "b1", 6, r, "awaiting_confirmation", nil); err != nil {
		t.Fatal(err)
	}
	// unleased states never hold a lease (CHECK)
	expectErr(t, m3Job(t, s, "jl", "b1", 7, r, "queued", "o"), "CHECK")
	expectErr(t, exec(t, s, `UPDATE jobs SET lease_owner='o', lease_expires_at=? WHERE id='j1'`, m3ts), "CHECK")
	// unique take index per batch
	expectErr(t, m3Job(t, s, "jd", "b1", 0, r, "queued", nil), "UNIQUE")

	// transition table
	expectErr(t, exec(t, s, `UPDATE jobs SET state='completed' WHERE id='j1'`), "illegal transition")
	expectErr(t, exec(t, s, `UPDATE jobs SET state='generating' WHERE id='j1'`), "illegal transition")
	mustExec(t, s, `UPDATE jobs SET state='submitting', lease_owner='o', lease_expires_at=? WHERE id='j1'`, m3ts)
	// a leased job may not be left without its owner pair
	expectErr(t, exec(t, s, `UPDATE jobs SET lease_owner=NULL WHERE id='j1'`), "CHECK")
	mustExec(t, s, `UPDATE jobs SET state='starting' WHERE id='j1'`)
	mustExec(t, s, `UPDATE jobs SET state='queued', lease_owner=NULL, lease_expires_at=NULL WHERE id='j1'`)
	// state unchanged is not a transition
	mustExec(t, s, `UPDATE jobs SET attempts=attempts+1 WHERE id='j1'`)

	// completed requires an outcome and a track (CHECKs), terminal jobs are immutable
	mustExec(t, s, `UPDATE jobs SET state='cancelled', finished_at=?, lease_owner=NULL, lease_expires_at=NULL WHERE id='j1'`, m3ts)
	expectErr(t, exec(t, s, `UPDATE jobs SET attempts=5 WHERE id='j1'`), "terminal jobs are immutable")
	expectErr(t, exec(t, s, `UPDATE jobs SET state='queued' WHERE id='j1'`), "terminal jobs are immutable")

	// the terminal-state CHECKs
	if err := m3Job(t, s, "j2", "b1", 8, r, "queued", nil); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `UPDATE jobs SET state='submitting', lease_owner='o', lease_expires_at=? WHERE id='j2'`, m3ts)
	for _, st := range []string{"starting", "generating", "transferring", "ingesting"} {
		mustExec(t, s, `UPDATE jobs SET state=? WHERE id='j2'`, st)
	}
	expectErr(t, exec(t, s, `UPDATE jobs SET state='completed', finished_at=?, lease_owner=NULL, lease_expires_at=NULL WHERE id='j2'`, m3ts), "CHECK")
	expectErr(t, exec(t, s, `UPDATE jobs SET state='completed', outcome='clean', finished_at=?, lease_owner=NULL, lease_expires_at=NULL WHERE id='j2'`, m3ts), "CHECK") // no track
	mustExec(t, s, `UPDATE jobs SET state='completed', outcome='clean', track_id='t1', finished_at=?, lease_owner=NULL, lease_expires_at=NULL WHERE id='j2'`, m3ts)

	// cancel intent pairs
	if err := m3Job(t, s, "j3", "b1", 9, r, "queued", nil); err != nil {
		t.Fatal(err)
	}
	expectErr(t, exec(t, s, `UPDATE jobs SET cancel_requested_at=? WHERE id='j3'`, m3ts), "CHECK")
	mustExec(t, s, `UPDATE jobs SET cancel_requested_at=?, cancel_requested_by='human:h' WHERE id='j3'`, m3ts)
}

func TestAttemptTransitions_SQL(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m3World(t, s)
	states := []string{"pending", "submitted", "unknown", "rejected", "done"}
	legal := map[[2]string]bool{
		{"pending", "submitted"}: true, {"pending", "unknown"}: true, {"pending", "rejected"}: true,
		{"unknown", "submitted"}: true, {"unknown", "done"}: true,
		{"submitted", "unknown"}: true, {"submitted", "done"}: true,
	}
	set := map[string]string{
		"submitted": `state='submitted', ref=COALESCE(ref,'ref-late')`,
		"unknown":   `state='unknown'`,
		"rejected":  `state='rejected', terminal_basis='not_accepted', terminal_at='` + m3ts + `'`,
		"done":      `state='done', terminal_basis='provider', terminal_at='` + m3ts + `'`,
		"pending":   `state='pending'`,
	}
	n := 100
	for _, from := range states {
		for _, to := range states {
			if from == to {
				continue
			}
			n++
			id := "att-" + from + "-" + to
			var ref any
			if from == "submitted" {
				ref = "ref-" + id
			}
			if err := m3Job(t, s, "jj"+itoa(n), "b1", n, "r-m3a", "queued", nil); err != nil {
				t.Fatal(err)
			}
			if err := m3Attempt(t, s, id, "jj"+itoa(n), 1, from, ref); err != nil {
				t.Fatalf("seed %s: %v", from, err)
			}
			err := exec(t, s, `UPDATE job_attempts SET `+set[to]+` WHERE id=?`, id)
			switch {
			case legal[[2]string{from, to}] && err != nil:
				t.Errorf("%s -> %s: legal move refused: %v", from, to, err)
			case !legal[[2]string{from, to}] && err == nil:
				t.Errorf("%s -> %s: illegal move accepted", from, to)
			case !legal[[2]string{from, to}] && !strings.Contains(err.Error(), "illegal transition") && !strings.Contains(err.Error(), "immutable"):
				t.Errorf("%s -> %s: unexpected error %v", from, to, err)
			}
		}
	}

	// an unknown attempt that already carries a ref cannot go to submitted (it is polled as unknown)
	if err := m3Job(t, s, "jq", "b1", 900, "r-m3a", "queued", nil); err != nil {
		t.Fatal(err)
	}
	if err := m3Attempt(t, s, "att-unk-ref", "jq", 1, "unknown", "ref-1"); err != nil {
		t.Fatalf("unknown with a ref must be allowed: %v", err)
	}
	expectErr(t, exec(t, s, `UPDATE job_attempts SET state='submitted' WHERE id='att-unk-ref'`), "illegal transition")
	// a pending attempt with a ref is a CHECK
	expectErr(t, m3Attempt(t, s, "att-pend-ref", "jq", 2, "pending", "ref-2"), "CHECK")
	// submitted needs a ref
	expectErr(t, m3Attempt(t, s, "att-sub-noref", "jq", 3, "submitted", nil), "CHECK")

	// ref set once, announced_ref set once, terminal attempts immutable, settled once
	if err := m3Job(t, s, "jr", "b1", 901, "r-m3a", "queued", nil); err != nil {
		t.Fatal(err)
	}
	if err := m3Attempt(t, s, "att-r", "jr", 1, "pending", nil); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `UPDATE job_attempts SET state='submitted', ref='r1', announced_ref='an1', announced_at=? WHERE id='att-r'`, m3ts)
	expectErr(t, exec(t, s, `UPDATE job_attempts SET ref='r2' WHERE id='att-r'`), "ref is set once")
	expectErr(t, exec(t, s, `UPDATE job_attempts SET announced_ref='an2' WHERE id='att-r'`), "announced_ref is set once")
	mustExec(t, s, `UPDATE job_attempts SET announced_ref='an1' WHERE id='att-r'`) // same value is not a change
	mustExec(t, s, `UPDATE job_attempts SET settled=1 WHERE id='att-r'`)
	expectErr(t, exec(t, s, `UPDATE job_attempts SET settled=1 WHERE id='att-r'`), "already settled")
	mustExec(t, s, `UPDATE job_attempts SET state='done', terminal_basis='provider', terminal_at=?, execution_ms=5 WHERE id='att-r'`, m3ts)
	expectErr(t, exec(t, s, `UPDATE job_attempts SET execution_ms=6 WHERE id='att-r'`), "terminal attempts are immutable")
	expectErr(t, exec(t, s, `UPDATE job_attempts SET remote_status='x' WHERE id='att-r'`), "terminal attempts are immutable")
}

func TestLedger_SQLChecks(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m3World(t, s)
	const q = `INSERT INTO ledger(at,kind,micro_usd,batch_id,job_id,window_start,principal_id,reason) VALUES(?,?,?,?,NULL,?,'agent:t1','x')`
	expectErr(t, exec(t, s, q, m3ts, "reserve", 0, "b1", nil), "CHECK")
	expectErr(t, exec(t, s, q, m3ts, "settle", -1, "b1", nil), "CHECK")
	expectErr(t, exec(t, s, q, m3ts, "bill", -5, nil, nil), "CHECK") // bill without window_start
	expectErr(t, exec(t, s, q, m3ts, "bill", -5, "b1", m3ts), "CHECK")
	expectErr(t, exec(t, s, q, m3ts, "settle", 5, "b1", m3ts), "CHECK") // window_start on a non-bill row
	expectErr(t, exec(t, s, q, m3ts, "bogus", 5, "b1", nil), "CHECK")
	mustExec(t, s, q, m3ts, "bill", -5, nil, m3ts)
	mustExec(t, s, q, m3ts, "settle", 0, "b1", nil)
	mustExec(t, s, q, m3ts, "residual", 3, "b1", nil)
	mustExec(t, s, q, m3ts, "release", 3, "b1", nil)
}

func TestRecipes_GeneratedIDUnique(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	const q = `INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'yue2','same-id',?,'pop','[Verse]\nla',?,'full',?,?,?,?)`
	js := func(seed string) string {
		return `{"cot":"full","id":"same-id","lyrics":"[Verse]\nla","seed":` + seed + `,"style":"pop"}`
	}
	mustExec(t, s, q, "g1", js("1"), 1, "h1", "c1", "agent:x", m3ts)
	expectErr(t, exec(t, s, q, "g2", js("2"), 2, "h2", "c2", "agent:x", m3ts), "UNIQUE")
	expectErr(t, exec(t, s, q, "g3", js("3"), 3, "h3", "c3", "human:y", m3ts), "UNIQUE") // vs the generated one
	mustExec(t, s, q, "i1", js("4"), 4, "h4", "c4", "import", m3ts)
	mustExec(t, s, q, "i2", js("5"), 5, "h5", "c5", "import", m3ts)
}
