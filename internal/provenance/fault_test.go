package provenance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/store/faultdb"
)

// faultSteps wires the real picks and sounds steps over st without touching the database (the taxonomy seed is
// already in the template), so a sweep fails only the calls of the operation under test.
func faultSteps(o *provenance.Options) {
	ds := duels.New(duels.Options{Store: o.Store, Library: library.New(o.Store, nil, o.Now), Now: o.Now, NewID: o.NewID})
	o.Picks, o.Sounds, o.Ladder = picks.New(o.Store, ds, o.Now, o.NewID), sounds.New(o.Store, o.Now, o.NewID), ds
}

// faultLibrary builds a small library with every kind of source row (roots with lineage, a rating, a duel verdict, a
// run, a collection, a batch with a job, drafts with and without a mother or reference) and settles it with the real
// steps. When more is true it then adds a second wave of source rows, leaving the queue non-empty.
func faultLibrary(t *testing.T, more bool) string {
	t.Helper()
	w := newWorld(t)
	w.prov = w.service(w.st, realSteps(t))
	sc := w.sc
	ra := sc.Recipe("fa_v1", "english, pop, 100 bpm")
	root := sc.Track(ra, "Root A", "", sc.At(0))
	child := sc.Track(ra, "Child A", root, sc.At(2))
	other := sc.Track(sc.Recipe("fb_v1", "reggae, dub, 80 bpm"), "Root B", "", sc.At(4))
	sc.Rate(child, 5)
	sc.Rate(root, 3)
	sc.Rate(other, 4)
	grand := sc.Track(ra, "Grandchild A", child, sc.At(3))
	sc.Verdict(sc.Duel(root, child), "b")
	sc.Verdict(sc.Duel(child, grand), "a")
	sc.Verdict(sc.Duel(root, grand), "tie")
	rd := sc.Recipe("fd_v1", "english, synth pop, 120 bpm")
	sc.Ref(rd, "i.csv", "Synthetic Muse 001", "Synthetic Artist 001", "variant-a")
	d1 := sc.Draft("fd_v1", librarytest.DraftOpts{Recipe: rd})
	d2 := sc.Draft("fe_v1", librarytest.DraftOpts{})
	sc.Collection(d2, "Synthetic set")
	run := sc.Run("completed", 6)
	sc.RootInRun(sc.Recipe("ff_v1", "english, rock"), "Run root", run, sc.At(7))
	batch := sc.Batch("1", 1, "finished")
	sc.Job(batch, 0, d1, "", rd)
	w.settle()
	if more {
		r2 := sc.Recipe("fg_v1", "english, jazz, 90 bpm")
		t2 := sc.Track(r2, "Late root", "", sc.At(9))
		sc.Rate(t2, 5)
		sc.Rate(other, 2)
		sc.Draft("fh_v1", librarytest.DraftOpts{})
		sc.Fulfil(d2, t2)
	}
	return faultdb.Snapshot(t, w.st, w.path)
}

func faultService(w *world, st *store.Store) *provenance.Service {
	return w.service(st, faultSteps)
}

// sweepOp runs op over a fault sweep of the template and requires that failures surface as errors at least once.
func sweepOp(t *testing.T, template string, op func(ctx context.Context, p *provenance.Service) error) {
	t.Helper()
	sweepOpWith(t, template, faultdb.Sweep, op)
}

// sweepOpSplit is sweepOp for Verify, which reads the ladder through the reader pool while its read transaction is open.
func sweepOpSplit(t *testing.T, template string, op func(ctx context.Context, p *provenance.Service) error) {
	t.Helper()
	sweepOpWith(t, template, faultdb.SweepSplit, op)
}

func sweepOpWith(t *testing.T, template string, sweep func(testing.TB, string, func(context.Context, *store.Store) error) int, op func(ctx context.Context, p *provenance.Service) error) {
	t.Helper()
	w := &world{t: t, log: &logBuf{}, n: 1 << 20}
	n := sweep(t, template, func(ctx context.Context, st *store.Store) error {
		return op(ctx, faultService(w, st))
	})
	if n == 0 {
		t.Error("no injected failure surfaced as an error")
	}
}

func TestFault_SettleFirstPass(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("fault sweep: make test runs it")
	}
	tpl := faultLibrary(t, true)
	sweepOp(t, tpl, func(ctx context.Context, p *provenance.Service) error {
		_, err := p.Settle(ctx)
		if err != nil && !errors.Is(err, faultdb.ErrInjected) {
			t.Errorf("settle error does not wrap the cause: %v", err)
		}
		return err
	})
}

func TestFault_ReadOnlyOps(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("fault sweep: make test runs it")
	}
	tpl := faultLibrary(t, true)
	ops := map[string]func(ctx context.Context, p *provenance.Service) error{
		"Verify":        func(ctx context.Context, p *provenance.Service) error { _, err := p.Verify(ctx); return err },
		"Pending":       func(ctx context.Context, p *provenance.Service) error { _, err := p.Pending(ctx); return err },
		"PendingByKind": func(ctx context.Context, p *provenance.Service) error { _, err := p.PendingByKind(ctx); return err },
		"LeaseHeld":     func(ctx context.Context, p *provenance.Service) error { _, err := p.LeaseHeld(ctx); return err },
		"Sweep":         func(ctx context.Context, p *provenance.Service) error { return p.Sweep(ctx) },
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if name == "Verify" {
				sweepOpSplit(t, tpl, op)
				return
			}
			sweepOp(t, tpl, op)
		})
	}
}

func TestFault_VerifyOnSettledLibrary(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("fault sweep: make test runs it")
	}
	tpl := faultLibrary(t, false)
	sweepOpSplit(t, tpl, func(ctx context.Context, p *provenance.Service) error { _, err := p.Verify(ctx); return err })
}

func TestFault_SettleIncremental(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("fault sweep: make test runs it")
	}
	tpl := faultLibrary(t, true)
	sweepOp(t, tpl, func(ctx context.Context, p *provenance.Service) error {
		if _, err := p.Settle(ctx); err != nil {
			return err
		}
		_, err := p.Settle(ctx)
		return err
	})
}

// TestFault_SettleTransientAnomaly: a draft gains a mother whose track key never arrived, so the pass requeues the
// key; every database call of that path (including the requeue write) fails once.
func TestFault_SettleTransientAnomaly(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("fault sweep: make test runs it")
	}
	w := newWorld(t)
	w.prov = w.service(w.st, realSteps(t))
	sc := w.sc
	d := sc.Draft("a_v1", librarytest.DraftOpts{})
	w.settle()
	root := sc.Track(sc.Recipe("m_v1", "pop"), "Mother", "", sc.At(0))
	w.exec(`DELETE FROM provenance_dirty WHERE kind = 'track'`)
	sc.SetMother(d, root)
	tpl := faultdb.Snapshot(t, w.st, w.path)
	sweepOp(t, tpl, func(ctx context.Context, p *provenance.Service) error { _, err := p.Settle(ctx); return err })
}
