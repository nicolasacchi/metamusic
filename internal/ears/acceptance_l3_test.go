package ears_test

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// TestAcceptance_L3_AnalyzersOnRealAudio (media tier): the key and energy analyzers through the real ffmpeg decode (a
// 22.05 kHz FLAC resampled to 11 025 Hz), on the files of FixtureL3Audio. Bars: C136.1 (22 of 24 exact plain, 20 of 24 at -35 cents,
// every result within {exact, relative, fifth}) and C136.2 (ordering with gaps of at least 0.15), and the tempo of a 120 BPM click file.
func TestAcceptance_L3_AnalyzersOnRealAudio(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	tools := testtools.Tools()
	f, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	a := lt.FixtureL3Audio(t, tools)
	ctx := context.Background()

	type verdict struct {
		cents float64
		rel   string
		name  string
	}
	var mu sync.Mutex
	var verdicts []verdict
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, s := range a.Sources {
		if s.Kind != "key" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			pcm, err := f.Mono(ctx, a.Abs(s), 240)
			if err != nil {
				t.Errorf("%s: %v", s.Name, err)
				return
			}
			got := analysis.ComputeKey(pcm)
			rel := "other"
			switch {
			case got.Key == nil:
				rel = "null"
			case *got.Tonic == s.Tonic && (*got.Mode == "minor") == s.Minor:
				rel = "exact"
			case s.Minor && *got.Mode == "major" && *got.Tonic == (s.Tonic+3)%12, !s.Minor && *got.Mode == "minor" && *got.Tonic == (s.Tonic+9)%12:
				rel = "relative"
			case (*got.Mode == "minor") == s.Minor && (*got.Tonic == (s.Tonic+7)%12 || *got.Tonic == (s.Tonic+5)%12):
				rel = "fifth"
			}
			mu.Lock()
			verdicts = append(verdicts, verdict{s.Cents, rel, s.Name})
			mu.Unlock()
		}()
	}
	wg.Wait()
	for _, cents := range []float64{0, -35} {
		counts := map[string]int{}
		for _, v := range verdicts {
			if v.cents == cents {
				counts[v.rel]++
				if v.rel != "exact" {
					t.Logf("%s (%v cents): %s", v.name, cents, v.rel)
				}
			}
		}
		want := map[float64]int{0: 22, -35: 20}[cents]
		if counts["exact"] < want || counts["other"] > 0 {
			t.Errorf("%v cents: %v, want at least %d exact and no result outside {exact, relative, fifth}", cents, counts, want)
		}
	}

	energy := map[string]float64{}
	for _, name := range []string{"dense", "pad", "quiet"} {
		p := a.Abs(a.Source(t, name))
		l, err := f.Loudness(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		pcm, err := f.Mono(ctx, p, 240)
		if err != nil {
			t.Fatal(err)
		}
		e := analysis.ComputeEnergy(pcm, &l.IntegratedLUFS)
		if e.Energy == nil {
			t.Fatalf("%s: no energy %+v", name, e)
		}
		energy[name] = *e.Energy
	}
	if d, p, q := energy["dense"], energy["pad"], energy["quiet"]; d-p < 0.15 || p-q < 0.15 {
		t.Errorf("energy ordering dense %.3f > pad %.3f > quiet %.3f with gaps >= 0.15 violated", d, p, q)
	}

	bpm, err := f.Tempo(ctx, a.Abs(a.Source(t, "clicks120")), 30)
	if err != nil || bpm.BPM == nil || !slices.ContainsFunc([]float64{60, 120, 240}, func(v float64) bool { return math.Abs(*bpm.BPM-v) < 2 }) {
		t.Errorf("clicks120 tempo %+v (%v)", bpm, err)
	}
}

// realMono is the fake Tier A for Signal and the spectrogram and the real ffmpeg for the shared decode. It embeds nothing, so
// ears does not see the fake's estimator seam: tempo, key and energy are the real functions of the real decode.
type realMono struct {
	fake *earstest.TierA
	real *ffmpeg.FFmpeg
}

func (m realMono) ToolVersion() string { return m.fake.ToolVersion() }
func (m realMono) Signal(ctx context.Context, p string, sr int, d float64) (analysis.Silence, analysis.Clipping, error) {
	return m.fake.Signal(ctx, p, sr, d)
}
func (m realMono) SpectrumPicture(ctx context.Context, p, png string, w, h int) error {
	return m.fake.SpectrumPicture(ctx, p, png, w, h)
}
func (m realMono) Mono(ctx context.Context, p string, maxS int) (analysis.PCM, error) {
	return m.real.Mono(ctx, p, maxS)
}

// TestEars_AnalyzeTrackRealDecode runs AnalyzeTrack with the real shared decode: a real 40 s FLAC (a triad progression in
// D minor) replaces the rig's master, and the stored key and energy rows are those of the real analyzers.
func TestEars_AnalyzeTrackRealDecode(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	tools := testtools.Tools()
	f, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	a := lt.FixtureL3Audio(t, tools)
	src, err := os.ReadFile(a.Abs(a.Source(t, "key-02-minor-plain")))
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, func(r *rig, o *ears.Options) { o.Analyzer = realMono{fake: r.tier, real: f} })
	id := lt.TrackTwinA
	refs, err := r.lib.TrackRefs(context.Background(), []string{id})
	if err != nil {
		t.Fatal(err)
	}
	master := filepath.Join(r.dir, filepath.FromSlash(refs[id].MasterPath))
	if err := os.WriteFile(master, src, 0o644); err != nil { //nolint:gosec // a test master
		t.Fatal(err)
	}
	out, err := r.svc.AnalyzeTrack(context.Background(), id, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, an := range []string{"tempo", "key", "energy"} {
		if !slices.Contains(out.Ran, an) || out.Failed[an] != "" {
			t.Errorf("%s: %+v", an, out)
		}
	}
	p, err := r.svc.Perception(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Key == nil || p.Key.Name != "D minor" || p.Key.Camelot != "7A" || p.Energy == nil {
		t.Errorf("perception key %+v energy %+v", p.Key, p.Energy)
	}
	out, err = r.svc.AnalyzeTrack(context.Background(), id, ears.AnalyzeOptions{})
	if err != nil || slices.Contains(out.Ran, "key") || slices.Contains(out.Ran, "tempo") {
		t.Errorf("second run: %+v %v", out, err)
	}
}
