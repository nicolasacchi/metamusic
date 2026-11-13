package clip_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// While the tool probe is pending (Options.Unavailable returns an error) every render path answers 503
// UNAVAILABLE {component: clips} before touching a master; once it returns nil the same service renders.
func TestClip_UnavailableUntilToolsRecover(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var down atomic.Bool
	down.Store(true)
	s := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil, clip.Options{
		Unavailable: func() error {
			if down.Load() {
				return errors.New("probe pending")
			}
			return nil
		}})
	sp := clip.Spec{MasterSHA256: "x", MasterPath: "audio/masters/none.flac", SampleRate: 48000, Channels: 2, W: clip.DefaultWindow(60)}
	check := func(name string, err error) {
		t.Helper()
		var pe *problem.Error
		if !errors.As(err, &pe) || pe.Status != 503 || pe.Code != "UNAVAILABLE" || pe.Details["component"] != "clips" {
			t.Fatalf("%s: want 503 UNAVAILABLE clips, got %v", name, err)
		}
	}
	_, err := s.Ensure(context.Background(), sp)
	check("Ensure", err)
	_, err = s.Measure(context.Background(), sp)
	check("Measure", err)
	_, err = s.Reference(context.Background(), clip.RefSpec{})
	check("Reference", err)
	down.Store(false)
	if _, err := s.Ensure(context.Background(), sp); err != nil {
		var pe *problem.Error
		if errors.As(err, &pe) && pe.Code == "UNAVAILABLE" {
			t.Fatalf("recovered service still unavailable: %v", err)
		}
	}
}
