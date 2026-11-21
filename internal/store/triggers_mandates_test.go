package store

import "testing"

const mandateIns = `INSERT INTO mandates(id,holder_key,title,budget_micro_usd,valid_from,valid_until,scope_json,state,created_by,created_at,
	revoked_by,revoked_at,revoke_reason) VALUES(?,?,?,?,?,?,?,?,'human:owner',?,?,?,?)`

// TestMigration_Mandates_Constraints pins the constraints and triggers of 0024_mandates.sql (C370): every CHECK refuses, a mandate is
// never deleted, only approved -> revoked is a transition, a revoked row is immutable, and mandate_id on estimates and batches is
// set at insert and never changes.
func TestMigration_Mandates_Constraints(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	const from, until = "2026-10-14T12:00:00.000Z", "2026-10-15T12:00:00.000Z"
	ins := func(id, holder, title string, budget int, vf, vu, scope, state string, rb, ra, rr any) error {
		return exec(t, s, mandateIns, id, holder, title, budget, vf, vu, scope, state, m3ts, rb, ra, rr)
	}
	t.Run("checks", func(t *testing.T) {
		expectErr(t, ins("x1", "agent:nightshift", "", 5, from, until, "{}", "approved", nil, nil, nil), "CHECK")
		expectErr(t, ins("x2", "agent:nightshift", string(make([]byte, 121)), 5, from, until, "{}", "approved", nil, nil, nil), "CHECK")
		expectErr(t, ins("x3", "agent:nightshift", "t", 0, from, until, "{}", "approved", nil, nil, nil), "CHECK")
		expectErr(t, ins("x4", "agent:nightshift", "t", 100000001, from, until, "{}", "approved", nil, nil, nil), "CHECK")
		expectErr(t, ins("x5", "agent:nightshift", "t", 5, from, until, "not json", "approved", nil, nil, nil), "CHECK")
		expectErr(t, ins("x6", "agent:nightshift", "t", 5, from, until, "[]", "approved", nil, nil, nil), "CHECK")
		expectErr(t, ins("x7", "agent:nightshift", "t", 5, from, until, "{}", "pending", nil, nil, nil), "CHECK")
		expectErr(t, ins("x8", "agent:nightshift", "t", 5, until, from, "{}", "approved", nil, nil, nil), "CHECK")
		expectErr(t, ins("x9", "agent:nightshift", "t", 5, from, from, "{}", "approved", nil, nil, nil), "CHECK")
		expectErr(t, ins("y1", "agent:nightshift", "t", 5, from, until, "{}", "revoked", nil, nil, nil), "CHECK")           // revoked without the columns
		expectErr(t, ins("y2", "agent:nightshift", "t", 5, from, until, "{}", "approved", "human:o", m3ts, "why"), "CHECK") // columns without revoked
		expectErr(t, ins("y3", "agent:nightshift", "t", 5, from, until, "{}", "revoked", nil, m3ts, "why"), "CHECK")        // by missing
		expectErr(t, ins("y4", "agent:nightshift", "t", 5, from, until, "{}", "revoked", "human:o", m3ts, ""), "CHECK")     // reason too short
		expectErr(t, ins("y5", "agent:nightshift", "t", 5, from, until, "{}", "revoked", "human:o", m3ts, string(make([]byte, 501))), "CHECK")
		mustExec(t, s, mandateIns, "ok", "agent:nightshift", "t", 5, from, until, "{}", "approved", m3ts, nil, nil, nil)
		expectErr(t, exec(t, s, mandateIns, "ok", "agent:nightshift", "t", 5, from, until, "{}", "approved", m3ts, nil, nil, nil), "UNIQUE")
	})
	t.Run("transitions", func(t *testing.T) {
		expectErr(t, exec(t, s, `DELETE FROM mandates`), "no physical delete")
		for _, col := range []string{"id = 'z'", "holder_key = 'z'", "title = 'z'", "budget_micro_usd = 9", "valid_from = 'z'",
			"valid_until = 'z'", "scope_json = '{}'", "created_by = 'z'", "created_at = 'z'"} {
			expectErr(t, exec(t, s, `UPDATE mandates SET `+col+` WHERE id = 'ok'`), "frozen column")
		}
		expectErr(t, exec(t, s, `UPDATE mandates SET state = 'pending' WHERE id = 'ok'`), "illegal transition")
		mustExec(t, s, `UPDATE mandates SET state = 'approved' WHERE id = 'ok'`) // not a change
		mustExec(t, s, `UPDATE mandates SET state='revoked', revoked_by='human:o', revoked_at=?, revoke_reason='done' WHERE id = 'ok'`, m3ts)
		expectErr(t, exec(t, s, `UPDATE mandates SET state='approved', revoked_by=NULL, revoked_at=NULL, revoke_reason=NULL WHERE id = 'ok'`), "immutable")
		expectErr(t, exec(t, s, `UPDATE mandates SET revoke_reason = 'again' WHERE id = 'ok'`), "immutable")
		expectErr(t, exec(t, s, `UPDATE mandates SET revoked_by = 'x' WHERE id = 'ok'`), "immutable")
	})
	t.Run("mandate_id", func(t *testing.T) {
		mustExec(t, s, mandateIns, "m2", "agent:nightshift", "t", 5, from, until, "{}", "approved", m3ts, nil, nil, nil)
		m3World(t, s)
		mustExec(t, s, `UPDATE estimates SET mandate_id = NULL WHERE id = 'e1'`) // not a change
		expectErr(t, exec(t, s, `UPDATE estimates SET mandate_id = 'm2' WHERE id = 'e1'`), "frozen column")
		expectErr(t, exec(t, s, `UPDATE batches SET mandate_id = 'm2' WHERE id = 'b1'`), "frozen column")
		expectErr(t, exec(t, s, `UPDATE estimates SET mandate_id = 'nope' WHERE id = 'e1'`), "frozen column")
		mustExec(t, s, `INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
			expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
			requires_approval,approval_reasons,token_hash,created_at,expires_at,mandate_id)
			VALUES('e2','agent:t1','agent','yue2','fake','[]','[]',1,'{}',10,20,5,25,690000,2,20,0,'[]','tok-e2',?,?,'m2')`, m3ts, m3ts)
		expectErr(t, exec(t, s, `UPDATE estimates SET mandate_id = NULL WHERE id = 'e2'`), "frozen column")
		if n := scalar0(t, s.R, `SELECT COUNT(*) FROM estimates WHERE mandate_id IS NULL`); n != "1" {
			t.Fatalf("existing estimates lost their NULL: %s", n)
		}
	})
}
