package features_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"uuid"

	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// step adapts *features.Service to provenance.FeatureStep, as app does (provenance cannot import features).
type step struct{ f *features.Service }

func (a step) ComputeTx(ctx context.Context, tx *sql.Tx, env store.Env, trackIDs, songIDs []string) (provenance.FeatureChanges, error) {
	c, err := a.f.ComputeTx(ctx, tx, env, trackIDs, songIDs)
	return provenance.FeatureChanges{Takes: c.Takes, Songs: c.Songs}, err
}

func (a step) SweepStaleTx(ctx context.Context, tx *sql.Tx, env store.Env, limit int) (int, error) {
	return a.f.SweepStaleTx(ctx, tx, env, limit)
}

// world is one library with the real Reconciler (structural phase, picks, sounds, features) and the features service.
type world struct {
	t    *testing.T
	st   *store.Store
	svc  *features.Service
	prov *provenance.Service
	sd   *sounds.Service
	tick atomic.Int64
	n    atomic.Int64
}

// now is a strictly increasing clock so a rewrite always changes computed_at.
func (w *world) now() time.Time {
	return librarytest.FixedNow.Add(time.Duration(w.tick.Add(1)) * time.Second)
}

func (w *world) newID() string { return uuid.NewV7().String() }

// newWorld opens an empty library; withFeatures false leaves the Reconciler on its no-op feature step.
func newWorld(t *testing.T, withFeatures bool) *world {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	w := &world{t: t, st: st}
	w.svc = features.New(st, w.now)
	w.sd = sounds.New(st, w.now, w.newID)
	if err := w.sd.EnsureSeed(context.Background()); err != nil {
		t.Fatal(err)
	}
	ds := duels.New(duels.Options{Store: st, Library: library.New(st, nil, w.now), Now: w.now, NewID: w.newID})
	w.prov = provenance.New(provenance.Options{
		Store: st, Muses: muses.New(st, w.now, w.newID), Songs: songs.New(st, w.now, w.newID), Sessions: sessions.New(st, w.now),
		Picks: picks.New(st, ds, w.now, w.newID), Sounds: w.sd, Ladder: ds, Now: w.now, NewID: w.newID,
		Owner: fmt.Sprintf("test-%d", w.n.Add(1)),
	})
	if withFeatures {
		w.prov.UseFeatures(step{w.svc})
	}
	return w
}

// fixtureL3 builds FixtureL3 and settles it.
func newL3World(t *testing.T) (*world, librarytest.L3Info) {
	t.Helper()
	w := newWorld(t, true)
	info := librarytest.FixtureL3(t, w.st, librarytest.FixedNow)
	w.settle()
	return w, info
}

func (w *world) settle() provenance.Report {
	w.t.Helper()
	rep, err := w.prov.Settle(context.Background())
	if err != nil {
		w.t.Fatalf("settle: %v", err)
	}
	return rep
}

func (w *world) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.st.W.Exec(q, args...); err != nil {
		w.t.Fatalf("%v\n%s", err, q)
	}
}

func (w *world) count(q string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		w.t.Fatalf("%v\n%s", err, q)
	}
	return n
}

func (w *world) str(q string, args ...any) string {
	w.t.Helper()
	var s sql.NullString
	if err := w.st.R.QueryRow(q, args...).Scan(&s); err != nil && err != sql.ErrNoRows {
		w.t.Fatalf("%v\n%s", err, q)
	}
	return s.String
}

func (w *world) verifyEmpty() {
	w.t.Helper()
	an, err := w.svc.Verify(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	if len(an) != 0 {
		w.t.Errorf("Verify: %+v", an)
	}
}

// inTx runs fn in a write transaction with a fresh Env.
func (w *world) inTx(fn func(ctx context.Context, tx *sql.Tx, env store.Env) error) {
	w.t.Helper()
	ctx := context.Background()
	tx, err := w.st.W.BeginTx(ctx, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(ctx, tx, store.Env{Now: w.now()}); err != nil {
		w.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		w.t.Fatal(err)
	}
}
