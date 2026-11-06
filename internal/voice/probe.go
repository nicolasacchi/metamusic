package voice

import (
	"context"
	"fmt"
	"regexp"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

const maxListing = 4 << 20

// RequireVoice checks what voice notes need from the pinned ffmpeg: decoders for opus, vorbis and aac, the webm (matroska), ogg and
// mp4 demuxers, and the libmp3lame encoder. The error wraps analysis.ErrToolMissing and names the first missing item.
func RequireVoice(ctx context.Context, bin string) error {
	for _, c := range []struct {
		flag string
		want []string
	}{
		{"-decoders", []string{"opus", "vorbis", "aac", "pcm_s16le"}},
		{"-demuxers", []string{"matroska,webm", "ogg", "mov,mp4,m4a,3gp,3g2,mj2"}},
		{"-encoders", []string{"libmp3lame"}},
	} {
		out, err := mediatool.Run(ctx, bin, []string{"-hide_banner", c.flag}, mediatool.RunOpts{Timeout: probeTimeout, MaxStdout: maxListing})
		if err != nil {
			return fmt.Errorf("%w: ffmpeg %s probe", analysis.ErrToolMissing, c.flag)
		}
		for _, n := range c.want {
			if !regexp.MustCompile(`(?m)\s` + regexp.QuoteMeta(n) + `\s`).Match(out) {
				return fmt.Errorf("%w: %s", analysis.ErrToolMissing, n)
			}
		}
	}
	return nil
}
