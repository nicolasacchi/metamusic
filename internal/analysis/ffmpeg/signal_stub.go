package ffmpeg

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/analysis"
)

// STUB(M2b-E1): Signal lands in signal.go (C49.1); M2b-E1 deletes this file.

// Signal runs the one-pass silence and clipping measurement.
func (f *FFmpeg) Signal(ctx context.Context, workPath string, sampleRate int, durationS float64) (analysis.Silence, analysis.Clipping, error) {
	return analysis.Silence{}, analysis.Clipping{}, errors.ErrUnsupported
}
