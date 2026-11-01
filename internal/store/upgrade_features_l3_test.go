package store_test

import (
	"context"
	"database/sql"
	"fmt"
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

// featStep adapts *features.Service to provenance.FeatureStep, as app does.
type featStep struct{ f *features.Service }

func (a featStep) ComputeTx(ctx context.Context, tx *sql.Tx, env store.Env, trackIDs, songIDs []string) (provenance.FeatureChanges, error) {
	c, err := a.f.ComputeTx(ctx, tx, env, trackIDs, songIDs)
	return provenance.FeatureChanges{Takes: c.Takes, Songs: c.Songs}, err
}

func (a featStep) SweepStaleTx(ctx context.Context, tx *sql.Tx, env store.Env, limit int) (int, error) {
	return a.f.SweepStaleTx(ctx, tx, env, limit)
}

type upgraded struct {
	st   *store.Store
	prov *provenance.Service
	svc  *features.Service
}

// upgradeAndSettle builds the schema-10 L2+L3 library, reopens it at the current schema (the migration re-marks every take
// and draft) and returns a Reconciler with the real picks, sounds and features steps; nothing has been drained yet.
func upgradeAndSettle(t *testing.T) *upgraded {
	t.Helper()
	ctx := context.Background()
	prev, p := openAt(t, agentsAtlasMigration-1)
	librarytest.FixtureL3Upgrade(t, prev, librarytest.FixedNow)
	prev.W.Close() // the queue the schema-10 triggers filled stays: the migration only adds its track and draft keys
	st, err := store.Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var tick, n atomic.Int64
	now := func() time.Time { return librarytest.FixedNow.Add(time.Duration(tick.Add(1)) * time.Second) }
	newID := func() string { return uuid.NewV7().String() }
	sd := sounds.New(st, now, newID)
	if err := sd.EnsureSeed(ctx); err != nil {
		t.Fatal(err)
	}
	ds := duels.New(duels.Options{Store: st, Library: library.New(st, nil, now), Now: now, NewID: newID})
	svc := features.New(st, now)
	prov := provenance.New(provenance.Options{
		Store: st, Muses: muses.New(st, now, newID), Songs: songs.New(st, now, newID), Sessions: sessions.New(st, now),
		Picks: picks.New(st, ds, now, newID), Sounds: sd, Ladder: ds, Features: featStep{svc}, Now: now, NewID: newID,
		Owner: fmt.Sprintf("upgrade-%d", n.Add(1)),
	})
	return &upgraded{st: st, prov: prov, svc: svc}
}

func (u *upgraded) settle(t *testing.T) provenance.Report {
	t.Helper()
	rep, err := u.prov.Settle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// featureHash hashes the feature tables without the timestamp.
func (u *upgraded) featureHash(t *testing.T) string {
	t.Helper()
	var out string
	for _, q := range []string{
		`SELECT COALESCE(group_concat(track_id||bpm_x100||energy_x10000||key_num||digest||COALESCE(detail_json,'')), '') FROM (SELECT * FROM take_features ORDER BY track_id)`,
		`SELECT COALESCE(group_concat(song_id||subject_id||COALESCE(subject_note,'')||bpm_x100||COALESCE(family,'')||digest), '') FROM (SELECT * FROM song_features ORDER BY song_id)`,
	} {
		var s sql.NullString
		if err := u.st.R.QueryRow(q).Scan(&s); err != nil {
			t.Fatal(err)
		}
		out += s.String + "|"
	}
	return out
}

func (u *upgraded) verifyEmpty(t *testing.T) {
	t.Helper()
	an, err := u.prov.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fa, err := u.svc.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(an)+len(fa) != 0 {
		t.Errorf("Verify: %+v %+v", an, fa)
	}
}

// TestBackfill_L3_UpgradePath: a library upgraded from schema 10 is re-marked by the migration, and one drain derives every
// take and Song row (the steady-state path is the backfill, D-L1-4); Verify (L1 and L3) is empty afterwards.
func TestBackfill_L3_UpgradePath(t *testing.T) {
	t.Parallel()
	u := upgradeAndSettle(t)
	count := func(q string) int {
		var n int
		if err := u.st.R.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(`SELECT COUNT(*) FROM take_features`) != 0 {
		t.Fatal("rows before the drain")
	}
	tracks := count(`SELECT COUNT(*) FROM tracks`)
	rep := u.settle(t)
	if rep.FeaturesComputed == 0 {
		t.Errorf("the backfill computed no feature row: %+v", rep)
	}
	if got := count(`SELECT COUNT(*) FROM take_features`); got != tracks {
		t.Errorf("take rows = %d, tracks %d", got, tracks)
	}
	if got := count(`SELECT COUNT(*) FROM song_features WHERE energy_basis = 'measured'`); got < 8 {
		t.Errorf("measured Song rows = %d, want at least the 8 of the L3 world", got)
	}
	u.verifyEmpty(t)
}

// TestBackfill_L3_Idempotent: re-marking every take and sweeping again leaves every feature row as it was.
func TestBackfill_L3_Idempotent(t *testing.T) {
	t.Parallel()
	u := upgradeAndSettle(t)
	u.settle(t)
	h := u.featureHash(t)
	if _, err := u.st.W.Exec(`INSERT INTO provenance_dirty(kind, id, at) SELECT 'track', id, '2026-10-06T12:00:00.000Z' FROM tracks WHERE 1 ON CONFLICT(kind, id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if rep := u.settle(t); rep.KeysProcessed == 0 || u.featureHash(t) != h {
		t.Errorf("re-marking every take changed rows (processed %d)", rep.KeysProcessed)
	}
	if err := u.prov.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u.featureHash(t) != h {
		t.Error("the sweep changed a settled library")
	}
}
