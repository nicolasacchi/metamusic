package provenance_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// l1World is a library holding librarytest.FixtureL1 with a Reconciler over it.
func l1World(t *testing.T, mod ...func(*provenance.Options)) *world {
	t.Helper()
	w := newWorld(t, mod...)
	librarytest.FixtureL1(t, w.st, librarytest.FixedNow)
	return w
}

func TestReconciler_PassIdempotent(t *testing.T) {
	t.Parallel()
	w := l1World(t)
	first := w.settle()
	if first.SongsCreated == 0 || first.Passes == 0 {
		t.Fatalf("first Settle did nothing: %+v", first)
	}
	h1 := w.contentHash()
	second := w.settle()
	if second.Passes != 0 || second.KeysProcessed != 0 {
		t.Errorf("the second Settle found work: %+v", second)
	}
	if h2 := w.contentHash(); h1 != h2 {
		t.Errorf("a second Settle changed the L1 tables")
	}
	// re-marking every key and settling again yields the same rows (the passes are idempotent, C95.2)
	w.exec(`UPDATE tracks SET status = status`) // no-op value, but the AFTER UPDATE OF status trigger fires
	w.exec(`UPDATE recipe_drafts SET label = label`)
	third := w.settle()
	if third.KeysProcessed == 0 {
		t.Errorf("the re-marked keys were not processed")
	}
	if third.SongsCreated+third.SongsAttached+third.SongsRetired+third.MusesCreated+third.SessionsCreated+third.MembersAdded != 0 {
		t.Errorf("a redo wrote new rows: %+v", third)
	}
	if h3 := w.contentHash(); h1 != h3 {
		t.Errorf("a redo changed the L1 tables")
	}
	w.verifyEmpty()
}

// remarkStep re-marks a queue key from inside phase B, before the dequeue (the situation of a writer committing
// between the read and the delete).
type remarkStep struct {
	id   string
	done bool
}

func (r *remarkStep) RecomputeTx(ctx context.Context, tx *sql.Tx, _ store.Env, _ []string, _ map[string]ladder.Strength) (picks.Changes, error) {
	if !r.done {
		r.done = true
		if _, err := tx.ExecContext(ctx, `UPDATE provenance_dirty SET at = '9999-01-01T00:00:00.000Z' WHERE kind = 'track' AND id = ?`, r.id); err != nil {
			return picks.Changes{}, err
		}
	}
	return picks.Changes{}, nil
}

func TestReconciler_RemarkedKeySurvives(t *testing.T) {
	t.Parallel()
	step := &remarkStep{}
	w := newWorld(t, func(o *provenance.Options) { o.Picks = step })
	r := w.sc.Recipe("a_v1", "pop")
	step.id = w.sc.Track(r, "T", "", w.sc.At(0))
	rep := w.settle()
	if rep.Passes != 2 {
		t.Errorf("passes = %d, want 2: the re-marked key must survive pass 1 and be processed by pass 2", rep.Passes)
	}
	if p, _ := w.prov.Pending(context.Background()); p != 0 {
		t.Errorf("queue = %d after Settle", p)
	}
}

func TestReconciler_CrashBetweenPhases(t *testing.T) {
	t.Parallel()
	crash := true
	w := l1World(t, func(o *provenance.Options) {
		o.FaultAfterPhaseA = func() error {
			if crash {
				crash = false
				return errors.New("injected crash after phase A")
			}
			return nil
		}
	})
	pending, _ := w.prov.Pending(context.Background())
	if _, err := w.prov.Settle(context.Background()); err == nil {
		t.Fatal("Settle must return the injected error")
	}
	if p, _ := w.prov.Pending(context.Background()); p != pending {
		t.Errorf("queue %d -> %d: phase B did not run, so no key may be dequeued", pending, p)
	}
	if w.count(`SELECT COUNT(*) FROM songs`) == 0 {
		t.Errorf("phase A committed: the Songs must exist")
	}
	w.settle() // converges
	if p, _ := w.prov.Pending(context.Background()); p != 0 {
		t.Errorf("queue = %d after the second Settle", p)
	}
	w.verifyEmpty()
}

func TestReconciler_BatchBoundary(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for i := range 250 {
		w.sc.Draft(fmt.Sprintf("b_%03d_v1", i), librarytest.DraftOpts{})
	}
	rep := w.settle()
	if rep.Passes != 3 || rep.KeysProcessed != 250 {
		t.Errorf("250 dirty drafts: %d passes, %d keys; want 3 passes (BatchKeys = %d)", rep.Passes, rep.KeysProcessed, picks.BatchKeys)
	}
	if w.count(`SELECT COUNT(*) FROM songs`) != 250 {
		t.Errorf("songs = %d", w.count(`SELECT COUNT(*) FROM songs`))
	}
	w.verifyEmpty()
}

