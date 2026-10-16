package provenance_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provenance"
)

// mirrorParams are the acceptance mirror's: values chosen different from the real library on purpose (C101.1).
var mirrorParams = librarytest.L1Params{Muses: 120, Angles: 3, DemoDrafts: 5, Originals: 4, Groups: 5, GroupSize: 3}

// wantMirror holds the literal integers of the backfill acceptance (C95.3, acceptance mapping), each computed once by
// independent SQL over the fixture's *source* tables: FixtureM3 (6 roots, 8 motherless backlog drafts of which one carries
// an index row with a variant type, 1 mothered draft, 1 import run, 3 batches of which 2 are active/finished with jobs, 1
// collection) plus the mirror.
type mirrorWant struct {
	muses, liveSongs, draftSongs, rootSongs, withMuse, unassigned int
	songRoots, sessions, retired                                  int
	angleA, angleB, angleC                                        int
}

var wantMirror = mirrorWant{
	muses: 121, // 120 + the base index row
	// 389 live Songs = 373 draft Songs (360 index-backed + 5 demo + 8 base) + 16 root Songs (6 base + 4 originals + 5 lossy groups + 1 lineage)
	liveSongs: 389, draftSongs: 373, rootSongs: 16,
	withMuse: 361, unassigned: 28, // 360 + the base row; 28 = 5 demo + 7 base drafts + 16 root Songs
	songRoots: 16,
	sessions:  8, // 3 import runs + 3 collections + 2 batches
	retired:   0,
	// variant-a 120; variant-b and variant-c 122 each: the base fixture's fxb_001_v2, fxb_002_v2 (and _v3) drafts have no
	// index row, and the request-id suffix rule of D-L1-7 gives them the angle every index row of that suffix agrees on.
	// The suffix _v1 is ambiguous (the base row says "slower", the mirror says variant-a), so no _v1 draft gains one. The
	// plan's integer (120 each) did not account for these four drafts; the rule, not the integer, is the contract.
	angleA: 120, angleB: 122, angleC: 122,
}

func checkMirror(t *testing.T, w *world, rep provenance.Report) {
	t.Helper()
	w.verifyEmpty()
	c := func(label string, got, want int) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %d, want %d", label, got, want)
		}
	}
	c("Muses", w.count(`SELECT COUNT(*) FROM muses`), wantMirror.muses)
	c("live Songs", w.count(`SELECT COUNT(*) FROM songs WHERE retired_at IS NULL`), wantMirror.liveSongs)
	c("draft Songs", w.count(`SELECT COUNT(*) FROM songs WHERE retired_at IS NULL AND origin = 'draft'`), wantMirror.draftSongs)
	c("root Songs", w.count(`SELECT COUNT(*) FROM songs WHERE retired_at IS NULL AND origin = 'root'`), wantMirror.rootSongs)
	c("Songs with a Muse", w.count(`SELECT COUNT(*) FROM songs WHERE muse_id IS NOT NULL`), wantMirror.withMuse)
	c("unassigned Songs", w.count(`SELECT COUNT(*) FROM songs WHERE muse_id IS NULL`), wantMirror.unassigned)
	c("retired Songs", w.count(`SELECT COUNT(*) FROM songs WHERE retired_at IS NOT NULL`), wantMirror.retired)
	c("song_roots", w.count(`SELECT COUNT(*) FROM song_roots`), wantMirror.songRoots)
	c("sessions", w.count(`SELECT COUNT(*) FROM sessions`), wantMirror.sessions)
	c("angle variant-a", w.count(`SELECT COUNT(*) FROM songs WHERE angle = 'variant-a'`), wantMirror.angleA)
	c("angle variant-b", w.count(`SELECT COUNT(*) FROM songs WHERE angle = 'variant-b'`), wantMirror.angleB)
	c("angle variant-c", w.count(`SELECT COUNT(*) FROM songs WHERE angle = 'variant-c'`), wantMirror.angleC)
	c("angles from the request-id suffix", w.count(`SELECT COUNT(*) FROM songs WHERE angle_source = 'request_id'`), 4)
	c("the base row's angle", w.count(`SELECT COUNT(*) FROM songs WHERE angle = 'slower'`), 1)
	// every take belongs to exactly one live Song
	c("takes without a Song", w.count(`SELECT COUNT(*) FROM tracks t WHERE NOT EXISTS (SELECT 1 FROM take_songs ts WHERE ts.track_id = t.id)`), 0)
	if p, _ := w.prov.Pending(context.Background()); p != 0 {
		t.Errorf("queue = %d after the backfill", p)
	}
	_ = rep
}

