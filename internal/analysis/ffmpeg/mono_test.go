package ffmpeg_test

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// TestFFmpeg_Mono (media tier): a synthetic 120 BPM click track in a FLAC decodes to about 11 025 samples per second, and
// the wrapper Tempo agrees with a Mono + ComputeTempo of the same file.
func TestFFmpeg_Mono(t *testing.T) {
	t.Parallel()
	f, tools := newReal(t)
	dir := t.TempDir()
	wav, flac := filepath.Join(dir, "in.wav"), filepath.Join(dir, "clicks.flac")
	writeClickWAV(t, wav, 120, 45)
	if _, err := mediatool.Run(context.Background(), tools.FFmpeg, []string{"-nostdin", "-v", "error", "-i", wav, "-c:a", "flac", flac}, mediatool.RunOpts{Timeout: 60e9, MaxStdout: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	got, err := f.Mono(context.Background(), flac, 240)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rate != 11025 || math.Abs(float64(len(got.Samples))-45*11025) > 11025/10 || math.Abs(got.AnalyzedS-45) > 0.1 {
		t.Fatalf("decode: rate %d, %d samples (want about %d), analyzed %.2f s", got.Rate, len(got.Samples), 45*11025, got.AnalyzedS)
	}
	var peak float64
	for _, v := range got.Samples {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			t.Fatal("NaN or Inf in the decode")
		}
		peak = math.Max(peak, math.Abs(float64(v)))
	}
	if peak < 0.2 || peak > 1.05 {
		t.Errorf("peak %.3f: the decode is not the click track", peak)
	}
	short, err := f.Mono(context.Background(), flac, 10)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(short.AnalyzedS-10) > 0.1 {
		t.Errorf("maxS 10 decoded %.2f s", short.AnalyzedS)
	}
}
