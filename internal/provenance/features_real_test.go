package provenance_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// featStep adapts *features.Service to provenance.FeatureStep, as app does (provenance cannot import features).
type featStep struct{ f *features.Service }

func (a featStep) ComputeTx(ctx context.Context, tx *sql.Tx, env store.Env, trackIDs, songIDs []string) (provenance.FeatureChanges, error) {
	c, err := a.f.ComputeTx(ctx, tx, env, trackIDs, songIDs)
	return provenance.FeatureChanges{Takes: c.Takes, Songs: c.Songs}, err
}

func (a featStep) SweepStaleTx(ctx context.Context, tx *sql.Tx, env store.Env, limit int) (int, error) {
	return a.f.SweepStaleTx(ctx, tx, env, limit)
}

// realFeatureSteps is realSteps plus the real feature step.
func realFeatureSteps(t *testing.T) func(*provenance.Options) {
	rs := realSteps(t)
	return func(o *provenance.Options) {
		rs(o)
		o.Features = featStep{features.New(o.Store, o.Now)}
	}
}

func (w *world) featuresVerifyEmpty() {
	w.t.Helper()
	an, err := features.New(w.st, nil).Verify(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	if len(an) != 0 {
		w.t.Errorf("features.Verify: %+v", an)
	}
}

// TestReconciler_FeaturesAfterSounds: a take ingested and rated in one pass gets its Sound rows and then its feature rows in
// the same pass; the Song row copies the take row and carries the primary Sound values the same pass computed.
func TestReconciler_FeaturesAfterSounds(t *testing.T) {
	t.Parallel()
	w := newWorld(t, realFeatureSteps(t))
	rec := w.sc.Recipe("fa_v1", "reggae, chill, 90bpm")
	take := w.sc.Track(rec, "Root", "", w.sc.At(2))
	w.sc.Rate(take, 4)
	rep := w.settle()
	if rep.FeaturesComputed == 0 || rep.SoundsComputed == 0 {
		t.Fatalf("report %+v", rep)
	}
	song := w.str(`SELECT song_id FROM take_songs WHERE track_id = ?`, take)
	if got := w.count(`SELECT COUNT(*) FROM take_features WHERE track_id = ?`, take); got != 1 {
		t.Fatalf("take rows = %d after one Settle", got)
	}
	if got := w.str(`SELECT subject_id FROM song_features WHERE song_id = ?`, song); got != take {
		t.Errorf("Song subject = %q, want the take", got)
	}
	if want, got := w.str(`SELECT value FROM song_sounds WHERE song_id = ? AND facet = 'genre_family'`, song), w.str(`SELECT family FROM song_features WHERE song_id = ?`, song); want == "" || got != want {
		t.Errorf("family = %q, want the Sound value %q (sounds must run before features)", got, want)
	}
	if got := w.count(`SELECT energy_x10000 FROM take_features WHERE track_id = ?`, take); got != 3333+0 && got == 0 {
		t.Errorf("predicted energy missing: %d", got)
	}
	w.verifyEmpty()
	w.featuresVerifyEmpty()
}

// TestReconciler_PassIdempotent_L3: after FixtureL3 settles, a second Settle changes no row of any L1 or L3 table.
func TestReconciler_PassIdempotent_L3(t *testing.T) {
	t.Parallel()
	w := newWorld(t, realFeatureSteps(t))
	librarytest.FixtureL3(t, w.st, librarytest.FixedNow)
	w.settle()
	w.verifyEmpty()
	w.featuresVerifyEmpty()
	h := w.contentHashL3()
	if w.count(`SELECT COUNT(*) FROM take_features`) == 0 || w.count(`SELECT COUNT(*) FROM song_features`) == 0 {
		t.Fatal("no feature rows")
	}
	if rep := w.settle(); rep.KeysProcessed != 0 || w.contentHashL3() != h {
		t.Errorf("a further Settle was not a no-op: %+v", rep)
	}
	if err := w.prov.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.contentHashL3() != h {
		t.Error("the sweep rewrote a row of a settled library")
	}
}

// TestProperty_FeatureInvariants drives seeded random sequences of: a take or a draft inserted, tempo/key/energy analyses added,
// a Pick changed, a Muse edited, a take discarded, with a Settle every few steps. The oracle: Verify (L1 and L3) is empty and a
// further Settle changes no row of any L1 or L3 table. MM_PROPERTY_SEQS overrides the 60 sequences; -short runs 6.
func TestProperty_FeatureInvariants(t *testing.T) {
	t.Parallel()
	seqs := 60
	if testing.Short() {
		seqs = 6
	}
	if v, err := strconv.Atoi(os.Getenv("MM_PROPERTY_SEQS")); err == nil && v > 0 {
		seqs = v
	}
	for seed := range seqs {
		t.Run(fmt.Sprintf("seq%03d", seed), func(t *testing.T) {
			t.Parallel()
			runFeatureSequence(t, uint64(seed))
		})
	}
}

func runFeatureSequence(t *testing.T, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, seed*104729+3))
	w := newWorld(t, realFeatureSteps(t))
	sc := w.sc
	var tracks, roots []string
	n := 0
	next := func() int { n++; return n }
	styles := []string{"reggae, chill, 90bpm", "pop, upbeat, in A minor, 120bpm", "rock, dark, 140bpm", "jazz, nostalgic", "dance, energetic, 128bpm", "ambient, in F# major"}
	versions := []string{"mm-tempo/1 ffmpeg a", "mm-tempo/1 ffmpeg b", "mm-tempo/2 x"}
	for step, steps := 0, 25+rng.IntN(20); step < steps; step++ {
		switch rng.IntN(10) {
		case 0, 1: // a root take
			r := sc.Track(sc.Recipe(fmt.Sprintf("pr%d_v1", next()), styles[rng.IntN(len(styles))]), fmt.Sprintf("Root %d", n), "", sc.At(next()))
			roots, tracks = append(roots, r), append(tracks, r)
		case 2: // a child take
			if len(tracks) > 0 {
				c := sc.Track(sc.Recipe(fmt.Sprintf("pc%d_v1", next()), styles[rng.IntN(len(styles))]), fmt.Sprintf("Child %d", n), tracks[rng.IntN(len(tracks))], sc.At(next()))
				tracks = append(tracks, c)
			}
		case 3: // a draft
			sc.Draft(fmt.Sprintf("pd%d_v1", next()), librarytest.DraftOpts{Recipe: sc.Recipe(fmt.Sprintf("pdr%d_v1", n), styles[rng.IntN(len(styles))]), Group: "g", Label: "variant-a"})
		case 4: // tempo analysis (a definition of another version is ignored)
			if len(tracks) > 0 {
				id := tracks[rng.IntN(len(tracks))]
				w.exec(`INSERT OR IGNORE INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,'tempo',?,'A',?,'2026-10-06T12:00:00Z')`,
					id, versions[rng.IntN(len(versions))], fmt.Sprintf(`{"bpm":%d.5,"confidence":2.5}`, 60+rng.IntN(150)))
			}
		case 5: // key and energy analyses
			if len(tracks) > 0 {
				id := tracks[rng.IntN(len(tracks))]
				w.exec(`INSERT OR IGNORE INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,'key',?,'A',?,'2026-10-06T12:00:00Z')`,
					id, fmt.Sprintf("mm-key/1 ffmpeg v%d", rng.IntN(2)), fmt.Sprintf(`{"tonic":%d,"mode":%q,"r":0.8,"margin":0.1}`, rng.IntN(12), []string{"major", "minor"}[rng.IntN(2)]))
				w.exec(`INSERT OR IGNORE INTO analyses(track_id,analyzer,version,tier,metrics_json,created_at) VALUES(?,'energy',?,'A',?,'2026-10-06T12:00:00Z')`,
					id, fmt.Sprintf("mm-energy/1 ffmpeg v%d", rng.IntN(2)), fmt.Sprintf(`{"energy":%.4f}`, rng.Float64()))
			}
		case 6: // a Pick change
			if len(tracks) > 0 {
				sc.Rate(tracks[rng.IntN(len(tracks))], 1+rng.IntN(5))
			}
		case 7: // a Muse is edited (the title text is not an input of any feature)
			_, _ = w.st.W.Exec(`UPDATE muses SET title = title || 'x' WHERE id = (SELECT id FROM muses LIMIT 1)`)
		case 8: // a take is discarded or restored
			if len(tracks) > 0 {
				sc.Status(tracks[rng.IntN(len(tracks))], []string{"draft", "discarded", "final"}[rng.IntN(3)])
			}
		case 9:
			w.settle()
		}
		if rng.IntN(5) == 0 {
			w.settle()
		}
	}
	w.settle()
	w.verifyEmpty()
	w.featuresVerifyEmpty()
	h := w.contentHashL3()
	if rep := w.settle(); rep.KeysProcessed != 0 || w.contentHashL3() != h {
		t.Errorf("seed %d: a further Settle was not a no-op: %+v", seed, rep)
	}
	if err := w.prov.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.contentHashL3() != h {
		t.Errorf("seed %d: the sweep rewrote a settled library", seed)
	}
}
