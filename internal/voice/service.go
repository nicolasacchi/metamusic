// Package voice holds walkie-talkie voice notes (C259, D-M5-23): validation, content-addressed storage, the MP3 playback
// rendition and the media resolver. A note is immutable: the upload and its rendition are written once under voice/, mode
// 0444, and never touched again; only the MP3 is ever served.
package voice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Limits and rendition settings (C250: no magic numbers elsewhere).
const (
	MaxSeconds = 120
	MaxBytes   = 4 << 20
	MP3Kbps    = 64

	// maxDurationS is the stored cap: the limit plus half a second of container rounding (the voice_notes CHECK).
	maxDurationS = MaxSeconds + 0.5
	// decodeRate and maxDecodeBytes bound the duration probe: 16 kHz mono s16le, at most 125 s of samples.
	decodeRate     = 16000
	maxDecodeBytes = 125 * decodeRate * 2
	decodeTimeout  = 30 * time.Second
	encodeTimeout  = 60 * time.Second
	probeTimeout   = 30 * time.Second
	maxConcurrent  = 2
)

// Options builds the service.
type Options struct {
	Store *store.Store
	// Data is the data root; DataDir its absolute path (ffmpeg takes absolute work-dir paths only).
	Data    *os.Root
	DataDir string
	// FFmpeg is the pinned binary. Empty means voice notes are unavailable (503) while stored notes still play.
	FFmpeg string
	Signer *serve.Signer
	Now    func() time.Time
	NewID  func() string
}

// Service owns the voice_notes table.
type Service struct {
	o     Options
	sem   chan struct{}
	probe sync.Once
	perr  error
}

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o, sem: make(chan struct{}, maxConcurrent)}
}

// Available reports whether new voice notes can be taken: the service is fully wired and ffmpeg has what it needs. The
// capability probe runs once, on first use.
func (s *Service) Available(ctx context.Context) error {
	if s == nil || s.o.Store == nil || s.o.Data == nil || s.o.FFmpeg == "" {
		return fmt.Errorf("%w: voice", analysis.ErrToolMissing)
	}
	s.probe.Do(func() { s.perr = RequireVoice(context.WithoutCancel(ctx), s.o.FFmpeg) })
	return s.perr
}

// ErrUnavailable is what Create returns when Available fails.
var ErrUnavailable = errors.New("voice: unavailable")
