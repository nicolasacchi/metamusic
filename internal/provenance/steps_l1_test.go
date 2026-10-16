package provenance_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

// realSteps wires the real picks and sounds steps and the ladder (what app.Build and the CLI do) and seeds the
// Sound taxonomy, so a world settles and verifies the whole model rather than only its structural core.
func realSteps(t *testing.T) func(*provenance.Options) {
	return func(o *provenance.Options) {
		ds := duels.New(duels.Options{Store: o.Store, Library: library.New(o.Store, nil, o.Now), Now: o.Now, NewID: o.NewID})
		sd := sounds.New(o.Store, o.Now, o.NewID)
		if err := sd.EnsureSeed(context.Background()); err != nil {
			t.Fatal(err)
		}
		o.Picks, o.Sounds, o.Ladder = picks.New(o.Store, ds, o.Now, o.NewID), sd, ds
	}
}

// TestAcceptance_L1_MirrorWithPicksAndSounds: the backfill mirror again with the real steps: every Song with an eligible
// take has a Pick with an `initial` event, Verify (which now also checks I4 and the Sound freshness) is empty, and a
// further Settle writes nothing.
func TestAcceptance_L1_MirrorWithPicksAndSounds(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("acceptance tier: make test and CI run it")
	}
	w := newWorld(t)
	w.prov = w.service(w.st, realSteps(t))
	librarytest.FixtureL1Mirror(t, w.st, librarytest.FixedNow, mirrorParams)
	rep := w.settle()
	checkMirror(t, w, rep)
	if got := w.count(`SELECT COUNT(*) FROM song_picks`); got != 16 {
		t.Errorf("song_picks = %d, want 16 (one per root Song)", got)
	}
	if got := w.count(`SELECT COUNT(*) FROM pick_events WHERE reason = 'initial'`); got != 16 {
		t.Errorf("initial pick events = %d, want 16", got)
	}
	if w.count(`SELECT COUNT(*) FROM sound_state WHERE subject_kind = 'song'`) == 0 {
		t.Errorf("no Sound was computed for any Song")
	}
	h := w.contentHash()
	if rep := w.settle(); rep.KeysProcessed != 0 || w.contentHash() != h {
		t.Errorf("a further Settle was not a no-op: %+v", rep)
	}
}

// TestAcceptance_L1_NoTempoNeeded: no acceptance item depends on the tempo analyzer (D-L1-39). The mirror above runs
// with no `analyses` tempo rows at all, and no `measured` Sound row exists.
func TestAcceptance_L1_NoTempoNeeded(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.prov = w.service(w.st, realSteps(t))
	librarytest.FixtureL1(t, w.st, librarytest.FixedNow)
	w.settle()
	w.verifyEmpty()
	if got := w.count(`SELECT COUNT(*) FROM take_sounds WHERE origin = 'measured'`); got != 0 {
		t.Errorf("%d measured rows without any tempo measurement", got)
	}
	if got := w.count(`SELECT COUNT(*) FROM song_picks`); got == 0 {
		t.Errorf("no Pick was derived")
	}
}

// TestRestore_RebuildsDerived: the derived Sound rows (and the Song structure rows that are not annotations) of a restored
// snapshot are rebuilt by one Settle. Drop every derived Sound row and re-mark the library: Verify names the loss, then
// Settle => Verify empty. The Pick table is event-sourced (no physical delete, a trigger refuses it), so it must come
// back unchanged and write no new event.
func TestRestore_RebuildsDerived(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.prov = w.service(w.st, realSteps(t))
	librarytest.FixtureL1(t, w.st, librarytest.FixedNow)
	w.settle()
	w.verifyEmpty()
	picksBefore := w.count(`SELECT COUNT(*) FROM song_picks`)
	eventsBefore := w.count(`SELECT COUNT(*) FROM pick_events`)
	soundsBefore := w.count(`SELECT COUNT(*) FROM song_sounds`)
	if picksBefore == 0 || soundsBefore == 0 {
		t.Fatalf("nothing to rebuild: %d picks, %d song sounds", picksBefore, soundsBefore)
	}
	// drop the derived rows (the writer connection allows it here: tests own the scratch database)
	for _, q := range []string{
		`DELETE FROM song_sounds`, `DELETE FROM take_sounds`, `DELETE FROM song_unmapped_terms`, `DELETE FROM sound_state`,
	} {
		w.exec(q)
	}
	if an, err := w.prov.Verify(context.Background()); err != nil || len(an) == 0 {
		t.Fatalf("the dropped rows must be detected: %v %v", an, err)
	}
	// the restore path (and `library reconcile`) re-marks every key, then Settle derives the rows again
	w.exec(`INSERT OR IGNORE INTO provenance_dirty(kind, id, at) SELECT 'track', id, '2026-10-15T00:00:00.000Z' FROM tracks`)
	w.exec(`INSERT OR IGNORE INTO provenance_dirty(kind, id, at) SELECT 'draft', id, '2026-10-15T00:00:00.000Z' FROM recipe_drafts`)
	w.settle()
	w.verifyEmpty()
	if got := w.count(`SELECT COUNT(*) FROM song_picks`); got != picksBefore {
		t.Errorf("song_picks = %d after the rebuild, want %d", got, picksBefore)
	}
	if got := w.count(`SELECT COUNT(*) FROM pick_events`); got != eventsBefore {
		t.Errorf("pick_events = %d after the rebuild, want %d (no new event)", got, eventsBefore)
	}
	if got := w.count(`SELECT COUNT(*) FROM song_sounds`); got != soundsBefore {
		t.Errorf("song_sounds = %d after the rebuild, want %d", got, soundsBefore)
	}
}
