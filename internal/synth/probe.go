package synth

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// ProbeFLAC writes the device-probe file (C20): 3 s, 24-bit/48 kHz stereo,
// 440 Hz sine at -20 dBFS per channel, in a single ffmpeg render.
func ProbeFLAC(ctx context.Context, t mediatool.Tools, dst string) error {
	if err := needAbs(dst); err != nil {
		return err
	}
	a := strconv.FormatFloat(math.Pow(10, -20.0/20), 'f', 9, 64)
	ex := a + "*sin(2*PI*440*t)"
	graph := fmt.Sprintf("aevalsrc='%s|%s':s=%d:d=3", ex, ex, sampleRate)
	tmp := dst + ".tmp"
	return publish(tmp, dst, render(ctx, t.FFmpeg, graph, flacOut, tmp))
}
