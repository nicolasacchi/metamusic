package ffmpeg

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/analysis"
)

const tempoShortS = 20 // analysis.ComputeTempo gives no tempo below this

// Tempo measures the tempo of the first 240 s of the work-dir master (C94, mm-tempo/1): Mono plus analysis.ComputeTempo.
// ears no longer calls it (it decodes once through Mono and runs the three analyzers); it stays for callers that want
// only the tempo. durationS is the track duration when known (0 otherwise). Files shorter than 20 s give a Tempo
// without BPM, not an error.
func (f *FFmpeg) Tempo(ctx context.Context, workPath string, durationS float64) (analysis.Tempo, error) {
	if _, err := input(workPath); err != nil {
		return analysis.Tempo{}, err
	}
	if durationS > 0 && durationS < tempoShortS { // too short to measure: no decode
		return analysis.ComputeTempo(nil, monoRate), nil
	}
	pcm, err := f.Mono(ctx, workPath, monoMaxS)
	if err != nil {
		return analysis.Tempo{}, err
	}
	return analysis.ComputeTempo(pcm.Samples, pcm.Rate), nil
}
