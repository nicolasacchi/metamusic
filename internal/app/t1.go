package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// T1Options are the test injection points of the transform service (C216.3). Production leaves every field zero: the render
// child is the exec supervisor over this binary and the media steps run ffmpeg and flac. A fake child never ships: the fakes
// live in internal/transform/transformtest, which no release binary links (TestNoTestHelpersInBinary).
type T1Options struct {
	Child     transform.ChildRunner
	Media     transform.Media
	Pipeline  transform.Ingester
	Resample  transform.Resampler
	Fault     func(point string) error
	Toolchain func() transform.Toolchain
	Self      string // nil-safe override of os.Executable
	Log       *slog.Logger
}

// t1Inputs is what the T1 wiring draws on.
type t1Inputs struct {
	cfg   config.Config
	st    *store.Store
	lib   *library.Service
	data  *os.Root
	ears  *ears.Service
	pipe  *ingest.Pipeline // the M1 freeze; nil without ffmpeg
	now   func() time.Time
	newID func() string
	o     T1Options
}

// buildT1 constructs the transform service (C210). It returns nil when MM_TRANSFORM_ENABLED is false, which makes the seven
// operations answer 503 UNAVAILABLE {component: transform}. Nothing is started here: serve starts the runner (RunTransforms).
// Without the M1 pipeline (no ffmpeg) the service is built but its runner idles and RunTransforms says so.
func buildT1(in t1Inputs) *transform.Service {
	if !in.cfg.TransformEnabled {
		return nil
	}
	self := in.o.Self
	if self == "" {
		self, _ = os.Executable()
	}
	var svc *transform.Service
	o := transform.Options{
		Store: in.st, Library: in.lib, Config: in.cfg, Now: in.now, NewID: in.newID,
		// The runner is woken through this injected, nil-safe hook, never through an import of its package (D-T1-17).
		Wake:      func() { svc.Kick() },
		Tools:     mediatool.Tools{FFmpeg: in.cfg.FFmpeg, FLAC: in.cfg.FLAC, Metaflac: in.cfg.Metaflac},
		Data:      in.data,
		DataDir:   in.cfg.DataDir,
		Owner:     "transform-runner",
		Self:      self,
		Log:       in.o.Log,
		Fault:     in.o.Fault,
		Child:     in.o.Child,
		Media:     in.o.Media,
		Resample:  in.o.Resample,
		Toolchain: in.o.Toolchain,
	}
	// never a typed-nil interface: a nil *ingest.Pipeline or *ears.Service would look present
	switch {
	case in.o.Pipeline != nil:
		o.Pipeline = in.o.Pipeline
	case in.pipe != nil:
		o.Pipeline = in.pipe
	}
	if in.ears != nil {
		o.Ears = in.ears
	}
	svc = transform.New(o)
	return svc
}

// RunTransforms starts the transform runner on ctx; serve calls it once, after the first derivation. Tests, mcp-stdio and the CLI
// never do. The runner recovers and sweeps first, then claims one transform at a time.
func RunTransforms(ctx context.Context, svc *Services, running *sync.WaitGroup) {
	t := svc.Transform
	if t == nil {
		return
	}
	if !t.Runnable() {
		slog.Info("transform runner not started", "reason", "the media pipeline is unavailable (ffmpeg, flac or metaflac missing)")
		return
	}
	running.Add(1)
	go func() { defer running.Done(); t.Run(ctx) }()
}

// resampleOr is the second-input resampler of D-T1-19: the test override, else the windowed-sinc dsp.Resample on each channel.
func resampleOr(r transform.Resampler) transform.Resampler {
	if r != nil {
		return r
	}
	return func(b pcm.Buffer, to int) (pcm.Buffer, error) {
		if b.Rate <= 0 || to <= 0 {
			return pcm.Buffer{}, errors.New("app: resample needs positive rates")
		}
		ratio := float64(to) / float64(b.Rate)
		out := pcm.Buffer{Rate: to}
		for c := range b.Ch {
			out.Ch[c] = dsp.Resample(b.Ch[c], ratio, 0)
		}
		return out, nil
	}
}
