package clip

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

const maxRenderStdout = 1 << 20

// renderArgs is the C46 render invocation for the work-dir paths.
func renderArgs(workMaster, outPath string, sp Spec) []string {
	st, en := sp.Samples()
	af := []string{fmt.Sprintf("atrim=start_sample=%d:end_sample=%d", st, en), "asetpts=PTS-STARTPTS"}
	if sp.SampleRate != OutRate {
		af = append(af, "aresample="+strconv.Itoa(OutRate)+":resampler=swr")
	}
	if sp.GainCDB != 0 {
		af = append(af, fmt.Sprintf("volume=%.2fdB:precision=double", float64(sp.GainCDB)/100))
	}
	fade := float64(FadeMS) / 1000
	af = append(af, fmt.Sprintf("afade=t=in:st=0:d=%.3f", fade),
		fmt.Sprintf("afade=t=out:st=%.3f:d=%.3f", sp.W.DurationS()-fade, fade))
	return []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe",
		"-i", "file:" + workMaster, "-map", "0:a:0", "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact",
		"-af", strings.Join(af, ","),
		"-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", "-compression_level", "5",
		"-f", "flac", "file:" + outPath}
}

// ffmpegRender is the default RenderFunc.
func (s *Service) ffmpegRender(ctx context.Context, workMaster, outPath string, sp Spec) error {
	_, err := mediatool.Run(ctx, s.tools.FFmpeg, renderArgs(workMaster, outPath, sp),
		mediatool.RunOpts{Timeout: 60 * time.Second, MaxStdout: maxRenderStdout})
	return err
}
