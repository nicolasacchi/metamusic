package store

import (
	"strings"
	"testing"
)

const (
	l3SeqQ = `INSERT INTO record_sequences(id,record_id,record_revision,strategy,first_song_id,last_song_id,order_before_json,order_after_json,exact,cost_input,cost_output,
		coverage_json,digest,explanation_json,actor,actor_kind,actor_name,at) VALUES(?,?,?,?,?,?,?,?,1,?,?,'{}',?,'{}','agent:a','agent','a',?)`
	l3PropQ = `INSERT INTO taxonomy_proposals(id,state,base_version,rationale,mappings_json,mappings_digest,impact_json,principal_id,principal_kind,principal_name,created_at)
		VALUES(?,'open',1,'why',?,?,'{}',?,'agent','a',?)`
)

func l3Seq(t *testing.T, s *Store, id, rec, before, after string, costIn, costOut int) error {
	t.Helper()
	return exec(t, s, l3SeqQ, id, rec, 1, "arc", nil, nil, before, after, costIn, costOut, strings.Repeat("d", 64), l1ts)
}

func l3Prop(t *testing.T, s *Store, id, mappings, digest, principal string) error {
	t.Helper()
	return exec(t, s, l3PropQ, id, mappings, strings.Repeat(digest, 64), principal, l1ts)
}

// TestTriggers_L3 is the C134 trigger and CHECK table for record_sequences and taxonomy_proposals.
func TestTriggers_L3(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	l1World(t, s)
	l2Record(t, s, "r1")

	// ---- record_sequences ----
	if err := l3Seq(t, s, "q1", "r1", `["a","b","c"]`, `["c","a","b"]`, 10, 8); err != nil {
		t.Fatal(err)
	}
	// there is no cost CHECK: an endpoint-constrained output may cost more than the input
	if err := l3Seq(t, s, "q2", "r1", `["a","b"]`, `["b","a"]`, 5, 90); err != nil {
		t.Fatalf("output costing more than the input must be legal: %v", err)
	}
	for _, c := range []struct{ name, q, want string }{
		{"update is append-only", `UPDATE record_sequences SET strategy = 'ramp_up' WHERE id = 'q1'`, "append-only"},
		{"delete is append-only", `DELETE FROM record_sequences WHERE id = 'q1'`, "append-only"},
		{"delete all", `DELETE FROM record_sequences`, "append-only"},
	} {
		expectErr(t, exec(t, s, c.q), c.want)
	}
	expectErr(t, l3Seq(t, s, "q3", "r1", `["a","b"]`, `["a"]`, 1, 1), "CHECK")                                                       // length mismatch
	expectErr(t, l3Seq(t, s, "q4", "r1", `{}`, `{}`, 1, 1), "CHECK")                                                                 // not arrays
	expectErr(t, l3Seq(t, s, "q5", "r1", `["a"]`, `["a"]`, -1, 1), "CHECK")                                                          // negative cost
	expectErr(t, l3Seq(t, s, "q6", "nope", `["a"]`, `["a"]`, 1, 1), "FOREIGN KEY")                                                   // unknown record
	expectErr(t, exec(t, s, l3SeqQ, "q7", "r1", 0, "arc", nil, nil, `["a"]`, `["a"]`, 1, 1, strings.Repeat("d", 64), l1ts), "CHECK") // revision >= 1
	expectErr(t, exec(t, s, l3SeqQ, "q8", "r1", 1, "", nil, nil, `["a"]`, `["a"]`, 1, 1, strings.Repeat("d", 64), l1ts), "CHECK")    // strategy length
	expectErr(t, exec(t, s, l3SeqQ, "q9", "r1", 1, "arc", "nope", nil, `["a"]`, `["a"]`, 1, 1, strings.Repeat("d", 64), l1ts), "FOREIGN KEY")
	// the strategy is validated in Go, not by the table: a future name is a row, not a rebuild (D-L3-40)
	if err := exec(t, s, l3SeqQ, "q10", "r1", 1, "future_name", "s1", "s2", `["a"]`, `["a"]`, 1, 1, strings.Repeat("d", 64), l1ts); err != nil {
		t.Fatalf("a new strategy name must not need a migration: %v", err)
	}

	// ---- taxonomy_proposals ----
	m1 := `[{"term":"skank","facet":"genre_family","value":"reggae","op":"add"}]`
	if err := l3Prop(t, s, "p1", m1, "a", "agent:a1"); err != nil {
		t.Fatal(err)
	}
	// one open duplicate per principal; another principal or another set may repeat
	expectErr(t, l3Prop(t, s, "p2", m1, "a", "agent:a1"), "UNIQUE")
	if err := l3Prop(t, s, "p3", m1, "a", "agent:a2"); err != nil {
		t.Fatal(err)
	}
	if err := l3Prop(t, s, "p4", m1, "b", "agent:a1"); err != nil {
		t.Fatal(err)
	}
	expectErr(t, exec(t, s, `DELETE FROM taxonomy_proposals WHERE id = 'p1'`), "no physical delete")
	for _, col := range []string{"base_version = 2", "rationale = 'x'", "mappings_json = '[]'", "mappings_digest = 'x'", "impact_json = '{}'",
		"principal_id = 'x'", "principal_kind = 'human'", "principal_name = 'x'", "created_at = 'x'", "id = 'x'"} {
		expectErr(t, exec(t, s, `UPDATE taxonomy_proposals SET `+col+` WHERE id = 'p1'`), "frozen column")
	}
	// CHECKs
	expectErr(t, l3Prop(t, s, "c1", `[]`, "c", "agent:a1"), "CHECK")                                      // 1..50 mappings
	expectErr(t, l3Prop(t, s, "c2", `{}`, "d", "agent:a1"), "CHECK")                                      // must be an array
	expectErr(t, exec(t, s, `UPDATE taxonomy_proposals SET state = 'rejected' WHERE id = 'p1'`), "CHECK") // closed without decided_at
	expectErr(t, exec(t, s, `UPDATE taxonomy_proposals SET state = 'superseded' WHERE id = 'p1'`), "CHECK")
	expectErr(t, exec(t, s, `UPDATE taxonomy_proposals SET state='accepted', decided_at=?, decided_by='human:h' WHERE id='p1'`, l1ts), "CHECK") // no applied_version
	expectErr(t, exec(t, s, `UPDATE taxonomy_proposals SET state='accepted', decided_at=?, decided_by='human:h', applied_version=2 WHERE id='p1'`, l1ts), "CHECK")
	expectErr(t, exec(t, s, `UPDATE taxonomy_proposals SET state='rejected', decided_at=? WHERE id='p1'`, l1ts), "CHECK") // decided_by missing
	mustExec(t, s, `UPDATE taxonomy_proposals SET state='accepted', decided_at=?, decided_by='human:h', applied_version=2, accepted_json='[0]', skipped_json='[]' WHERE id='p1'`, l1ts)
	// a decided proposal is immutable, even in the decision columns
	expectErr(t, exec(t, s, `UPDATE taxonomy_proposals SET decided_note = 'later' WHERE id = 'p1'`), "decided proposals are immutable")
	expectErr(t, exec(t, s, `UPDATE taxonomy_proposals SET state='rejected' WHERE id='p1'`), "decided proposals are immutable")
	// an accepted proposal no longer blocks the same set from the same principal
	if err := l3Prop(t, s, "p5", m1, "a", "agent:a1"); err != nil {
		t.Fatalf("a decided proposal must free the duplicate slot: %v", err)
	}
	mustExec(t, s, `UPDATE taxonomy_proposals SET state='expired', decided_at=?, decided_by='system:sounds' WHERE id='p5'`, l1ts)
	mustExec(t, s, `UPDATE taxonomy_proposals SET state='rejected', decided_at=?, decided_by='human:h', decided_note='no' WHERE id='p3'`, l1ts)
}

