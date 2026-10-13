// Package clip renders, caches and serves the loudness-matched, sample-aligned
// audio clips duels and get_clip hand out (C46). Clips are input-addressed
// (D-M2b-1): the key is a hash of everything that determines the bytes.
package clip

import (
	"context"
	"time"
)

// Clip format constants (C46).
const (
	Def        = "mm-clip/1"
	OutRate    = 48000
	FadeMS     = 10
	GridMS     = 100
	MinClipMS  = 30000
	MaxClipMS  = 45000
	SilentLUFS = -60.0
	MaxCutCDB  = -2400 // -24 dB
)

// Window is a clip window on the 100 ms grid, in milliseconds from the track start.
type Window struct{ StartMS, EndMS int64 }

// DurationS is the window length in seconds.
func (w Window) DurationS() float64 { return float64(w.EndMS-w.StartMS) / 1000 }

// Measure is the loudness of one window.
type Measure struct{ IntegratedLUFS, TruePeakDBTP float64 }

// Spec fixes everything that determines a clip's bytes.
type Spec struct {
	MasterSHA256, MasterPath string // from library.TrackRef
	SampleRate, Channels     int    // source; Channels must be 1 or 2 (else 422 CLIP_UNSUPPORTED_CHANNELS)
	W                        Window
	GainCDB                  int // <= 0
}

// Object is a rendered clip in the cache.
type Object struct {
	Key, RelPath string
	Bytes        int64
	DurationS    float64 // (E-S)/SampleRate
	Channels     int     // output channels (= source, 1 or 2)
}

// WindowAnalyzer measures a window of a work-dir master; *ffmpeg.FFmpeg implements it.
type WindowAnalyzer interface {
	ToolVersion() string
	WindowLoudness(ctx context.Context, workPath string, startSample, endSample int64) (Measure, error)
}

// RenderFunc cuts the clip of sp from workMaster into outPath.
type RenderFunc func(ctx context.Context, workMaster, outPath string, sp Spec) error

// Options tunes a Service.
type Options struct {
	CacheBytes int64                                                  // > 0; app.NewMedia: ClipCacheMB << 20
	Now        func() time.Time                                       // nil => time.Now
	Pinned     func(ctx context.Context) (map[string]struct{}, error) // keys never evicted; nil => none
	Render     RenderFunc                                             // nil => the ffmpeg render
}

// Query is the get_clip window and loudness request.
type Query struct{ FromS, ToS, MatchLUFS *float64 }

// Link is the get_clip answer.
type Link struct {
	TrackID, URL, Key, ContentType string // ContentType "audio/flac"
	ExpiresAt                      time.Time
	TTLS                           int64
	Bytes                          int64
	SampleRate, BitsPerSample      int // 48000, 24
	Channels                       int
	FromS, ToS, DurationS          float64
	GainDB                         float64
	WindowLUFS, WindowTPDBTP       *float64
	TargetLUFS                     *float64
	Match                          string // off | matched | limited | window_silent
}
