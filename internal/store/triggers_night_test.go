package store

import "testing"

// TestMigration_NightShift_Constraints pins 0026_night_shift.sql (C377.1): the settings row is the OFF default and cannot be
// deleted or made invalid, a night is claimed once per local day, never deleted, its batch and mandate are set once and only the
// documented state moves are allowed.
func TestMigration_NightShift_Constraints(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	t.Run("default_is_off", func(t *testing.T) {
		if got := scalar0(t, s.R, `SELECT enabled || '|' || at || '|' || max_takes || '|' || explore_ppm || '|' || min_stars || '|' || listening_checks ||
			'|' || COALESCE(halted_at,'-') FROM night_shift_settings WHERE id = 1`); got != "0|01:30|8|300000|4|on|-" {
			t.Fatalf("settings row = %s", got)
		}
		if n := scalar0(t, s.R, `SELECT COUNT(*) FROM night_shift_runs`); n != "0" {
			t.Fatalf("runs = %s", n)
		}
	})
	t.Run("settings", func(t *testing.T) {
		for _, set := range []string{"enabled = 2", "at = '0130'", "at = '01-30'", "max_takes = 0", "max_takes = 13", "explore_ppm = -1", "explore_ppm = 500001",
			"min_stars = 0", "min_stars = 6", "listening_checks = 'auto'", "halted_at = 'x'", "halted_at = 'x', halted_by = 'y'",
			"halted_by = 'y', halt_reason = 'z'", "halted_at = 'x', halted_by = 'y', halt_reason = ''"} {
			expectErr(t, exec(t, s, `UPDATE night_shift_settings SET `+set), "CHECK")
		}
		expectErr(t, exec(t, s, `INSERT INTO night_shift_settings(id,enabled,at,max_takes,explore_ppm,min_stars,listening_checks,updated_by,updated_at)
			VALUES(2,0,'01:30',8,300000,4,'on','x','y')`), "CHECK")
		expectErr(t, exec(t, s, `DELETE FROM night_shift_settings`), "no physical delete")
		mustExec(t, s, `UPDATE night_shift_settings SET enabled = 1, halted_at = 'a', halted_by = 'b', halt_reason = 'c'`)
		mustExec(t, s, `UPDATE night_shift_settings SET enabled = 0, halted_at = NULL, halted_by = NULL, halt_reason = NULL`)
	})
	t.Run("runs", func(t *testing.T) {
		const ins = `INSERT INTO night_shift_runs(id,night,state,reason,plan_json,estimate_id,batch_id,mandate_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`
		expectErr(t, exec(t, s, ins, "r0", "2026-11-22", "running", nil, nil, nil, nil, nil, m3ts, m3ts), "CHECK")
		expectErr(t, exec(t, s, ins, "r0", "2026-11-22", "planned", "", nil, nil, nil, nil, m3ts, m3ts), "CHECK")
		expectErr(t, exec(t, s, ins, "r0", "2026-11-22", "planned", nil, "not json", nil, nil, nil, m3ts, m3ts), "CHECK")
		expectErr(t, exec(t, s, ins, "r0", "2026-11-22", "created", nil, nil, nil, nil, nil, m3ts, m3ts), "CHECK") // created needs a batch
		mustExec(t, s, ins, "r1", "2026-11-22", "planned", nil, `{"a":1}`, nil, nil, nil, m3ts, m3ts)
		expectErr(t, exec(t, s, ins, "r2", "2026-11-22", "planned", nil, nil, nil, nil, nil, m3ts, m3ts), "UNIQUE") // one night per day
		expectErr(t, exec(t, s, `DELETE FROM night_shift_runs`), "no physical delete")
		for _, col := range []string{"id = 'z'", "night = '2026-01-01'", "created_at = 'z'"} {
			expectErr(t, exec(t, s, `UPDATE night_shift_runs SET `+col+` WHERE id = 'r1'`), "frozen column")
		}
		mustExec(t, s, ins, "m1x", "x", "skipped", "no_mandate", nil, nil, nil, nil, m3ts, m3ts) // satisfies nothing else; used for the transition checks below
		mustExec(t, s, `UPDATE night_shift_runs SET state = 'skipped' WHERE id = 'm1x'`)         // not a change
		expectErr(t, exec(t, s, `UPDATE night_shift_runs SET state = 'planned' WHERE id = 'm1x'`), "illegal transition")
		expectErr(t, exec(t, s, `UPDATE night_shift_runs SET state = 'failed' WHERE id = 'm1x'`), "illegal transition")
		// a batch needs its estimate and its mandate, and both are set once
		m3World(t, s)
		mustExec(t, s, mandateIns, "mn", "agent:nightshift", "t", 5, "2026-10-14T12:00:00.000Z", "2026-10-15T12:00:00.000Z", "{}", "approved", m3ts, nil, nil, nil)
		expectErr(t, exec(t, s, `UPDATE night_shift_runs SET batch_id = 'b1', mandate_id = 'mn', state = 'created' WHERE id = 'r1'`), "CHECK") // no estimate
		mustExec(t, s, `UPDATE night_shift_runs SET estimate_id = 'e1' WHERE id = 'r1'`)
		expectErr(t, exec(t, s, `UPDATE night_shift_runs SET batch_id = 'b1' WHERE id = 'r1'`), "CHECK") // batch without mandate
		mustExec(t, s, `UPDATE night_shift_runs SET batch_id = 'b1', mandate_id = 'mn', state = 'created' WHERE id = 'r1'`)
		expectErr(t, exec(t, s, `UPDATE night_shift_runs SET batch_id = 'b2' WHERE id = 'r1'`), "set once")
		expectErr(t, exec(t, s, `UPDATE night_shift_runs SET mandate_id = NULL WHERE id = 'r1'`), "set once")
		mustExec(t, s, `UPDATE night_shift_runs SET reason = 'x' WHERE id = 'r1'`)
		expectErr(t, exec(t, s, `UPDATE night_shift_runs SET state = 'skipped' WHERE id = 'r1'`), "illegal transition")
		mustExec(t, s, `UPDATE night_shift_runs SET state = 'halted' WHERE id = 'r1'`)
		expectErr(t, exec(t, s, `UPDATE night_shift_runs SET state = 'created' WHERE id = 'r1'`), "illegal transition")
		expectErr(t, exec(t, s, ins, "r3", "2026-11-23", "planned", nil, nil, "e1", "b1", "mn", m3ts, m3ts), "UNIQUE") // a batch belongs to one night
	})
}
