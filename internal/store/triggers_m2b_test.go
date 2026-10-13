package store

import "testing"

func TestDuelsTriggers(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	m2bTracks(t, s)
	const duelQ = `INSERT INTO duels(id,slot_a_track_id,slot_b_track_id,swapped,blind,from_ms,to_ms,clip_def,tool_version,match,gain_a_cdb,gain_b_cdb,created_by,created_by_kind,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,'mm-clip/1','ffmpeg x','off',0,0,'human:h',?,?,?)`
	mustExec(t, s, duelQ, "d1", "t1", "t2", 0, 1, 0, 1000, "human", ts, ts)
	mustExec(t, s, duelQ, "d2", "t1", "t2", 1, 1, 0, 1000, "agent", ts, ts)
	expectErr(t, exec(t, s, duelQ, "d3", "t1", "t2", 1, 0, 0, 1000, "human", ts, ts), "CHECK")  // swapped without blind
	expectErr(t, exec(t, s, duelQ, "d3", "t1", "t2", 0, 1, 500, 500, "human", ts, ts), "CHECK") // to_ms = from_ms
	expectErr(t, exec(t, s, duelQ, "d3", "t1", "t1", 0, 1, 0, 1000, "human", ts, ts), "CHECK")  // same track
	expectErr(t, exec(t, s, duelQ, "d3", "t1", "t2", 0, 1, 0, 1000, "system", ts, ts), "CHECK") // creator kind
	expectErr(t, exec(t, s, `UPDATE duels SET match='x'`), "duels: immutable")
	expectErr(t, exec(t, s, `DELETE FROM duels`), "no physical delete")

	const vQ = `INSERT INTO duel_verdicts(id,duel_id,source,principal_kind,principal_id,verdict,winner_track_id,note,saw_reveal,created_at)
		VALUES(?,?,?,?,?,?,?,?,1,?)`
	expectErr(t, exec(t, s, vQ, "v0", "d1", "human", "human", "human:op", "a", "t2", nil, ts), "winner does not match slot")
	// a winner-less 'a' is refused by the BEFORE INSERT trigger, which runs ahead of the table CHECK
	expectErr(t, exec(t, s, vQ, "v0", "d1", "human", "human", "human:op", "a", nil, nil, ts), "winner does not match slot")
	expectErr(t, exec(t, s, vQ, "v0", "d1", "human", "human", "human:op", "tie", "t1", nil, ts), "CHECK")
	for _, k := range []string{"system", "anonymous", "x"} {
		expectErr(t, exec(t, s, vQ, "v0", "d1", "critic", k, "p", "tie", nil, nil, ts), "CHECK")
	}
	expectErr(t, exec(t, s, vQ, "v0", "d1", "human", "agent", "agent:t1", "tie", nil, nil, ts), "CHECK")
	expectErr(t, exec(t, s, vQ, "v0", "d1", "critic", "human", "human:op", "tie", nil, nil, ts), "CHECK")
	expectErr(t, exec(t, s, vQ, "v0", "d1", "critic", "device", "device:d1", "tie", nil, nil, ts), "CHECK")
	expectErr(t, exec(t, s, vQ, "v0", "d1", "human", "human", "human:op", "tie", nil, "", ts), "CHECK")

	mustExec(t, s, vQ, "v1", "d1", "human", "human", "human:op", "a", "t1", "note", ts)
	expectErr(t, exec(t, s, vQ, "v2", "d1", "human", "device", "device:d1", "b", "t2", nil, ts), "UNIQUE")
	mustExec(t, s, vQ, "v3", "d1", "critic", "agent", "agent:t1", "both_bad", nil, nil, ts)
	mustExec(t, s, vQ, "v4", "d1", "critic", "agent", "agent:t2", "b", "t2", nil, ts)
	expectErr(t, exec(t, s, vQ, "v5", "d1", "critic", "agent", "agent:t1", "tie", nil, nil, ts), "UNIQUE")
	// the same critic may judge another duel; the winner follows the slot (d2 is not swapped in the table: slot b = t2)
	mustExec(t, s, vQ, "v6", "d2", "critic", "agent", "agent:t1", "b", "t2", nil, ts)

	expectErr(t, exec(t, s, `UPDATE duel_verdicts SET note='x'`), "duel_verdicts: immutable")
	expectErr(t, exec(t, s, `DELETE FROM duel_verdicts`), "no physical delete")
}
