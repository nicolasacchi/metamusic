package ears_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func fp(v float64) *float64 { return &v }

// streamJSON is one token-loop stream with a periodic run of the given span.
func streamJSON(index int, spanS float64) string {
	return `{"index":` + itoa(index) + `,"n_tokens":400,"distinct_tokens":10,"tokens_per_s":50,"distinct_4gram_ratio":0.5,` +
		`"longest_repeat":{"tokens":0,"s":0,"first_at_s":0,"second_at_s":0},` +
		`"periodic_run":{"period_tokens":5,"period_s":0.1,"span_tokens":100,"span_s":` + ftoa(spanS) + `,"at_s":1,"repeats":2},` +
		`"gzip_ratio_10s":[],"gzip_ratio_min":null,"gzip_ratio_median":null,"encoding":"u16le"}`
}

func TestCompareTracks_Deltas(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	ctx := context.Background()
	// TwinA: token loops (max periodic span 9.5 s over two streams), no clipping or silence rows
	r.exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, 'token_loops', 'mm-token-loops/1 go1.27', 'A', ?, ?)`,
		lt.TrackTwinA, `{"dtype":"<i8","shape":[2,400],"time_axis":1,"streams":[`+streamJSON(0, 2.0)+`,`+streamJSON(1, 9.5)+`]}`, "2026-10-06T11:00:00Z")

	c, err := r.svc.Compare(ctx, lt.TrackCitta, lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	// b - a: Luna (-14.0 LUFS, 16000 Hz, 12 s, clean) minus Citta (-15.5, 12000, 6 s, 2.0 s clipped, 2.5 s internal silence)
	want := ears.Deltas{IntegratedLUFS: fp(1.5), TruePeakDBTP: fp(0), LRALU: fp(0), BandwidthHz: fp(4000), DurationS: fp(6),
		ClippedS: fp(-2), InternalSilenceS: fp(-2.5)} // Citta has no token_loops row: null
	if diff := cmp.Diff(want, c.Deltas); diff != "" {
		t.Errorf("deltas (-want +got):\n%s", diff)
	}
	if c.A.TrackID != lt.TrackCitta || c.B.TrackID != lt.TrackLuna || c.A.Seed != 102 || c.B.Seed != 101 || c.A.DurationS != 6 || c.A.Tier != "A" || c.B.Tier != "A" {
		t.Errorf("sides = %+v / %+v", c.A, c.B)
	}
	if c.A.Title == "" || c.A.RequestID != "fx-citta" || c.B.RootTrackID != lt.TrackLuna {
		t.Errorf("side fields = %+v / %+v", c.A, c.B)
	}

	// token_loop_span_s: difference of the per-side maxima over streams, 9.5 (TwinA) - 6.5 (Luna)
	c, err = r.svc.Compare(ctx, lt.TrackLuna, lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	if c.Deltas.TokenLoopSpanS == nil || *c.Deltas.TokenLoopSpanS != 3.0 {
		t.Errorf("token_loop_span_s = %v, want 3.0", c.Deltas.TokenLoopSpanS)
	}
	if c.Deltas.ClippedS != nil || c.Deltas.InternalSilenceS != nil {
		t.Errorf("clipped/internal = %v/%v, want null (TwinA has no such rows)", c.Deltas.ClippedS, c.Deltas.InternalSilenceS)
	}
	if c.Deltas.IntegratedLUFS == nil || *c.Deltas.IntegratedLUFS != 2.0 {
		t.Errorf("integrated_lufs = %v, want -12.0 - -14.0 = 2.0", c.Deltas.IntegratedLUFS)
	}
	if c.B.Tier != "0" {
		t.Errorf("TwinA tier = %q", c.B.Tier)
	}
}

func TestCompareTracks_FlagsAndRecipe(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	ctx := context.Background()
	c, err := r.svc.Compare(ctx, lt.TrackCitta, lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"clipping", "internal_silence", "band_limited"} {
		if !slices.Contains(c.Flags.OnlyA, code) || slices.Contains(c.Flags.OnlyB, code) {
			t.Errorf("%s must be only on a: %+v", code, c.Flags)
		}
	}
	if !slices.Contains(c.Flags.OnlyB, "token_loop_suspected") {
		t.Errorf("token_loop_suspected must be only on b: %+v", c.Flags)
	}
	if c.Flags.Both == nil || c.Flags.OnlyA == nil || c.Flags.OnlyB == nil {
		t.Error("flag lists must not be nil")
	}
	a, _ := r.lib.Track(ctx, lt.TrackCitta)
	b, _ := r.lib.Track(ctx, lt.TrackLuna)
	if c.RecipeDiff.SameHash || !slices.Equal(c.RecipeDiff.Changed, []string{"seed", "style", "lyrics"}) {
		t.Errorf("recipe diff = %+v", c.RecipeDiff)
	}
	if c.RecipeDiff.Seed.A != 102 || c.RecipeDiff.Seed.B != 101 {
		t.Errorf("seeds = %+v", c.RecipeDiff.Seed)
	}
	var added, removed []string
	for _, s := range b.StyleTerms {
		if !slices.Contains(a.StyleTerms, s) {
			added = append(added, s)
		}
	}
	for _, s := range a.StyleTerms {
		if !slices.Contains(b.StyleTerms, s) {
			removed = append(removed, s)
		}
	}
	if len(added) == 0 || len(removed) == 0 {
		t.Fatalf("fixture style terms do not differ: %v / %v", a.StyleTerms, b.StyleTerms)
	}
	if !slices.Equal(c.RecipeDiff.StyleTerms.Added, added) || !slices.Equal(c.RecipeDiff.StyleTerms.Removed, removed) {
		t.Errorf("style terms = %+v, want +%v -%v", c.RecipeDiff.StyleTerms, added, removed)
	}

	// two seeds of one request: only the seed differs
	c, err = r.svc.Compare(ctx, lt.TrackTwinA, lt.TrackTwinB)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.RecipeDiff.Changed, []string{"seed"}) || len(c.RecipeDiff.StyleTerms.Added) != 0 || c.RecipeDiff.StyleTerms.Added == nil || c.RecipeDiff.StyleTerms.Removed == nil {
		t.Errorf("twin recipe diff = %+v", c.RecipeDiff)
	}
	if c.BlindSpots == nil {
		t.Error("blind_spots must not be nil")
	}
}

func TestCompareTracks_DuelsAndLadder(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows())
	ctx := context.Background()
	a, b := lt.TrackCitta, lt.TrackLuna
	r.judg.Pairs[[2]string{a, b}] = ladder.PairStats{Human: ladder.Counts{AWins: 2, BWins: 1, N: 3}, HumanBlindN: 2}
	r.judg.Strength[a] = ladder.Strength{U: 0.4, SD: 0.5, NDuels: 3}
	r.judg.Strength[b] = ladder.Strength{U: -0.2, SD: 0.6, NDuels: 3}
	c, err := r.svc.Compare(ctx, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if c.Duels.HumanBlindN != 2 || c.Duels.Human.AWins != 2 {
		t.Errorf("duels = %+v", c.Duels)
	}
	if c.Ladder.A == nil || c.Ladder.B == nil || c.Ladder.A.U != 0.4 || c.Ladder.B.NDuels != 3 || c.Ladder.Nu != 0.5 {
		t.Fatalf("ladder = %+v", c.Ladder)
	}
	win, tie, _ := ladder.PBeats(0.4, -0.2, 0.5)
	if c.Ladder.PABeatsB == nil || *c.Ladder.PABeatsB != win || c.Ladder.PTie == nil || *c.Ladder.PTie != tie {
		t.Errorf("p = %v/%v, want %v/%v", c.Ladder.PABeatsB, c.Ladder.PTie, win, tie)
	}
	// only one side has counted duels: probabilities are null
	delete(r.judg.Strength, b)
	c, _ = r.svc.Compare(ctx, a, b)
	if c.Ladder.A == nil || c.Ladder.B != nil || c.Ladder.PABeatsB != nil || c.Ladder.PTie != nil {
		t.Errorf("one-sided ladder = %+v", c.Ladder)
	}
	// no judgments service wired
	r2 := newRig(t, withTierARows(), func(_ *rig, o *ears.Options) { o.Judgments = nil })
	c, err = r2.svc.Compare(ctx, a, b)
	if err != nil || c.Ladder.A != nil || c.Ladder.PABeatsB != nil || c.Duels.Human.N != 0 {
		t.Errorf("without judgments: %+v %v", c, err)
	}
}

func TestCompareTracks_Errors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	_, err := r.svc.Compare(context.Background(), lt.TrackCitta, lt.TrackCitta)
	if pe := problemOf(t, err); pe.Status != 422 || pe.Code != "COMPARE_SAME_TRACK" {
		t.Errorf("same track: %+v", pe)
	}
	if _, err := r.svc.Compare(context.Background(), lt.TrackCitta, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown b: %v", err)
	}
	if _, err := r.svc.Compare(context.Background(), "nope", lt.TrackCitta); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown a: %v", err)
	}
}

func TestCompareTracks_Relation(t *testing.T) {
	r := newRig(t) // sequential: later steps edit the ledger
	ctx := context.Background()
	rel := func(a, b string) string {
		t.Helper()
		c, err := r.svc.Compare(ctx, a, b)
		if err != nil {
			t.Fatal(err)
		}
		return c.Relation
	}
	// TwinB is an inferred sibling (child edge) of TwinA
	if got := rel(lt.TrackTwinA, lt.TrackTwinB); got != "siblings" {
		t.Errorf("root vs inferred sibling = %q", got)
	}
	if got := rel(lt.TrackTwinB, lt.TrackTwinA); got != "siblings" {
		t.Errorf("inferred sibling vs root = %q", got)
	}
	if got := rel(lt.TrackLuna, lt.TrackCitta); got != "unrelated" {
		t.Errorf("different roots = %q", got)
	}

	// Citta becomes a second inferred sibling of TwinA
	r.dropTriggers("tracks_frozen", "mutations_immutable")
	r.exec(`UPDATE tracks SET parent_track_id = ?, root_track_id = ?, generation = 1 WHERE id = ?`, lt.TrackTwinA, lt.TrackTwinA, lt.TrackCitta)
	r.exec(`INSERT INTO mutations(id, parent_track_id, child_track_id, operator, inferred, params_json, principal_id, created_at)
		VALUES ('01900000-0000-7000-8000-00000000ffff', ?, ?, 'inferred_sibling', 1, '{"rule":"same_request_id"}', 'cli', '2026-10-06T11:00:00Z')`, lt.TrackTwinA, lt.TrackCitta)
	if got := rel(lt.TrackTwinB, lt.TrackCitta); got != "siblings" {
		t.Errorf("two inferred siblings of one root = %q", got)
	}

	// an edge with another operator
	r.exec(`UPDATE mutations SET operator = 'regenerate', inferred = 0 WHERE child_track_id = ?`, lt.TrackTwinB)
	if got := rel(lt.TrackTwinA, lt.TrackTwinB); got != "parent_child" {
		t.Errorf("root vs child (other operator) = %q", got)
	}
	if got := rel(lt.TrackTwinB, lt.TrackTwinA); got != "child_parent" {
		t.Errorf("child (other operator) vs root = %q", got)
	}
	// same parent but only one inferred edge: same lineage
	if got := rel(lt.TrackTwinB, lt.TrackCitta); got != "same_lineage" {
		t.Errorf("siblings with a non-inferred edge = %q", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func ftoa(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func TestUseJudgments_LateWiring(t *testing.T) {
	t.Parallel()
	r := newRig(t, withTierARows(), func(_ *rig, o *ears.Options) { o.Judgments = nil })
	r.svc.UseJudgments(r.judg)
	r.judg.Strength[lt.TrackCitta] = ladder.Strength{U: 1, SD: 1, NDuels: 2}
	c, err := r.svc.Compare(context.Background(), lt.TrackCitta, lt.TrackLuna)
	if err != nil || c.Ladder.A == nil || c.Ladder.Nu != 0.5 {
		t.Errorf("ladder after UseJudgments = %+v (%v)", c.Ladder, err)
	}
}

func TestUnwiredServiceIsNotImplemented(t *testing.T) {
	t.Parallel()
	svc := ears.New(ears.Options{})
	ctx := context.Background()
	if _, err := svc.Dossier(ctx, "x"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Dossier: %v", err)
	}
	if _, err := svc.Spectrogram(ctx, human, "x", true); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Spectrogram: %v", err)
	}
	if _, err := svc.Compare(ctx, "x", "y"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Compare: %v", err)
	}
	if _, err := svc.Perception(ctx, "x"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Perception: %v", err)
	}
	if _, err := svc.AnalyzeTrack(ctx, "x", ears.AnalyzeOptions{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("AnalyzeTrack: %v", err)
	}
	if _, err := svc.Backfill(ctx, ears.BackfillOptions{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Backfill: %v", err)
	}
}
