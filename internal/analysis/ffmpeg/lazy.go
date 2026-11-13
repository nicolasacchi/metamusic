package ffmpeg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Lazy is an ffmpeg analyzer whose startup probe may still be pending. The consumers (clips, Tier A, the generation
// ingest pipeline) are built once with a Lazy; until Set installs the probed *FFmpeg every analysis answers
// analysis.ErrToolMissing, afterwards it delegates. A slow boot (the one-shot probe timing out on a loaded host) then
// heals by itself once Retry succeeds, with no restart.
type Lazy struct {
	mu      sync.Mutex
	f       *FFmpeg
	clips   error
	tier    error
	ready   chan struct{}
	onReady []func()
}

// NewLazy returns a Lazy that is not ready.
func NewLazy() *Lazy { return &Lazy{ready: make(chan struct{})} }

var (
	_ analysis.Analyzer   = (*Lazy)(nil)
	_ clip.WindowAnalyzer = (*Lazy)(nil)
)

func (l *Lazy) get() *FFmpeg {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f
}

func pending() error { return fmt.Errorf("%w: ffmpeg probe pending", analysis.ErrToolMissing) }

// Set installs the probed analyzer. Only the first call counts. It fires the OnReady hooks (outside the lock).
func (l *Lazy) Set(f *FFmpeg) {
	l.mu.Lock()
	if l.f != nil || f == nil {
		l.mu.Unlock()
		return
	}
	l.f, l.clips, l.tier = f, f.RequireClips(), f.RequireTierA()
	hooks := l.onReady
	l.onReady = nil
	close(l.ready)
	l.mu.Unlock()
	for _, h := range hooks {
		h()
	}
}

// OnReady registers fn to run once the probe has succeeded (immediately when it already has).
func (l *Lazy) OnReady(fn func()) {
	l.mu.Lock()
	if l.f == nil {
		l.onReady = append(l.onReady, fn)
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	fn()
}

// Ready reports whether the probe has succeeded.
func (l *Lazy) Ready() bool { return l.get() != nil }

// ReadyCh is closed once the probe has succeeded.
func (l *Lazy) ReadyCh() <-chan struct{} { return l.ready }

// ClipsErr is nil when clips can run: the probe succeeded and ffmpeg has what the clip service needs.
func (l *Lazy) ClipsErr() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return pending()
	}
	return l.clips
}

// TierAErr is nil when Tier A can run.
func (l *Lazy) TierAErr() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return pending()
	}
	return l.tier
}

// ToolVersion is the probed token, or "ffmpeg" while the probe is pending (no clip key is minted then: the clip service refuses first).
func (l *Lazy) ToolVersion() string {
	if f := l.get(); f != nil {
		return f.ToolVersion()
	}
	return "ffmpeg"
}

// Loudness delegates.
func (l *Lazy) Loudness(ctx context.Context, workPath string) (analysis.Loudness, error) {
	f := l.get()
	if f == nil {
		return analysis.Loudness{}, pending()
	}
	return f.Loudness(ctx, workPath)
}

// Scan delegates.
func (l *Lazy) Scan(ctx context.Context, workPath string, sampleRate, channels int, totalSamples uint64) (analysis.Peaks, analysis.Spectrum, error) {
	f := l.get()
	if f == nil {
		return analysis.Peaks{}, analysis.Spectrum{}, pending()
	}
	return f.Scan(ctx, workPath, sampleRate, channels, totalSamples)
}

// CountFrames delegates.
func (l *Lazy) CountFrames(ctx context.Context, workPath string, sampleRate, channels int) (uint64, error) {
	f := l.get()
	if f == nil {
		return 0, pending()
	}
	return f.CountFrames(ctx, workPath, sampleRate, channels)
}

// WindowLoudness delegates.
func (l *Lazy) WindowLoudness(ctx context.Context, workPath string, startSample, endSample int64) (clip.Measure, error) {
	f := l.get()
	if f == nil {
		return clip.Measure{}, pending()
	}
	return f.WindowLoudness(ctx, workPath, startSample, endSample)
}

// Signal delegates (ears.TierA).
func (l *Lazy) Signal(ctx context.Context, workPath string, sampleRate int, durationS float64) (analysis.Silence, analysis.Clipping, error) {
	f := l.get()
	if f == nil {
		return analysis.Silence{}, analysis.Clipping{}, pending()
	}
	return f.Signal(ctx, workPath, sampleRate, durationS)
}

// SpectrumPicture delegates (ears.TierA).
func (l *Lazy) SpectrumPicture(ctx context.Context, workPath, outPNG string, w, h int) error {
	f := l.get()
	if f == nil {
		return pending()
	}
	return f.SpectrumPicture(ctx, workPath, outPNG, w, h)
}

// Mono delegates (ears.TierA).
func (l *Lazy) Mono(ctx context.Context, workPath string, maxS int) (analysis.PCM, error) {
	f := l.get()
	if f == nil {
		return analysis.PCM{}, pending()
	}
	return f.Mono(ctx, workPath, maxS)
}

// RetryOptions tunes Retry; the zero value is 5 s doubling to 5 min.
type RetryOptions struct {
	Base, Max time.Duration
	Log       *slog.Logger
}

// Retry probes again with exponential backoff until one attempt succeeds (then it Sets l and returns) or ctx ends. Every
// failure is logged at WARN (the interval is at most Max, so a stuck pipeline warns every few minutes).
func (l *Lazy) Retry(ctx context.Context, probe func(context.Context) (*FFmpeg, error), o RetryOptions) {
	if o.Base <= 0 {
		o.Base = 5 * time.Second
	}
	if o.Max <= 0 {
		o.Max = 5 * time.Minute
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	wait := o.Base
	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		f, err := probe(ctx)
		if err == nil {
			l.Set(f)
			log.Info("media tools recovered: clips, tier A, generation ingest and transforms enabled", "attempts", attempt+1)
			return
		}
		if ctx.Err() != nil {
			return
		}
		log.Warn("media tools still unavailable; retrying", "attempt", attempt+1, "retry_in", min(wait*2, o.Max).String(), "err", redact.Error(err))
		wait = min(wait*2, max(o.Max, o.Base))
	}
}
