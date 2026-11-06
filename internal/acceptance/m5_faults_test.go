package acceptance_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestM5_EveryDatabaseCallMayFail: a database call that fails anywhere in the take transaction of a Song PR child, or right after
// it, leaves the library consistent (C264): no child without its callbacks, no callbacks without their child, no comment moved to
// addressed by a take that does not exist. Each fault is a trigger that aborts one kind of statement; the job runs to its end on
// the real runner and the real ingest; afterwards the same patch renders again on a healthy database and the loop completes (the
// failed render used up one of the patch's three renders, nothing else). The per-statement matrix of the hook itself, driven
// one statement at a time, is callbacks.TestAfterTakeTx_FaultMatrix; this test is the same promise through the real ingest.
func TestM5_EveryDatabaseCallMayFail(t *testing.T) {
	faults := []struct {
		name, ddl  string
		rolledBack bool // the take must be absent (the fault is inside the take transaction)
	}{
		{"first callback insert", `CREATE TRIGGER fault BEFORE INSERT ON callbacks BEGIN SELECT RAISE(ABORT, 'injected'); END`, true},
		{"keep callback insert", `CREATE TRIGGER fault BEFORE INSERT ON callbacks WHEN NEW.kind = 'keep' BEGIN SELECT RAISE(ABORT, 'injected'); END`, true},
		{"fix callback insert", `CREATE TRIGGER fault BEFORE INSERT ON callbacks WHEN NEW.kind = 'fix' BEGIN SELECT RAISE(ABORT, 'injected'); END`, true},
		{"comment move to addressed", `CREATE TRIGGER fault BEFORE UPDATE OF state ON comments BEGIN SELECT RAISE(ABORT, 'injected'); END`, true},
		{"take row", `CREATE TRIGGER fault BEFORE INSERT ON tracks BEGIN SELECT RAISE(ABORT, 'injected'); END`, true},
		{"mutation row", `CREATE TRIGGER fault BEFORE INSERT ON mutations BEGIN SELECT RAISE(ABORT, 'injected'); END`, true},
		{"audio analysis row", `CREATE TRIGGER fault BEFORE INSERT ON analyses BEGIN SELECT RAISE(ABORT, 'injected'); END`, true},
	}
	for _, f := range faults {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			l := newM5Loop(t, servertest.Options{})
			fix := l.humanComment(t, l.mother, "fix", "the hook arrives too late", 1.5)
			keep := l.humanComment(t, l.mother, "keep", "the verse is perfect", 0.5)
			pid := l.proposeChorus(t, []string{fix}, "the hook arrives right on time")["id"].(string)
			children0 := count(t, l.Rig, `SELECT COUNT(*) FROM tracks WHERE parent_track_id = ?`, l.mother)

			exec := func(q string) {
				t.Helper()
				if _, err := l.Store.W.Exec(q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			consistent := func(when string) (children, callbacks int) {
				t.Helper()
				children = count(t, l.Rig, `SELECT COUNT(*) FROM tracks WHERE parent_track_id = ?`, l.mother) - children0
				callbacks = count(t, l.Rig, `SELECT COUNT(*) FROM callbacks`)
				orphans := count(t, l.Rig, `SELECT COUNT(*) FROM callbacks c WHERE NOT EXISTS (SELECT 1 FROM tracks t WHERE t.id = c.child_track_id)`)
				childless := count(t, l.Rig, `SELECT COUNT(*) FROM tracks t WHERE t.parent_track_id = ? AND t.origin = 'job'
					AND NOT EXISTS (SELECT 1 FROM callbacks c WHERE c.child_track_id = t.id)`, l.mother)
				if orphans != 0 || childless != 0 {
					t.Fatalf("%s: %d callbacks without a child, %d Song PR children without callbacks", when, orphans, childless)
				}
				if st, _ := l.commentState(t, fix); (st == "addressed") != (children > 0) {
					t.Fatalf("%s: the fix comment is %s with %d children", when, st, children)
				}
				if st, _ := l.commentState(t, keep); st != "open" {
					t.Fatalf("%s: the keep comment is %s", when, st)
				}
				return children, callbacks
			}

			// the fault is armed, the render runs on the real runner and ingest to the job's end
			exec(f.ddl)
			est := obj(l.h.OK(t, "POST", "/api/generations/estimate", map[string]any{"items": []any{map[string]any{"mode": "song_pr", "patch_id": pid, "n_seeds": 1}}}))
			resp := l.h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "fault"}, "Idempotency-Key", "fault-1")
			if resp.Status != 201 {
				t.Fatalf("create: %d %s", resp.Status, resp.Body)
			}
			job := obj(arr(obj(resp.JSON(t))["jobs"])[0])["id"].(string)
			pump(t, l.Rig, func() bool { s, _ := jobState(t, l.Rig, job); return terminalJob(s) })
			children, callbacks := consistent("after the faulted render")
			if f.rolledBack && (children != 0 || callbacks != 0) {
				t.Errorf("the fault is inside the take transaction, yet %d children and %d callbacks exist", children, callbacks)
			}
			if s, code := jobState(t, l.Rig, job); f.rolledBack && s != "failed" {
				t.Errorf("job %s (%s) after a rolled-back take, want failed", s, code)
			}

			// healthy again: the same Song PR renders and the loop is whole (child, fix + keep callbacks, comment addressed)
			exec(`DROP TRIGGER fault`)
			_, kids := l.renderHuman(t, pid, 1)
			if len(kids) != 1 {
				t.Fatalf("children %v", kids)
			}
			children, callbacks = consistent("after the healthy render")
			if callbacks != 2 || children < 1 {
				t.Errorf("after the healthy render: %d children, %d callbacks, want a child with 2 callbacks", children, callbacks)
			}
			if st, by := l.commentState(t, fix); st != "addressed" || by != "system:job" {
				t.Errorf("fix comment %s by %s after the healthy render", st, by)
			}
		})
	}
}
