package ffmpeg

import (
	"context"
	"encoding/binary"
	"math"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

const (
	monoRate    = 11025
	monoMaxS    = 240 // the longest decode the analyzers use
	monoTimeout = 120 * time.Second
)

// Mono decodes the first maxS seconds (at most 240; 240 when maxS <= 0) of the work-dir master to mono 11 025 Hz float32
// (C136.0): the one decode that tempo, key and energy share. NaN and Inf samples are zeroed.
func (f *FFmpeg) Mono(ctx context.Context, workPath string, maxS int) (analysis.PCM, error) {
	in, err := input(workPath)
	if err != nil {
		return analysis.PCM{}, err
	}
	if maxS <= 0 || maxS > monoMaxS {
		maxS = monoMaxS
	}
	args := append([]string{"-nostdin", "-hide_banner", "-nostats", "-v", "error"}, in...)
	args = append(args, "-ac", "1", "-ar", strconv.Itoa(monoRate), "-t", strconv.Itoa(maxS), "-f", "f32le", "-")
	out, err := mediatool.Run(ctx, f.bin, args, mediatool.RunOpts{Timeout: monoTimeout, MaxStdout: monoRate*int64(maxS)*4 + 1<<16}) // f32le bytes of the longest decode, plus slack
	if err != nil {
		return analysis.PCM{}, err
	}
	x := make([]float32, len(out)/4)
	for i := range x {
		v := math.Float32frombits(binary.LittleEndian.Uint32(out[4*i:]))
		if f64 := float64(v); math.IsNaN(f64) || math.IsInf(f64, 0) { // NaN or Inf from a corrupt decode
			v = 0
		}
		x[i] = v
	}
	return analysis.NewPCM(x, monoRate), nil
}
