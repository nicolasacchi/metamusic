package ffmpeg

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
)

// STUB(M2b-M1): WindowLoudness lands in window.go (C46); M2b-M1 deletes this file.

var _ clip.WindowAnalyzer = (*FFmpeg)(nil)

// WindowLoudness measures the loudness of [startSample, endSample) of the work-dir master.
func (f *FFmpeg) WindowLoudness(ctx context.Context, workPath string, startSample, endSample int64) (clip.Measure, error) {
	return clip.Measure{}, errors.ErrUnsupported
}
