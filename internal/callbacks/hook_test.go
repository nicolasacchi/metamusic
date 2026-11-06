package callbacks_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestAfterTakeTx_CreatesCallbacks: a Song PR child moves the addressed comments open -> addressed (as system:job), gets one fix
// callback per patch comment and one keep callback per open non-hidden keep comment of the base take; a hidden fix and a resolved
// comment that no patch names are left alone; a repeat changes nothing.
func TestAfterTakeTx_CreatesCallbacks(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	g := r.info.Lineage
	patch := r.stubPatch(g[0], r.comment("fix_chorus"), r.comment("fix_verse"))
	ti := r.stubJob(patch, g[1], "")
	if err := r.hook(ti); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"fix_chorus", "fix_verse"} {
		if got := r.state(r.comment(k)); got != "addressed" {
			t.Errorf("%s state %s, want addressed", k, got)
		}
		if by := r.str(`SELECT state_changed_by FROM comments WHERE id = ?`, r.comment(k)); by != "system:job" {
			t.Errorf("%s changed by %s", k, by)
		}
	}
	for _, k := range []string{"hidden_fix", "note", "keep"} {
		if got := r.state(r.comment(k)); got != "open" {
			t.Errorf("%s state %s, want open", k, got)
		}
	}
	if got := r.state(r.comment("resolved_fix")); got != "resolved" {
		t.Errorf("resolved_fix state %s", got)
	}
	if n := r.count(`SELECT COUNT(*) FROM callbacks WHERE child_track_id = ? AND kind = 'fix'`, g[1]); n != 2 {
		t.Errorf("fix callbacks %d, want 2", n)
	}
	if n := r.count(`SELECT COUNT(*) FROM callbacks WHERE child_track_id = ? AND kind = 'keep' AND comment_id = ? AND source_track_id = ?`, g[1], r.comment("keep"), g[0]); n != 1 {
		t.Errorf("keep callbacks %d, want 1 for the open keep comment", n)
	}
	if n := r.count(`SELECT COUNT(*) FROM callbacks WHERE job_id = ? AND patch_id = ?`, ti.JobID, patch); n != 3 {
		t.Errorf("callbacks carry job and patch: %d of 3", n)
	}
	// idempotent: a repeat (a retried ingest) creates nothing new
	if err := r.hook(ti); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT COUNT(*) FROM callbacks`); n != 3 {
		t.Errorf("after a repeat %d callbacks, want 3", n)
	}
}

// TestAfterTakeTx_NotAPatchTake: a take with no patch costs nothing (no statement, no error), also with a zero service.
func TestAfterTakeTx_NotAPatchTake(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	if err := r.hook(jobs.TakeInfo{TrackID: r.info.Lineage[1], JobID: "j", BatchID: "b", Fence: r.fence}); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT COUNT(*) FROM callbacks`); n != 0 {
		t.Errorf("%d callbacks for a take without a patch", n)
	}
}

// TestAfterTakeTx_AddressedAndResolvedKeepState: a comment that is already addressed (by another patch) or resolved still gets
// its callback, and its state is not touched; a regressed comment is addressed again.
func TestAfterTakeTx_AddressedAndResolvedKeepState(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	g := r.info.Lineage
	r.exec(`UPDATE comments SET state = 'addressed', state_changed_at = ?, state_changed_by = 'system:job' WHERE id = ?`, store.FormatTime(r.now), r.comment("fix_verse"))
	r.exec(`UPDATE comments SET state = 'regressed', state_changed_at = ?, state_changed_by = 'human:x' WHERE id = ?`, store.FormatTime(r.now), r.comment("fix_chorus"))
	patch := r.stubPatch(g[0], r.comment("fix_chorus"), r.comment("fix_verse"), r.comment("resolved_fix"))
	if err := r.hook(r.stubJob(patch, g[1], "")); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"fix_chorus": "addressed", "fix_verse": "addressed", "resolved_fix": "resolved"}
	for k, w := range want {
		if got := r.state(r.comment(k)); got != w {
			t.Errorf("%s: %s, want %s", k, got, w)
		}
	}
	if n := r.count(`SELECT COUNT(*) FROM callbacks WHERE kind = 'fix'`); n != 3 {
		t.Errorf("%d fix callbacks, want 3", n)
	}
}

// TestAfterTakeTx_LostLease: a runner that lost its lease cannot write callbacks or move comments.
func TestAfterTakeTx_LostLease(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	g := r.info.Lineage
	patch := r.stubPatch(g[0], r.comment("fix_chorus"))
	ti := r.stubJob(patch, g[1], "")
	ti.Fence = store.LeaseFence{Owner: "someone-else", Now: r.now}
	if err := r.hook(ti); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
	if n := r.count(`SELECT COUNT(*) FROM callbacks`); n != 0 {
		t.Errorf("%d callbacks written without the lease", n)
	}
	if got := r.state(r.comment("fix_chorus")); got != "open" {
		t.Errorf("state %s", got)
	}
}

