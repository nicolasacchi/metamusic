package app

import (
	"context"
	"log/slog"
	"os"
	"time"

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
	m := &Media{}
	var win clip.WindowAnalyzer
	var render clip.RenderFunc
	var tier ears.TierA
	if mo != nil {
		win, render, tier = mo.Window, mo.Render, mo.TierA
		m.ClipsMissing, m.TierAMissing = mo.ClipsMissing, mo.TierAMissing
	} else {
		f, err := ffmpeg.New(ctx, tools)
		switch {
		case err != nil:
			m.ClipsMissing, m.TierAMissing = err, err
		default:
			m.ClipsMissing, m.TierAMissing = f.RequireClips(), f.RequireTierA()
			win, tier = f, f
		}
	}
	if m.ClipsMissing != nil {
		slog.Warn("clips unavailable: clip-dependent operations answer 503", "err", redact.Error(m.ClipsMissing))
	}
	if m.TierAMissing != nil {
		slog.Warn("tier A unavailable: analysis reports TOOL_MISSING", "err", redact.Error(m.TierAMissing))
	}
	if m.ClipsMissing == nil && win != nil {
		mb := cfg.ClipCacheMB
		if mb <= 0 {
			mb = defaultClipCacheMB
		}
		m.Clips = clip.New(data, cfg.DataDir, tools, win, lib, signer, clip.Options{
			CacheBytes: mb << 20, Now: now, Pinned: pinned, Render: render})
	}
	m.Ears = ears.New(ears.Options{Store: st, Library: lib, Data: data, DataDir: cfg.DataDir,
		Analyzer: tier, Missing: m.TierAMissing, Signer: signer, Now: now})
	return m, nil
}
