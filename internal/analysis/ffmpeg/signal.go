package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

const (
	signalTimeout = 300 * time.Second
	maxSignalOut  = 64 << 20
)

// Signal runs the C49.1 one-pass silence and clipping measurement. sampleRate
// sets the 0.1 s frame size (asetnsamples n = round(sr/10)); durationS closes
// open silence spans and the last frame.
func (f *FFmpeg) Signal(ctx context.Context, workPath string, sampleRate int, durationS float64) (analysis.Silence, analysis.Clipping, error) {
	in, err := input(workPath)
	if err != nil {
		return analysis.Silence{}, analysis.Clipping{}, err
	}
	if sampleRate <= 0 || sampleRate > 768000 {
		return analysis.Silence{}, analysis.Clipping{}, fmt.Errorf("ffmpeg: sample rate %d out of range", sampleRate)
	}
	n := (sampleRate + 5) / 10
	graph := "asetnsamples=n=" + strconv.Itoa(n) + ":p=0," +
		"astats=metadata=1:reset=1:measure_perchannel=none:measure_overall=Peak_level+Flat_factor," +
		"silencedetect=noise=-50dB:duration=1," +
		"ametadata=mode=print:file=-"
	args := append([]string{"-nostdin", "-hide_banner", "-nostats", "-v", "error"}, in...)
	args = append(args, "-af", graph, "-f", "null", "-")
	out, err := mediatool.Run(ctx, f.bin, args, mediatool.RunOpts{Timeout: signalTimeout, MaxStdout: maxSignalOut})
	if err != nil {
		return analysis.Silence{}, analysis.Clipping{}, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return analysis.Silence{}, analysis.Clipping{}, errors.New("ffmpeg: signal pass produced no output")
	}
	return ParseSignal(bytes.NewReader(out), durationS)
}