func TestAcceptance_L1_BackfillMirror(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("acceptance tier: make test and CI run it")
	}
	w := newWorld(t)
	info := librarytest.FixtureL1Mirror(t, w.st, librarytest.FixedNow, mirrorParams)
	if len(info.IndexDrafts) != 360 || len(info.Originals) != 4 || len(info.Groups) != 5 {
		t.Fatalf("mirror shape: %d index drafts, %d originals, %d groups", len(info.IndexDrafts), len(info.Originals), len(info.Groups))
	}
	rep := w.settle()
	checkMirror(t, w, rep)
	if rep.MusesCreated != 121 {
		t.Errorf("report: %+v", rep)
	}
}

func TestAcceptance_L1_ReferenceSongs(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	info := librarytest.FixtureL1(t, w.st, librarytest.FixedNow)
	w.settle()
	for _, o := range info.Originals { // each original reference recording is its own Song with one take
		var n int
		song := w.str(`SELECT song_id FROM take_songs WHERE track_id = ?`, o)
		if err := w.st.R.QueryRow(`SELECT COUNT(*) FROM take_songs WHERE song_id = ?`, song).Scan(&n); err != nil || song == "" || n != 1 {
			t.Errorf("original %s: song %q with %d takes (%v)", o, song, n, err)
		}
	}
	for _, g := range info.Groups { // a lossy pilot group is one Song with its three takes, rooted at p1
		song := w.str(`SELECT song_id FROM take_songs WHERE track_id = ?`, g[0])
		if got := w.count(`SELECT COUNT(*) FROM take_songs WHERE song_id = ?`, song); got != 3 {
			t.Errorf("group %v: %d takes in the Song, want 3", g, got)
		}
		for _, tk := range g {
			if w.str(`SELECT song_id FROM take_songs WHERE track_id = ?`, tk) != song {
				t.Errorf("take %s is not in the group's Song", tk)
			}
		}
		if w.str(`SELECT root_track_id FROM songs WHERE id = ?`, song) != g[0] {
			t.Errorf("the group's Song must be rooted at p1")
		}
	}
	w.verifyEmpty()
}

func TestBackfill_MuseFromRefs(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	librarytest.FixtureL1(t, w.st, librarytest.FixedNow)
	rep := w.settle()
	// 6 index Muses plus the base fixture's index row (Synthetic Reference Title)
	if got := w.count(`SELECT COUNT(*) FROM muses WHERE source = 'recipe_refs' AND created_by = 'system:provenance'`); got != 7 || rep.MusesCreated != 7 {
		t.Errorf("reference Muses = %d (report %d), want 7", got, rep.MusesCreated)
	}
	// three drafts per Muse: many Songs per Muse, one Muse per distinct normalised (title, artist)
	if got := w.count(`SELECT COUNT(*) FROM (SELECT muse_id FROM songs WHERE muse_id IS NOT NULL GROUP BY muse_id HAVING COUNT(*) = 3)`); got != 6 {
		t.Errorf("%d Muses with three Songs, want 6", got)
	}
	if got := w.count(`SELECT COUNT(*) FROM muses_fts`); got != 7 {
		t.Errorf("FTS rows = %d, want 7", got)
	}
}

func TestBackfill_Idempotent(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	librarytest.FixtureL1Mirror(t, w.st, librarytest.FixedNow, librarytest.L1Params{Muses: 8, Angles: 3, DemoDrafts: 2, Originals: 2, Groups: 2, GroupSize: 3})
	w.settle()
	h := w.contentHash()
	if rep := w.settle(); rep.KeysProcessed != 0 {
		t.Errorf("a second Settle processed %d keys", rep.KeysProcessed)
	}
	if h != w.contentHash() {
		t.Errorf("a second Settle wrote rows")
	}
}
