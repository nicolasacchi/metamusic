package songs_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/store/faultdb"
)

var faultHuman = auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "proxy"}

func fsp(s string) *string { return &s }

// faultScenario builds every mapping the structural reconcile handles. wave is called after the first settle and
// leaves its rows unsettled, so a Settle over the template exercises the incremental paths.
func faultScenario(w *world, settleFirst bool) {
	sc := w.sc
	// a root whose recipe has no reference, a draft that will gain it as its mother
	rr := sc.Recipe("fm_v1", "pop, 100 bpm")
	mother := sc.Track(rr, "Mother", "", sc.At(0))
	rd := sc.Recipe("fd_v1", "pop")
	sc.Ref(rd, "i.csv", "Synthetic Muse 060", "Synthetic Artist 060", "variant-a")
	dMother := sc.Draft("fd_v1", librarytest.DraftOpts{Recipe: rd})
	// a draft that a job will render into a root; a draft fulfilled by a root; two drafts on one root
	dJob := sc.Draft("fj_v1", librarytest.DraftOpts{})
	dDone := sc.Draft("ff_v1", librarytest.DraftOpts{})
	dSecond := sc.Draft("fg_v1", librarytest.DraftOpts{})
	// a root with a reference Muse and no draft
	rm := sc.Recipe("fr_v1", "rock")
	sc.Ref(rm, "i.csv", "Synthetic Muse 061", "", "variant-b")
	sc.Track(rm, "Referenced", "", sc.At(3))
	if settleFirst {
		w.settle()
	}
	// wave 2 (unsettled)
	sc.SetMother(dMother, mother)
	batch := sc.Batch(`[{"item_index":0},{"item_index":1},{"item_index":1}]`, 3, "finished")
	jobRecipe := sc.Recipe("fjob_v1", "pop")
	j := sc.Job(batch, 0, dJob, "", jobRecipe)
	rootJob := sc.Track(jobRecipe, "From job", "", sc.At(5))
	sc.JobProduced(j, rootJob)
	c1 := sc.Job(batch, 1, "", "", sc.Recipe("fc1_v1", "pop"))
	c2 := sc.Job(batch, 2, "", "", sc.Recipe("fc2_v1", "pop"))
	sc.JobProduced(c1, sc.Track(sc.Recipe("fc1_v1", "pop"), "Compose a", "", sc.At(6)))
	sc.JobProduced(c2, sc.Track(sc.Recipe("fc2_v1", "pop"), "Compose b", "", sc.At(7)))
	rootDone := sc.Track(sc.Recipe("fx_v1", "pop"), "Fulfils", "", sc.At(8))
	sc.Fulfil(dDone, rootDone)
	sc.Fulfil(dSecond, rootDone)
	child := sc.Track(rr, "Child", mother, sc.At(9))
	sc.Draft("fh_v1", librarytest.DraftOpts{Fulfilled: child})
	sc.Draft("fi_v1", librarytest.DraftOpts{Parent: mother})
}

func faultProv(st *store.Store) *provenance.Service {
	var n atomic.Int64
	clock := func() time.Time { return librarytest.FixedNow }
	return provenance.New(provenance.Options{Store: st, Now: clock, NewID: func() string {
		return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xa00000+n.Add(1))
	}})
}

func TestFault_SongReconcile(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("fault sweep: make test runs it")
	}
	for name, settleFirst := range map[string]bool{"first pass": false, "incremental": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			faultScenario(w, settleFirst)
			tpl := faultdb.Snapshot(t, w.st, w.path)
			n := faultdb.Sweep(t, tpl, func(ctx context.Context, st *store.Store) error {
				_, err := faultProv(st).Settle(ctx)
				return err
			})
			if n == 0 {
				t.Error("no injected failure surfaced as an error")
			}
		})
	}
}

func TestFault_SongService(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("fault sweep: make test runs it")
	}
	w := newWorld(t)
	faultScenario(w, true)
	w.settle()
	song := w.str(`SELECT id FROM songs WHERE retired_at IS NULL ORDER BY id LIMIT 1`)
	retired := w.str(`SELECT id FROM songs WHERE retired_at IS NOT NULL LIMIT 1`)
	muse := w.str(`SELECT id FROM muses LIMIT 1`)
	rev := w.count(`SELECT revision FROM songs WHERE id = ?`, song)
	tpl := faultdb.Snapshot(t, w.st, w.path)
	svc := func(st *store.Store) *songs.Service {
		var n atomic.Int64
		return songs.New(st, func() time.Time { return librarytest.FixedNow }, func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xb00000+n.Add(1)) })
	}
	yes := true
	ops := map[string]func(ctx context.Context, st *store.Store) error{
		"Get": func(ctx context.Context, st *store.Store) error { _, err := svc(st).Get(ctx, song); return err },
		"Core": func(ctx context.Context, st *store.Store) error {
			if _, err := svc(st).Core(ctx, song); err != nil {
				return err
			}
			if retired != "" {
				_, err := svc(st).Core(ctx, retired)
				return err
			}
			return nil
		},
		"List": func(ctx context.Context, st *store.Store) error {
			if _, err := svc(st).List(ctx, songs.ListFilter{Limit: 2, IncludeHidden: true}); err != nil {
				return err
			}
			_, err := svc(st).List(ctx, songs.ListFilter{Q: "Synthetic", NoMuse: true, Sounds: map[string][]string{"genre_family": {"pop"}}, HasPick: &yes})
			return err
		},
		"Update": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Update(ctx, faultHuman, song, songs.Patch{Revision: rev, Title: fsp("Renamed"), Angle: songs.OptString{Set: true, Value: fsp("variant-z")},
				MuseID: songs.OptString{Set: true, Value: &muse}, Hidden: &yes})
			return err
		},
		"Update clearing": func(ctx context.Context, st *store.Store) error {
			no := false
			_, err := svc(st).Update(ctx, faultHuman, song, songs.Patch{Revision: rev, Angle: songs.OptString{Set: true}, MuseID: songs.OptString{Set: true}, Hidden: &no})
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			faultdb.Sweep(t, tpl, op)
		})
	}
}
