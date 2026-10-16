package ffmpeg

import (
	"context"
	"encoding/binary"
	"math"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

const (
	tempoRate    = 11025
	tempoMaxS    = 240
	tempoShortS  = 20 // analysis.ComputeTempo gives no tempo below this
	tempoTimeout = 120 * time.Second
	maxTempoOut  = tempoRate*tempoMaxS*4 + 1<<16 // f32le bytes of the longest decode, plus slack
)

// Tempo decodes the first 240 s of the work-dir master to mono 11 025 Hz float32 and measures its tempo
// (C94, mm-tempo/1). durationS is the track duration when known (0 otherwise). Files shorter than 20 s give a
// Tempo without BPM, not an error.
func (f *FFmpeg) Tempo(ctx context.Context, workPath string, durationS float64) (analysis.Tempo, error) {
	in, err := input(workPath)
	if err != nil {
		return analysis.Tempo{}, err
	}
	if durationS > 0 && durationS < tempoShortS { // too short to measure: no decode
		return analysis.ComputeTempo(nil, tempoRate), nil
	}
	args := append([]string{"-nostdin", "-hide_banner", "-nostats", "-v", "error"}, in...)
	args = append(args, "-ac", "1", "-ar", "11025", "-t", "240", "-f", "f32le", "-")
	out, err := mediatool.Run(ctx, f.bin, args, mediatool.RunOpts{Timeout: tempoTimeout, MaxStdout: maxTempoOut})
	if err != nil {
		return analysis.Tempo{}, err
	}
	x := make([]float32, len(out)/4)
	for i := range x {
		v := math.Float32frombits(binary.LittleEndian.Uint32(out[4*i:]))
		if f64 := float64(v); math.IsNaN(f64) || math.IsInf(f64, 0) { // NaN or Inf from a corrupt decode
			v = 0
		}
		x[i] = v
	}
	return analysis.ComputeTempo(x, tempoRate), nil
}
