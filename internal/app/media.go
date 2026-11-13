package app

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Compile-time contracts between the media services and their consumers.
var (
	_ clip.WindowAnalyzer = (*ffmpeg.FFmpeg)(nil)
	_ ears.TierA          = (*ffmpeg.FFmpeg)(nil)
)

// MediaOptions injects the ffmpeg-backed pieces; nil in production, where
// NewMedia probes cfg.FFmpeg itself. The rigs set it so no ffmpeg subprocess runs.
type MediaOptions struct {
	// Clip inputs. A nil Window with a nil ClipsMissing leaves clips unavailable.
	Window       clip.WindowAnalyzer
	Render       clip.RenderFunc
	ClipsMissing error
	// Ears inputs.
	TierA        ears.TierA
	TierAMissing error
}

// Media is what NewMedia builds. Clips is nil when ClipsMissing is set; Ears is
// always built (its read-only operations work from the database) and carries
// TierAMissing in its options.
type Media struct {
	Clips                      *clip.Service
	Ears                       *ears.Service
	ClipsMissing, TierAMissing error
	// Analyzer is the ffmpeg-backed analyzer the generation ingest pipeline uses; nil when ffmpeg is missing
	// or the inputs were injected (MediaOptions), in which case the ingest bridge has no pipeline.
	Analyzer analysis.Analyzer
	// Lazy is the probe state of the real ffmpeg (nil when the inputs were injected). A probe that failed at boot (a
	// timeout on a loaded host after a reboot) leaves it pending; StartProbe retries in the background and, once it
	// succeeds, clips, Tier A, the generation ingest and the transform runner switch on without a restart.
	Lazy *ffmpeg.Lazy
	// Retry tunes StartProbe's backoff; the zero value is 5 s doubling to 5 min.
	Retry ffmpeg.RetryOptions
	tools mediatool.Tools
}

// IngestReady reports whether generation ingest can store a finished take. nil means "always" (injected media, where
// the rig owns the ingest seam); otherwise it follows the ffmpeg probe.
func (m *Media) IngestReady() func() bool {
	if m.Lazy == nil {
		return nil
	}
	return m.Lazy.Ready
}

// Degraded names the pieces of the media pipeline that are down right now (empty when healthy).
func (m *Media) Degraded() []string {
	if m == nil || m.Lazy == nil || m.Lazy.Ready() {
		return nil
	}
	return []string{"media_tools"}
}

// StartProbe retries the ffmpeg probe in the background until it succeeds or ctx ends; it does nothing when the boot
// probe already succeeded or the media was injected. Serve calls it once; it adds to running so shutdown waits for it.
func (m *Media) StartProbe(ctx context.Context, running *sync.WaitGroup) {
	if m == nil || m.Lazy == nil || m.Lazy.Ready() {
		return
	}
	running.Add(1)
	go func() {
		defer running.Done()
		m.Lazy.Retry(ctx, func(c context.Context) (*ffmpeg.FFmpeg, error) { return ffmpeg.New(c, m.tools) }, m.Retry)
	}()
}

// defaultClipCacheMB applies to config literals that leave ClipCacheMB at 0 (the servertest rig).
const defaultClipCacheMB = 4096

// NewMedia builds the clip and Ears services (C51.7). The import, seed-synthetic
// and analyze commands never call Build, so they use this directly with
// pinned == nil. signer may be nil (the CLI): link-minting operations then fail
// with 503. With mo == nil it runs ffmpeg.New(cfg tools): a failure is not an
// error, it is recorded in the two ...Missing fields and logged once.
func NewMedia(ctx context.Context, cfg config.Config, st *store.Store, data *os.Root, lib *library.Service,
	signer *serve.Signer, now func() time.Time, pinned func(context.Context) (map[string]struct{}, error),
	mo *MediaOptions) (*Media, error) {
	if now == nil {
		now = time.Now
	}
	tools := mediatool.Tools{FFmpeg: cfg.FFmpeg, FLAC: cfg.FLAC, Metaflac: cfg.Metaflac}
	m := &Media{tools: tools}
	var win clip.WindowAnalyzer
	var render clip.RenderFunc
	var tier ears.TierA
	pending := false // the probe failed and is being retried: clips exist but refuse until it succeeds
	if mo != nil {
		win, render, tier = mo.Window, mo.Render, mo.TierA
		m.ClipsMissing, m.TierAMissing = mo.ClipsMissing, mo.TierAMissing
	} else {
		// Every consumer is built once on the lazy analyzer; a failed boot probe only leaves it pending (see Media.Lazy).
		f, err := ffmpeg.New(ctx, tools)
		lz := ffmpeg.NewLazy()
		m.Lazy = lz
		win, tier, m.Analyzer = lz, lz, lz
		switch {
		case err != nil:
			m.ClipsMissing, m.TierAMissing = err, err
			pending = true
		default:
			lz.Set(f)
			m.ClipsMissing, m.TierAMissing = f.RequireClips(), f.RequireTierA()
		}
	}
	if m.ClipsMissing != nil {
		slog.Warn("clips unavailable: clip-dependent operations answer 503", "err", redact.Error(m.ClipsMissing))
	}
	if m.TierAMissing != nil {
		slog.Warn("tier A unavailable: analysis reports TOOL_MISSING", "err", redact.Error(m.TierAMissing))
	}
	if (m.ClipsMissing == nil || pending) && win != nil {
		mb := cfg.ClipCacheMB
		if mb <= 0 {
			mb = defaultClipCacheMB
		}
		co := clip.Options{CacheBytes: mb << 20, Now: now, Pinned: pinned, Render: render}
		if m.Lazy != nil {
			co.Unavailable = m.Lazy.ClipsErr
		}
		m.Clips = clip.New(data, cfg.DataDir, tools, win, lib, signer, co)
	}
	eo := ears.Options{Store: st, Library: lib, Data: data, DataDir: cfg.DataDir,
		Analyzer: tier, Missing: m.TierAMissing, Signer: signer, Now: now}
	if m.Lazy != nil {
		eo.MissingFn = m.Lazy.TierAErr
	}
	m.Ears = ears.New(eo)
	return m, nil
}
