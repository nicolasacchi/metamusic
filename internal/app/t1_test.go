package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/transform"
	"github.com/nicolasacchi/metamusic/internal/transform/transformtest"
)

func t1Rig(t *testing.T, enabled bool, o T1Options) (*transform.Service, *Services) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	data, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	cfg := config.Config{TransformEnabled: enabled, FFmpeg: "ffmpeg", FLAC: "flac", Metaflac: "metaflac", DataDir: dir}
	svc := buildT1(t1Inputs{cfg: cfg, st: st, lib: library.New(st, data, time.Now), data: data, now: time.Now, newID: func() string { return "id" }, o: o})
	return svc, &Services{Transform: svc}
}

// TestBuildT1: MM_TRANSFORM_ENABLED=false builds no service (the seven operations answer 503); without the M1 pipeline the
// service exists but is not runnable (no typed-nil pipeline sneaks in), and RunTransforms starts nothing; with a pipeline (an
// injected one here) the runner starts and stops with its context.
func TestBuildT1(t *testing.T) {
	if svc, _ := t1Rig(t, false, T1Options{}); svc != nil {
		t.Fatal("a disabled transform service was built")
	}
	svc, all := t1Rig(t, true, T1Options{})
	if svc == nil || svc.Runnable() {
		t.Fatalf("without a pipeline: service %v runnable %v", svc, svc != nil && svc.Runnable())
	}
	var wg sync.WaitGroup
	RunTransforms(context.Background(), all, &wg)
	wg.Wait() // returns at once: nothing was started
	RunTransforms(context.Background(), &Services{}, &wg)

	svc2, all2 := t1Rig(t, true, T1Options{Pipeline: &transformtest.FakeIngest{}, Child: &transformtest.FakeChild{}})
	if !svc2.Runnable() {
		t.Fatal("not runnable with a pipeline and a data root")
	}
	ctx, cancel := context.WithCancel(context.Background())
	RunTransforms(ctx, all2, &wg)
	svc2.Kick() // the injected wake works on a running loop
	cancel()
	wg.Wait() // the runner stops with its context
}

// TestResampleOr: the default second-input resampler converts both channels by the rate ratio; an override wins.
func TestResampleOr(t *testing.T) {
	b := pcm.Buffer{Rate: 24000}
	b.Ch[0], b.Ch[1] = make([]float64, 2400), make([]float64, 2400)
	out, err := resampleOr(nil)(b, 48000)
	if err != nil || out.Rate != 48000 || len(out.Ch[0]) != 4800 || len(out.Ch[1]) != 4800 {
		t.Fatalf("out rate %d len %d err %v", out.Rate, len(out.Ch[0]), err)
	}
	if _, err := resampleOr(nil)(pcm.Buffer{}, 48000); err == nil {
		t.Error("a buffer with no rate was resampled")
	}
	called := false
	_, _ = resampleOr(func(pcm.Buffer, int) (pcm.Buffer, error) { called = true; return pcm.Buffer{}, nil })(b, 1)
	if !called {
		t.Error("the override was ignored")
	}
}
