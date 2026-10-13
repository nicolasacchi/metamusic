package ffmpeg

import (
	"context"
	"errors"
)

// STUB(M2b-E2): SpectrumPicture lands in spectrumpic.go (C50.1); M2b-E2 deletes this file.

// SpectrumPicture renders the log-frequency spectrogram PNG of the work-dir master.
func (f *FFmpeg) SpectrumPicture(ctx context.Context, workPath, outPNG string, w, h int) error {
	return errors.ErrUnsupported
}
