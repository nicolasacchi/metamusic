package ears_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const lossyBlindSpot = "analysed from a lossy MP3 decode: bandwidth, clipping and true peak are not comparable with FLAC takes"

// pathRecorder wraps the fake Tier A and records the work paths it is handed.
type pathRecorder struct {
	*earstest.TierA
	mu    sync.Mutex
	paths []string
}

func (p *pathRecorder) Signal(ctx context.Context, wp string, sr int, d float64) (analysis.Silence, analysis.Clipping, error) {
	p.mu.Lock()
	p.paths = append(p.paths, wp)
	p.mu.Unlock()
	return p.TierA.Signal(ctx, wp, sr, d)
}

// newRigM2c is newRig over FixtureM2c (the reference rows) with a path-recording Tier A.
func newRigM2c(t *testing.T) (*rig, *pathRecorder) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.WriteFiles(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	now := lt.FixedNow
	r := &rig{t: t, st: st, root: root, dir: dir, now: now, tier: earstest.NewTierA(),
		judg: &earstest.Judgments{Pairs: map[[2]string]ladder.PairStats{}, Strength: map[string]ladder.Strength{}, Nu: 0.5}}
	r.fx = lt.FixtureM2c(t, st, now)
	r.lib = library.New(st, root, func() time.Time { return now })
	rec := &pathRecorder{TierA: r.tier}
	r.opts = ears.Options{Store: st, Library: r.lib, Data: root, DataDir: dir, Analyzer: rec, Judgments: r.judg,
		Signer: serve.NewSigner([]byte("test-secret-key-0123456789abcdef"), func() time.Time { return now }),
		Now:    func() time.Time { return now }}
	r.svc = ears.New(r.opts)
	return r, rec
}

func TestEars_LossyMaster(t *testing.T) {
	t.Parallel()
	r, rec := newRigM2c(t)
	ctx := context.Background()
	out, err := r.svc.AnalyzeTrack(ctx, lt.TrackLossy, ears.AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// the fake picture tool draws 48 kHz geometry, so the 44.1 kHz fixture's spectrogram may fail; Tier A signal must run
	if !slices.Contains(out.Ran, "silence") || !slices.Contains(out.Ran, "clipping") {
		t.Fatalf("Tier A did not run: %+v", out)
	}
	if len(rec.paths) != 1 || filepath.Base(rec.paths[0]) != "master.mp3" {
		t.Fatalf("work paths = %v, want one master.mp3", rec.paths)
	}
	d, err := r.svc.Dossier(ctx, lt.TrackLossy)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(d.BlindSpots, lossyBlindSpot) {
		t.Errorf("blind spots = %v", d.BlindSpots)
	}
	// a FLAC take's payload does not carry it
	d2, err := r.svc.Dossier(ctx, lt.TrackTwinA)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(d2.BlindSpots, lossyBlindSpot) {
		t.Errorf("FLAC take has the lossy blind spot: %v", d2.BlindSpots)
	}
}
