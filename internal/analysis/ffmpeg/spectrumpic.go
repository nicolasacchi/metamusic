package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

const (
	spectrumTimeout = 120 * time.Second
	maxSpectrumPNG  = 8 << 20
)

// SpectrumPicture renders the C50.1 log-frequency spectrogram PNG (mono
// downmix, legend on) of the work-dir master. The PNG is (w+2*x0) x (h+128).
func (f *FFmpeg) SpectrumPicture(ctx context.Context, workPath, outPNG string, w, h int) error {
	if !filepath.IsAbs(workPath) || !filepath.IsAbs(outPNG) {
		return errors.New("ffmpeg: work paths must be absolute")
	}
	if w < 64 || w > 4096 || h < 64 || h > 4096 {
		return fmt.Errorf("ffmpeg: spectrogram size %dx%d out of range", w, h)
	}
	if err := os.Remove(outPNG); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	graph := "[0:a:0]aformat=channel_layouts=mono,showspectrumpic=s=" + strconv.Itoa(w) + "x" + strconv.Itoa(h) +
		":legend=1:fscale=log:scale=log:color=magma[v]"
	args := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", "file:" + workPath,
		"-filter_complex", graph, "-map", "[v]", "-frames:v", "1", "-update", "1", "-c:v", "png", "-f", "image2", "file:" + outPNG}
	if _, err := mediatool.Run(ctx, f.bin, args, mediatool.RunOpts{Timeout: spectrumTimeout, MaxStdout: 1 << 20}); err != nil {
		return err
	}
	st, err := os.Stat(outPNG)
	if err != nil {
		return err
	}
	if st.Size() > maxSpectrumPNG {
		return fmt.Errorf("ffmpeg: spectrogram picture %d bytes exceeds limit", st.Size())
	}
	return nil
}