// TestQueueTriggers_L3: pd_analyses_feat marks the take when an Ears key or energy row lands; loudness does not.
func TestQueueTriggers_L3(t *testing.T) {
	t.Parallel()
	s, _ := open(t)
	ctx := t.Context()
	count := func(id string) (n int, at string, tries int) {
		t.Helper()
		err := s.R.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(at), ''), COALESCE(MAX(tries), 0) FROM provenance_dirty WHERE kind = 'track' AND id = ?`, id).Scan(&n, &at, &tries)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	reset := func() { mustExec(t, s, `DELETE FROM provenance_dirty`) }
	const an = `INSERT INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES('t1',?,?,'A','{}',?)`
	r := insertRecipe(t, s, "q1", 1)
	l1Track(t, s, "t1", r, "")
	reset()
	for _, c := range []struct {
		analyzer string
		want     int
	}{{"loudness", 0}, {"key", 1}, {"energy", 1}, {"tempo", 1}} {
		reset()
		mustExec(t, s, an, c.analyzer, "mm-"+c.analyzer+"/1 x", ts)
		if n, _, _ := count("t1"); n != c.want {
			t.Errorf("%s analysis: %d queue rows, want %d", c.analyzer, n, c.want)
		}
	}
	// a re-mark overwrites `at` (one row per key) and resets tries
	for _, an2 := range []string{"key", "energy"} {
		reset()
		mustExec(t, s, `INSERT INTO provenance_dirty(kind,id,at,tries) VALUES('track','t1','2000-01-01T00:00:00.000Z',3)`)
		mustExec(t, s, an, an2, "mm-"+an2+"/1 y", ts)
		if n, at, tries := count("t1"); n != 1 || at == "2000-01-01T00:00:00.000Z" || tries != 0 {
			t.Errorf("%s re-mark: n=%d at=%s tries=%d", an2, n, at, tries)
		}
	}
}