func TestReconciler_NoPassCap(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("1 000 keys: make test and CI run it")
	}
	w := newWorld(t)
	for i := range 1000 {
		w.sc.Draft(fmt.Sprintf("c_%04d_v1", i), librarytest.DraftOpts{})
	}
	rep := w.settle()
	if rep.Passes != 10 || rep.KeysProcessed != 1000 {
		t.Errorf("1 000 keys: %d passes, %d keys; want 10 passes in one call", rep.Passes, rep.KeysProcessed)
	}
	if p, _ := w.prov.Pending(context.Background()); p != 0 {
		t.Errorf("%d keys left", p)
	}
}

// TestReconciler_DraftBeforeTrackAcrossBatches: with 150 tracks and 150 drafts and BatchKeys 100 the selection is
// kind-rank first, so no draft is reconciled before the track key of its lineage lands in a later pass.
func TestReconciler_DraftBeforeTrackAcrossBatches(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	var drafts []string
	for i := range 150 { // the drafts are older than every track by `at`
		drafts = append(drafts, sc.Draft(fmt.Sprintf("d_%03d_v1", i), librarytest.DraftOpts{}))
	}
	var roots, children []string
	for i := range 150 {
		r := sc.Recipe(fmt.Sprintf("t_%03d_v1", i), "pop")
		root := sc.Track(r, fmt.Sprintf("T%03d", i), "", sc.At(0))
		roots = append(roots, root)
		if i%3 == 0 {
			children = append(children, sc.Track(r, fmt.Sprintf("C%03d", i), root, sc.At(2)))
		}
	}
	for i := 0; i < 50; i++ { // some drafts are given mothers
		sc.SetMother(drafts[i], roots[i])
	}
	for i := 50; i < 50+len(children); i++ { // some are fulfilled by child takes
		sc.Fulfil(drafts[i], children[i-50])
	}
	rep := w.settle()
	for _, a := range rep.Anomalies {
		t.Errorf("anomaly %+v: a draft was reconciled before its track", a)
	}
	w.verifyEmpty()
	if n := w.count(`SELECT COUNT(*) FROM songs s JOIN recipe_drafts d ON d.id = s.draft_id WHERE s.retired_at IS NULL AND d.parent_track_id IS NOT NULL`); n != 0 {
		t.Errorf("%d live draft Songs for mothered drafts", n)
	}
}

func TestReconciler_TransientAnomalyRequeues(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	sc := w.sc
	d := sc.Draft("a_v1", librarytest.DraftOpts{})
	w.settle() // the draft Song exists
	rr := sc.Recipe("m_v1", "pop")
	root := sc.Track(rr, "Mother", "", sc.At(0))
	w.exec(`DELETE FROM provenance_dirty WHERE kind = 'track'`) // the mother's track key never arrives (yet)
	sc.SetMother(d, root)
	rep := w.settle()
	if rep.KeysRequeued != 1 || len(rep.Anomalies) == 0 || rep.Anomalies[0].Code != "song_missing_for_root" || !rep.Anomalies[0].Transient {
		t.Fatalf("first Settle: %+v", rep)
	}
	if got := w.str(`SELECT tries FROM provenance_dirty WHERE kind = 'draft' AND id = ?`, d); got != "1" {
		t.Errorf("tries = %q, want 1", got)
	}
	// the track arrives: the next Settle converges
	w.exec(`UPDATE tracks SET status = 'final' WHERE id = ?`, root)
	rep = w.settle()
	if p, _ := w.prov.Pending(context.Background()); p != 0 || w.str(`SELECT retired_at FROM songs WHERE draft_id = ?`, d) == "" {
		t.Errorf("after the track arrived: queue %d, report %+v", p, rep)
	}
	w.verifyEmpty()

	// without the track the key is dropped at MaxQueueTries and the anomaly stays visible to Verify
	w2 := newWorld(t)
	sc2 := w2.sc
	d2 := sc2.Draft("a_v1", librarytest.DraftOpts{})
	w2.settle()
	root2 := sc2.Track(sc2.Recipe("m_v1", "pop"), "Mother", "", sc2.At(0))
	w2.exec(`DELETE FROM provenance_dirty WHERE kind = 'track'`)
	sc2.SetMother(d2, root2)
	var last provenance.Report
	for i := 1; i <= picks.MaxQueueTries; i++ {
		last = w2.settle()
		p, _ := w2.prov.Pending(context.Background())
		if i < picks.MaxQueueTries && p != 1 {
			t.Fatalf("settle %d: queue = %d, want the key still queued", i, p)
		}
	}
	if p, _ := w2.prov.Pending(context.Background()); p != 0 || last.KeysDropped != 1 || len(last.Anomalies) == 0 {
		t.Errorf("at MaxQueueTries: queue %d, report %+v", p, last)
	}
	an, err := w2.prov.Verify(context.Background())
	if err != nil || !hasCode(an, "track_without_song") || !hasCode(an, "song_for_mothered_draft") {
		t.Errorf("Verify must still report the fault: %v %v", codes(an), err)
	}
}