// TestAfterTakeTx_FaultMatrix: whichever statement of the hook fails, the take transaction's rollback leaves no callback and no
// moved comment (no child without callbacks, no callbacks without a child): every write is a statement inside the caller's
// transaction, and a failure surfaces as an error the bridge turns into a rollback.
func TestAfterTakeTx_FaultMatrix(t *testing.T) {
	t.Parallel()
	faults := map[string]string{
		"first callback insert":       `CREATE TRIGGER f BEFORE INSERT ON callbacks BEGIN SELECT RAISE(ABORT, 'fault'); END`,
		"second callback insert":      `CREATE TRIGGER f BEFORE INSERT ON callbacks WHEN (SELECT COUNT(*) FROM callbacks) >= 1 BEGIN SELECT RAISE(ABORT, 'fault'); END`,
		"last callback insert (keep)": `CREATE TRIGGER f BEFORE INSERT ON callbacks WHEN NEW.kind = 'keep' BEGIN SELECT RAISE(ABORT, 'fault'); END`,
		"comment move":                `CREATE TRIGGER f BEFORE UPDATE OF state ON comments BEGIN SELECT RAISE(ABORT, 'fault'); END`,
	}
	for name, ddl := range faults {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, nil)
			g := r.info.Lineage
			patch := r.stubPatch(g[0], r.comment("fix_chorus"), r.comment("fix_verse"))
			ti := r.stubJob(patch, g[1], "")
			r.exec(ddl)
			if err := r.hook(ti); err == nil {
				t.Fatal("the injected fault did not surface")
			}
			if n := r.count(`SELECT COUNT(*) FROM callbacks`); n != 0 {
				t.Errorf("%d callbacks survived a failed hook", n)
			}
			for _, k := range []string{"fix_chorus", "fix_verse"} {
				if got := r.state(r.comment(k)); got != "open" {
					t.Errorf("%s moved to %s by a failed hook", k, got)
				}
			}
		})
	}
	t.Run("unknown patch", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, nil)
		ghost := "ghost-patch"
		ti := jobs.TakeInfo{TrackID: r.info.Lineage[1], JobID: "j", BatchID: "b", PatchID: &ghost, Fence: r.fence}
		if err := r.hook(ti); err == nil || !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("err = %v, want sql.ErrNoRows", err)
		}
	})
	t.Run("unconfigured service", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, nil)
		r.svc = newZero()
		patch := "p"
		tx, _ := r.st.W.BeginTx(context.Background(), nil)
		defer func() { _ = tx.Rollback() }()
		if err := r.svc.AfterTakeTx(context.Background(), tx, jobs.TakeInfo{PatchID: &patch}); !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestAfterTakeCommit_Notifies: one count-only notification when the take is the last of its batch to arrive; none while a sibling
// still runs, none for a take without a patch or without callbacks; a failing notifier is only a warning.
func TestAfterTakeCommit_Notifies(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	g := r.info.Lineage
	patch := r.stubPatch(g[0], r.comment("fix_chorus"), r.comment("fix_verse"))
	ti := r.stubJob(patch, g[1], "")
	sibling := r.stubJobInBatch(patch, ti, "queued")
	if err := r.hook(ti); err != nil {
		t.Fatal(err)
	}
	r.svc.AfterTakeCommit(context.Background(), ti)
	if len(r.note.msgs) != 0 {
		t.Fatalf("notified while a sibling still runs: %+v", r.note.msgs)
	}
	r.setJobState(sibling, "cancelled")
	r.svc.AfterTakeCommit(context.Background(), ti)
	if len(r.note.msgs) != 1 {
		t.Fatalf("%d notifications, want 1", len(r.note.msgs))
	}
	m := r.note.msgs[0]
	if m.Body != "3 callbacks ready" || m.Click != "https://mm.example.com/judge/callbacks" || strings.Contains(m.Click, "?") {
		t.Errorf("message %+v", m)
	}
	// nothing of the comments, titles or lyrics may travel
	all := m.Title + m.Body + m.Click
	for _, a := range m.Actions {
		all += a.Label + a.URL
	}
	for _, secret := range []string{"chorus lands late", "rushed", "Synthetic Lineage", "fixture line", "fixture hook"} {
		if strings.Contains(all, secret) {
			t.Errorf("notification leaks %q", secret)
		}
	}
	// no patch: silent
	r.svc.AfterTakeCommit(context.Background(), jobs.TakeInfo{TrackID: g[1], JobID: "j", BatchID: ti.BatchID})
	if len(r.note.msgs) != 1 {
		t.Error("a take without a patch notified")
	}
	// a failing notifier does not panic or fail anything
	r.note.err = errors.New("ntfy down https://ntfy.example.com/topic?token=secret")
	r.svc.AfterTakeCommit(context.Background(), ti)
	if len(r.note.msgs) != 2 {
		t.Errorf("notifier called %d times", len(r.note.msgs))
	}
}
