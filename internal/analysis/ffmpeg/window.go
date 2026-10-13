package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

const windowTimeout = 60 * time.Second

var _ clip.WindowAnalyzer = (*FFmpeg)(nil)

// WindowLoudness measures the integrated loudness and true peak of
// [startSample, endSample) of the work-dir master (C46).
func (f *FFmpeg) WindowLoudness(ctx context.Context, workPath string, startSample, endSample int64) (clip.Measure, error) {
	if startSample < 0 || endSample <= startSample {
		return clip.Measure{}, fmt.Errorf("ffmpeg: bad window [%d, %d)", startSample, endSample)
	}
	in, err := input(workPath)
	if err != nil {
		return clip.Measure{}, err
	}
	args := append([]string{"-nostdin", "-hide_banner", "-nostats", "-v", "error"}, in...)
	args = append(args, "-af",
		fmt.Sprintf("atrim=start_sample=%d:end_sample=%d,ebur128=peak=true+sample:framelog=quiet:metadata=1,ametadata=mode=print:file=-", startSample, endSample),
		"-f", "null", "-")
	out, err := mediatool.Run(ctx, f.bin, args, mediatool.RunOpts{Timeout: windowTimeout, MaxStdout: maxLoudnessOut})
	if err != nil {
		return clip.Measure{}, err
	}
	l, err := ParseLoudness(bytes.NewReader(out))
	if err != nil {
		return clip.Measure{}, err
	}
	return clip.Measure{IntegratedLUFS: l.IntegratedLUFS, TruePeakDBTP: l.TruePeakDBTP}, nil
}
