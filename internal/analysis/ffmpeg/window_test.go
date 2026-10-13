package ffmpeg_test

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// TestWindowLoudness_Golden feeds a capture of ffmpeg n9.0.1 (a windowed
// synthetic tone) through a fake binary and checks I and TP against
// ParseLoudness of the same text.
func TestWindowLoudness_Golden(t *testing.T) {
	t.Parallel()
	capture, err := filepath.Abs("testdata/window_ebur128_n9.0.1.txt")
	if err != nil {
		t.Fatal(err)
	}
	bin, _ := fakeFFmpeg(t, "ffmpeg version n9.0.1 Copyright", okFilters, okEncoders, "cat '"+capture+"'")
	f, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.WindowLoudness(context.Background(), filepath.Join(t.TempDir(), "master.flac"), 48000, 240000)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(m.IntegratedLUFS-(-35.76)) > 1e-9 {
		t.Errorf("I = %v, want -35.76", m.IntegratedLUFS)
	}
	// true_peak 0.018 linear = 20*log10(0.018) dBTP
	if want := 20 * math.Log10(0.018); math.Abs(m.TruePeakDBTP-want) > 0.01 {
		t.Errorf("TP = %v, want %v", m.TruePeakDBTP, want)
	}
}
