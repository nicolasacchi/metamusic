package provenance_test

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// TestVerify_TransformTakeNeverNewest: a Song whose newest take is a transform output keeps a 'newest' Pick on its
// newest non-transform take (C211.6). Verify must derive with the same input as the recompute step (Transform set),
// so it reports no pick_stale for it (the production false positive "pick_stale <song> newest->newest").
func TestVerify_TransformTakeNeverNewest(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.prov = w.service(w.st, realSteps(t))
	sc := w.sc
	root := sc.Track(sc.Recipe("tv_v1", "pop"), "Root", "", sc.At(-10))
	w.settle()
	tk := librarytest.InsertTransformTake(t, w.st, librarytest.FixedNow, librarytest.TransformTake{N: 1, Kind: "widen", Parent: root, Age: time.Second})
	w.settle()
	song := w.str(`SELECT song_id FROM take_songs WHERE track_id = ?`, root)
	if got := w.str(`SELECT take_id FROM song_picks WHERE song_id = ?`, song); got != root {
		t.Fatalf("pick = %s, want the root %s (transform %s must not win by being newest)", got, root, tk)
	}
	w.verifyEmpty()
}
